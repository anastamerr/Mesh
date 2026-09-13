package executor

import (
	"context"
	"net"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDockerCreateAppliesRestrictedJobPolicy(t *testing.T) {
	collectionID := strings.Repeat("a", 64)
	workload := job()
	workload.InputCollectionID = &collectionID
	input := filepath.Join(t.TempDir(), "input")
	var arguments []string
	docker := &DockerCLI{program: "docker", command: func(_ context.Context, values ...string) ([]byte, error) {
		arguments = append([]string(nil), values...)
		return nil, nil
	}}
	if err := docker.Create(context.Background(), containerName(workload.ID), Request{Workload: workload, InputPath: input}); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"--read-only", "--cap-drop", "ALL", "no-new-privileges", "--pids-limit",
		"--cpus", "--memory", "--tmpfs", "--mount", "--network", "none"} {
		if !slices.Contains(arguments, required) {
			t.Fatalf("container policy omitted %q: %v", required, arguments)
		}
	}
	joined := strings.Join(arguments, " ")
	if !strings.Contains(joined, "target=/mesh/input,readonly") || !strings.Contains(joined, "target=/mesh/data") ||
		strings.Contains(joined, "--privileged") {
		t.Fatalf("unsafe or incomplete mount policy: %v", arguments)
	}
	if len(arguments) < 2 || arguments[len(arguments)-2] != workload.Image || arguments[len(arguments)-1] != "run" {
		t.Fatalf("image and typed command were not final arguments: %v", arguments)
	}
}

func TestDockerApplicationIngressRequiresExactOwnershipLabels(t *testing.T) {
	workload := job()
	workload.Kind = "application"
	port := 8080
	workload.ServicePort = &port
	var address string
	docker := &DockerCLI{program: "docker", command: func(_ context.Context, values ...string) ([]byte, error) {
		if slices.Contains(values, "ls") {
			return []byte(containerName(workload.ID) + "\n"), nil
		}
		return []byte(`{"Config":{"Image":"example/app@sha256:` + strings.Repeat("a", 64) + `","Labels":{` +
			`"mesh.workload.id":"` + workload.ID + `","mesh.workload.revision":"1",` +
			`"mesh.workload.kind":"application","mesh.workload.port":"8080"}},` +
			`"State":{"Status":"running","ExitCode":0},"NetworkSettings":{"Networks":{"bridge":{"IPAddress":"172.17.0.8"}}}}`), nil
	}, dial: func(_ context.Context, _, target string) (net.Conn, error) {
		address = target
		client, server := net.Pipe()
		_ = server.Close()
		return client, nil
	}}
	connection, err := docker.DialApplication(context.Background(), workload.ID, 1, port)
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	if address != "172.17.0.8:8080" {
		t.Fatalf("unexpected application target %q", address)
	}
	if _, err := docker.DialApplication(context.Background(), workload.ID, 2, port); err == nil {
		t.Fatal("application ingress accepted a different assignment revision")
	}
}

func TestDockerExportUsesOwnedContainerDataPath(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "output")
	var arguments []string
	docker := &DockerCLI{program: "docker", command: func(_ context.Context, values ...string) ([]byte, error) {
		arguments = append([]string(nil), values...)
		return nil, nil
	}}
	if err := docker.Export(context.Background(), "mesh-owned", destination); err != nil {
		t.Fatal(err)
	}
	expected := []string{"container", "cp", "mesh-owned:/mesh/data/.", destination}
	if !slices.Equal(arguments, expected) {
		t.Fatalf("unexpected export command: %v", arguments)
	}
}
