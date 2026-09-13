package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"mesh.local/agent/internal/stream"
)

const maxDockerOutput = 64 * 1024

type DockerCLI struct {
	program string
	command func(context.Context, ...string) ([]byte, error)
	dial    func(context.Context, string, string) (net.Conn, error)
}

func NewDockerCLI(program string) (*DockerCLI, error) {
	if strings.TrimSpace(program) == "" {
		return nil, errors.New("docker executable is required")
	}
	return &DockerCLI{program: program}, nil
}

func (docker *DockerCLI) run(ctx context.Context, arguments ...string) ([]byte, error) {
	if docker.command != nil {
		return docker.command(ctx, arguments...)
	}
	command := exec.CommandContext(ctx, docker.program, arguments...)
	output := &stream.LimitedBuffer{Remaining: maxDockerOutput}
	command.Stdout = output
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("container runtime command failed")
	}
	if output.Truncated {
		return nil, errors.New("container runtime response exceeded limit")
	}
	return output.Buffer.Bytes(), nil
}

func (docker *DockerCLI) Inspect(ctx context.Context, name string) (Container, bool, error) {
	listed, err := docker.run(ctx, "container", "ls", "--all", "--filter", "name=^/"+name+"$", "--format", "{{.Names}}")
	if err != nil {
		return Container{}, false, err
	}
	if strings.TrimSpace(string(listed)) == "" {
		return Container{}, false, nil
	}
	if strings.TrimSpace(string(listed)) != name {
		return Container{}, false, errors.New("container runtime returned an ambiguous name")
	}
	data, err := docker.run(ctx, "container", "inspect", "--format", "{{json .}}", name)
	if err != nil {
		return Container{}, false, err
	}
	var inspected struct {
		Config struct {
			Image  string            `json:"Image"`
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
		State struct {
			Status   string `json:"Status"`
			ExitCode int    `json:"ExitCode"`
		} `json:"State"`
		NetworkSettings struct {
			Networks map[string]struct {
				IPAddress string `json:"IPAddress"`
			} `json:"Networks"`
		} `json:"NetworkSettings"`
	}
	// Docker inspect is intentionally decoded into a narrow projection; the
	// runtime returns many unrelated fields that are not part of this boundary.
	if err := json.Unmarshal(data, &inspected); err != nil {
		return Container{}, false, errors.New("invalid container runtime response")
	}
	addresses := make([]string, 0, len(inspected.NetworkSettings.Networks))
	for _, network := range inspected.NetworkSettings.Networks {
		if network.IPAddress != "" {
			addresses = append(addresses, network.IPAddress)
		}
	}
	revision, err := strconv.ParseUint(inspected.Config.Labels["mesh.workload.revision"], 10, 64)
	servicePort, portError := strconv.Atoi(inspected.Config.Labels["mesh.workload.port"])
	if err != nil {
		return Container{WorkloadID: inspected.Config.Labels["mesh.workload.id"], Image: inspected.Config.Image,
			Kind: inspected.Config.Labels["mesh.workload.kind"], ServicePort: servicePort, Addresses: addresses,
			State: inspected.State.Status, ExitCode: inspected.State.ExitCode}, true, nil
	}
	if portError != nil {
		servicePort = 0
	}
	return Container{WorkloadID: inspected.Config.Labels["mesh.workload.id"], Revision: revision,
		Kind: inspected.Config.Labels["mesh.workload.kind"], ServicePort: servicePort, Addresses: addresses,
		Image: inspected.Config.Image, State: inspected.State.Status, ExitCode: inspected.State.ExitCode}, true, nil
}

func (docker *DockerCLI) Pull(ctx context.Context, image string) error {
	_, err := docker.run(ctx, "image", "pull", image)
	return err
}

func (docker *DockerCLI) RuntimeVersion(ctx context.Context) (string, error) {
	data, err := docker.run(ctx, "version", "--format", "{{.Server.Version}}")
	if err != nil {
		return "", err
	}
	version := strings.TrimSpace(string(data))
	if version == "" || len(version) > 40 {
		return "", errors.New("container runtime returned an invalid version")
	}
	for _, character := range version {
		if !(character >= 'A' && character <= 'Z') && !(character >= 'a' && character <= 'z') &&
			!(character >= '0' && character <= '9') && character != '.' && character != '+' && character != '-' {
			return "", errors.New("container runtime returned an invalid version")
		}
	}
	return version, nil
}

func (docker *DockerCLI) Create(ctx context.Context, name string, request Request) error {
	workload := request.Workload
	arguments := []string{"container", "create", "--name", name,
		"--label", "mesh.workload.id=" + workload.ID,
		"--label", "mesh.workload.revision=" + strconv.FormatUint(workload.Revision, 10),
		"--label", "mesh.workload.kind=" + workload.Kind,
		"--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
		"--pids-limit", "256", "--cpus", strconv.FormatFloat(float64(workload.Resources.CPUMillis)/1000, 'f', 3, 64),
		"--memory", strconv.FormatUint(workload.Resources.MemoryBytes, 10),
		"--tmpfs", "/tmp:rw,noexec,nosuid,size=67108864",
		"--mount", "type=volume,source=mesh-data-" + workload.ID + ",target=/mesh/data"}
	if request.InputPath != "" {
		arguments = append(arguments, "--mount", "type=bind,source="+request.InputPath+",target=/mesh/input,readonly")
	}
	if workload.Kind == "application" {
		arguments = append(arguments, "--network", "bridge", "--restart", "unless-stopped",
			"--label", "mesh.workload.port="+strconv.Itoa(*workload.ServicePort),
			"--expose", strconv.Itoa(*workload.ServicePort))
	} else {
		arguments = append(arguments, "--network", "none")
	}
	arguments = append(arguments, workload.Image)
	arguments = append(arguments, workload.Command...)
	_, err := docker.run(ctx, arguments...)
	return err
}

// DialApplication opens only the exact running application container and port
// described by immutable Mesh ownership labels. Docker ports are never bound
// to the host, so this is the sole executor-side ingress boundary.
func (docker *DockerCLI) DialApplication(ctx context.Context, workloadID string, revision uint64, port int) (net.Conn, error) {
	container, exists, err := docker.Inspect(ctx, containerName(workloadID))
	if err != nil {
		return nil, err
	}
	if !exists || container.WorkloadID != workloadID || container.Revision != revision || container.Kind != "application" ||
		container.ServicePort != port || container.State != "running" || len(container.Addresses) != 1 {
		return nil, errors.New("application container is unavailable or does not match its assignment")
	}
	ip := net.ParseIP(container.Addresses[0])
	if ip == nil || ip.IsUnspecified() || ip.IsLoopback() || ip.IsMulticast() {
		return nil, errors.New("application container returned an invalid address")
	}
	dial := docker.dial
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	return dial(ctx, "tcp", net.JoinHostPort(ip.String(), strconv.Itoa(port)))
}

func (docker *DockerCLI) Start(ctx context.Context, name string) error {
	_, err := docker.run(ctx, "container", "start", name)
	return err
}

func (docker *DockerCLI) Stop(ctx context.Context, name string) error {
	_, err := docker.run(ctx, "container", "stop", "--time", "20", name)
	return err
}

func (docker *DockerCLI) Remove(ctx context.Context, name string) error {
	_, err := docker.run(ctx, "container", "rm", name)
	return err
}

func (docker *DockerCLI) Export(ctx context.Context, name, destination string) error {
	if err := os.MkdirAll(destination, 0700); err != nil {
		return errors.New("cannot create job output directory")
	}
	info, err := os.Lstat(destination)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("job output path is not a real directory")
	}
	_, err = docker.run(ctx, "container", "cp", name+":/mesh/data/.", destination)
	return err
}

func (docker *DockerCLI) String() string {
	return fmt.Sprintf("DockerCLI(%s)", docker.program)
}
