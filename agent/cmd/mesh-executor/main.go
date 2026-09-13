// mesh-executor runs inside Linux and owns the restricted Docker integration.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"mesh.local/agent/internal/buildinfo"
	"mesh.local/agent/internal/executor"
)

const maxRequestBytes = 64 * 1024

func run(ctx context.Context, arguments []string, input io.Reader, output io.Writer) error {
	if len(arguments) == 1 && arguments[0] == "version" {
		_, err := fmt.Fprintln(output, buildinfo.Version)
		return err
	}
	if len(arguments) < 1 || (arguments[0] != "reconcile" && arguments[0] != "doctor" && arguments[0] != "proxy") {
		return errors.New("usage: mesh-executor version | doctor | reconcile | proxy [options]")
	}
	flags := flag.NewFlagSet(arguments[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	dockerProgram := flags.String("docker", "docker", "container runtime executable")
	workloadID := flags.String("workload", "", "application workload UUID")
	revision := flags.Uint64("revision", 0, "application revision")
	port := flags.Int("port", 0, "application container port")
	if err := flags.Parse(arguments[1:]); err != nil || flags.NArg() != 0 {
		return errors.New("invalid executor options")
	}
	docker, err := executor.NewDockerCLI(*dockerProgram)
	if err != nil {
		return err
	}
	if arguments[0] != "proxy" && (*workloadID != "" || *revision != 0 || *port != 0) {
		return errors.New("invalid executor options")
	}
	if arguments[0] == "doctor" {
		version, err := docker.RuntimeVersion(ctx)
		if err != nil {
			return errors.New("Docker is not ready inside the execution environment")
		}
		return json.NewEncoder(output).Encode(struct {
			Architecture string `json:"architecture"`
			Runtime      string `json:"runtime"`
			Version      string `json:"version"`
		}{runtime.GOARCH, "docker", version})
	}
	if arguments[0] == "proxy" {
		return executor.Proxy(ctx, docker, *workloadID, *revision, *port, input, output)
	}
	data, err := io.ReadAll(io.LimitReader(input, maxRequestBytes+1))
	if err != nil || len(data) > maxRequestBytes {
		return errors.New("invalid executor request")
	}
	var request executor.Request
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		return errors.New("invalid executor request")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("invalid executor request")
	}
	return json.NewEncoder(output).Encode(executor.Reconcile(ctx, docker, request))
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
