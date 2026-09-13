package compute

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"mesh.local/agent/internal/control"
	"mesh.local/agent/internal/executor"
)

const maxExecutorResponse = 64 * 1024

type boundedOutput struct {
	buffer    bytes.Buffer
	remaining int
	truncated bool
}

func (output *boundedOutput) Write(data []byte) (int, error) {
	written := len(data)
	if len(data) > output.remaining {
		data = data[:output.remaining]
		output.truncated = true
	}
	output.remaining -= len(data)
	_, _ = output.buffer.Write(data)
	return written, nil
}

type ProcessLauncher struct {
	program      string
	arguments    []string
	windows      bool
	distribution string
}

func (launcher *ProcessLauncher) Ready(ctx context.Context) (string, string, error) {
	arguments := []string{"doctor"}
	if launcher.windows {
		arguments = []string{"--distribution", launcher.distribution, "--user", "root", "--exec", "mesh-executor", "doctor"}
	}
	output, err := commandOutput(ctx, launcher.program, nil, arguments...)
	if err != nil {
		return "", "", err
	}
	var status struct {
		Architecture string `json:"architecture"`
		Runtime      string `json:"runtime"`
		Version      string `json:"version"`
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&status); err != nil || status.Runtime != "docker" ||
		(status.Architecture != "amd64" && status.Architecture != "arm64") || status.Version == "" || len(status.Version) > 40 {
		return "", "", errors.New("invalid executor readiness response")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return "", "", errors.New("invalid executor readiness response")
	}
	return status.Version, status.Architecture, nil
}

func NewProcessLauncher(distribution string) *ProcessLauncher {
	if runtime.GOOS == "windows" {
		return &ProcessLauncher{program: "wsl.exe", arguments: []string{"--distribution", distribution, "--user", "root", "--exec", "mesh-executor", "reconcile"},
			windows: true, distribution: distribution}
	}
	return &ProcessLauncher{program: "mesh-executor", arguments: []string{"reconcile"}}
}

func commandOutput(ctx context.Context, program string, input []byte, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, program, arguments...)
	command.Stdin = bytes.NewReader(input)
	output := &boundedOutput{remaining: maxExecutorResponse}
	command.Stdout = output
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("executor process failed")
	}
	if output.truncated {
		return nil, errors.New("executor response exceeded limit")
	}
	return output.buffer.Bytes(), nil
}

func (launcher *ProcessLauncher) linuxPath(ctx context.Context, path string) (string, error) {
	if !launcher.windows || path == "" {
		return path, nil
	}
	output, err := commandOutput(ctx, launcher.program, nil, "--distribution", launcher.distribution,
		"--user", "root", "--exec", "wslpath", "--absolute", "--unix", path)
	if err != nil {
		return "", err
	}
	converted := strings.TrimSpace(string(output))
	if converted == "" || !strings.HasPrefix(converted, "/") {
		return "", errors.New("WSL returned an invalid collection path")
	}
	return converted, nil
}

func (launcher *ProcessLauncher) Reconcile(ctx context.Context, request executor.Request) (control.WorkloadObservation, error) {
	var err error
	request.InputPath, err = launcher.linuxPath(ctx, request.InputPath)
	if err != nil {
		return control.WorkloadObservation{}, err
	}
	request.OutputPath, err = launcher.linuxPath(ctx, request.OutputPath)
	if err != nil {
		return control.WorkloadObservation{}, err
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return control.WorkloadObservation{}, errors.New("cannot encode executor request")
	}
	output, err := commandOutput(ctx, launcher.program, payload, launcher.arguments...)
	if err != nil {
		return control.WorkloadObservation{}, err
	}
	var observation control.WorkloadObservation
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&observation); err != nil {
		return observation, errors.New("invalid executor response")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return observation, errors.New("invalid executor response")
	}
	return observation, nil
}

func (launcher *ProcessLauncher) Proxy(ctx context.Context, workload control.Workload) (net.Conn, error) {
	if !control.ValidWorkload(workload, workload.NodeID) || workload.Kind != "application" || workload.ServicePort == nil {
		return nil, errors.New("invalid application proxy assignment")
	}
	arguments := []string{"proxy", "--workload", workload.ID, "--revision",
		strconv.FormatUint(workload.Revision, 10), "--port", strconv.Itoa(*workload.ServicePort)}
	if launcher.windows {
		arguments = append([]string{"--distribution", launcher.distribution, "--user", "root", "--exec", "mesh-executor"}, arguments...)
	}
	command := exec.CommandContext(ctx, launcher.program, arguments...)
	input, err := command.StdinPipe()
	if err != nil {
		return nil, errors.New("cannot open executor proxy input")
	}
	output, err := command.StdoutPipe()
	if err != nil {
		_ = input.Close()
		return nil, errors.New("cannot open executor proxy output")
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		_ = input.Close()
		_ = output.Close()
		return nil, errors.New("cannot start executor proxy")
	}
	connection := &processConn{input: input, output: output, command: command, done: make(chan struct{})}
	go func() {
		_ = command.Wait()
		close(connection.done)
	}()
	return connection, nil
}

type processConn struct {
	input   io.WriteCloser
	output  io.ReadCloser
	command *exec.Cmd
	done    chan struct{}
	close   sync.Once
}

func (connection *processConn) Read(data []byte) (int, error)    { return connection.output.Read(data) }
func (connection *processConn) Write(data []byte) (int, error)   { return connection.input.Write(data) }
func (connection *processConn) LocalAddr() net.Addr              { return processAddr("mesh-agent") }
func (connection *processConn) RemoteAddr() net.Addr             { return processAddr("mesh-executor") }
func (connection *processConn) SetDeadline(time.Time) error      { return nil }
func (connection *processConn) SetReadDeadline(time.Time) error  { return nil }
func (connection *processConn) SetWriteDeadline(time.Time) error { return nil }
func (connection *processConn) Close() error {
	connection.close.Do(func() {
		_ = connection.input.Close()
		_ = connection.output.Close()
		if connection.command.Process != nil {
			_ = connection.command.Process.Kill()
		}
	})
	return nil
}

type processAddr string

func (address processAddr) Network() string { return "stdio" }
func (address processAddr) String() string  { return string(address) }
