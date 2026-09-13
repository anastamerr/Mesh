package compute

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"mesh.local/agent/internal/control"
	"mesh.local/agent/internal/executor"
)

type fakeController struct {
	mu           sync.Mutex
	workloads    []control.Workload
	observations []control.WorkloadObservation
	environment  control.ExecutionEnvironmentReport
}

func (controller *fakeController) ReportExecutionEnvironment(_ context.Context, nodeID, _ string,
	report control.ExecutionEnvironmentReport) (control.ExecutionEnvironment, error) {
	controller.environment = report
	return control.ExecutionEnvironment{ExecutionEnvironmentReport: report,
		ID: "32345678-1234-4234-8234-123456789abc", NodeID: nodeID}, nil
}

func (controller *fakeController) Workloads(context.Context, string, string) ([]control.Workload, error) {
	return controller.workloads, nil
}
func (controller *fakeController) ObserveWorkload(_ context.Context, _, _, _ string, observation control.WorkloadObservation) error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	controller.observations = append(controller.observations, observation)
	return nil
}

type fakeLauncher struct {
	mu           sync.Mutex
	requests     []executor.Request
	observation  control.WorkloadObservation
	architecture string
	failWorkload string
}

func (launcher *fakeLauncher) Ready(context.Context) (string, string, error) {
	architecture := launcher.architecture
	if architecture == "" {
		architecture = "amd64"
	}
	return "28.1.0", architecture, nil
}

func (launcher *fakeLauncher) Reconcile(_ context.Context, request executor.Request) (control.WorkloadObservation, error) {
	launcher.mu.Lock()
	defer launcher.mu.Unlock()
	launcher.requests = append(launcher.requests, request)
	if request.Workload.ID == launcher.failWorkload {
		return control.WorkloadObservation{}, errors.New("executor process failed")
	}
	if launcher.observation.State != "" {
		return launcher.observation, nil
	}
	return control.WorkloadObservation{Revision: request.Workload.Revision, State: "running"}, nil
}

func TestOnceRetriesUnavailableExecutorWithoutLosingOtherAssignments(t *testing.T) {
	controller := &fakeController{workloads: []control.Workload{
		{WorkloadSpec: control.WorkloadSpec{ID: "first", Kind: "application"}, Revision: 1},
		{WorkloadSpec: control.WorkloadSpec{ID: "second", Kind: "application"}, Revision: 1},
	}}
	launcher := &fakeLauncher{failWorkload: "first"}
	root := t.TempDir()
	if err := Once(context.Background(), controller, launcher, nil, "node", "credential", root); err == nil {
		t.Fatal("unavailable executor was hidden")
	}
	if len(launcher.requests) != 2 || len(controller.observations) != 1 || controller.observations[0].State != "running" {
		t.Fatalf("temporary failure became terminal or blocked another assignment: %#v", controller.observations)
	}
	launcher.failWorkload = ""
	controller.observations = nil
	if err := Once(context.Background(), controller, launcher, nil, "node", "credential", root); err != nil {
		t.Fatal(err)
	}
	if len(controller.observations) != 2 {
		t.Fatal("assignments did not recover")
	}
}

type fakePublisher struct {
	paths []string
	id    string
	fail  bool
}

type blockedLauncher struct {
	fakeLauncher
	entered chan string
	release chan struct{}
}

func (launcher *blockedLauncher) Reconcile(ctx context.Context, request executor.Request) (control.WorkloadObservation, error) {
	launcher.entered <- request.Workload.ID
	select {
	case <-launcher.release:
		return control.WorkloadObservation{Revision: request.Workload.Revision, State: "running"}, nil
	case <-ctx.Done():
		return control.WorkloadObservation{}, ctx.Err()
	}
}

func TestOnceBoundsConcurrentReconciliationAndJoinsCancelledWorkers(t *testing.T) {
	controller := &fakeController{workloads: []control.Workload{
		{WorkloadSpec: control.WorkloadSpec{ID: "first", Kind: "application"}, Revision: 1},
		{WorkloadSpec: control.WorkloadSpec{ID: "second", Kind: "application"}, Revision: 1},
		{WorkloadSpec: control.WorkloadSpec{ID: "third", Kind: "application"}, Revision: 1},
	}}
	launcher := &blockedLauncher{entered: make(chan string, 3), release: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	root := t.TempDir()
	go func() { done <- Once(ctx, controller, launcher, nil, "node", "credential", root) }()
	for range 2 {
		select {
		case <-launcher.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("one blocked workload prevented independent reconciliation")
		}
	}
	select {
	case <-launcher.entered:
		t.Fatal("reconciliation exceeded its worker bound")
	case <-time.After(50 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancellation was hidden")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled workers were not joined")
	}
	if len(controller.observations) != 0 {
		t.Fatal("cancelled invocations became job observations")
	}
}

func (publisher *fakePublisher) Publish(_ context.Context, path string) (string, error) {
	publisher.paths = append(publisher.paths, path)
	if publisher.fail {
		return "", errors.New("unavailable")
	}
	return publisher.id, nil
}

func TestOnceMapsOnlyApprovedCollectionPath(t *testing.T) {
	collectionID := strings.Repeat("a", 64)
	controller := &fakeController{workloads: []control.Workload{{
		WorkloadSpec: control.WorkloadSpec{ID: "12345678-1234-4234-8234-123456789abc",
			NodeID: "22345678-1234-4234-8234-123456789abc", Name: "job", Kind: "job",
			Image: "example/job@sha256:" + strings.Repeat("b", 64), Command: []string{"run"},
			Resources:         control.WorkloadResources{CPUMillis: 500, MemoryBytes: 128 * 1024 * 1024},
			InputCollectionID: &collectionID, DesiredState: "running"},
		ExecutionEnvironmentID: "32345678-1234-4234-8234-123456789abc", Revision: 1,
	}}}
	launcher := &fakeLauncher{architecture: "arm64"}
	root := t.TempDir()
	if err := Once(context.Background(), controller, launcher, nil,
		controller.workloads[0].NodeID, "credential", root); err != nil {
		t.Fatal(err)
	}
	expected, _ := filepath.Abs(filepath.Join(root, "collections", collectionID))
	expectedOutput, _ := filepath.Abs(filepath.Join(root, ".mesh", "job-output", controller.workloads[0].ID, "1"))
	if len(launcher.requests) != 1 || launcher.requests[0].InputPath != expected ||
		launcher.requests[0].OutputPath != expectedOutput || len(controller.observations) != 1 ||
		controller.environment.Architecture != "arm64" {
		t.Fatal("collection path or observation was not reconciled")
	}
}

func TestOncePublishesSuccessfulJobBeforeReportingIt(t *testing.T) {
	workload := control.Workload{WorkloadSpec: control.WorkloadSpec{
		ID: "12345678-1234-4234-8234-123456789abc", NodeID: "22345678-1234-4234-8234-123456789abc",
		Name: "job", Kind: "job", Image: "example/job@sha256:" + strings.Repeat("b", 64), Command: []string{"run"},
		Resources: control.WorkloadResources{CPUMillis: 500, MemoryBytes: 128 * 1024 * 1024}, DesiredState: "running",
	}, ExecutionEnvironmentID: "32345678-1234-4234-8234-123456789abc", Revision: 1}
	controller := &fakeController{workloads: []control.Workload{workload}}
	exitCode := 0
	launcher := &fakeLauncher{observation: control.WorkloadObservation{Revision: 1, State: "succeeded", ExitCode: &exitCode}}
	publisher := &fakePublisher{id: strings.Repeat("c", 64)}
	root := t.TempDir()
	if err := Once(context.Background(), controller, launcher, publisher, workload.NodeID, "credential", root); err != nil {
		t.Fatal(err)
	}
	observation := controller.observations[0]
	if len(publisher.paths) != 1 || observation.OutputCollectionID == nil || *observation.OutputCollectionID != publisher.id {
		t.Fatalf("successful output was not published: %#v %#v", publisher.paths, observation)
	}
	publisher.fail = true
	controller.observations = nil
	if err := Once(context.Background(), controller, launcher, publisher, workload.NodeID, "credential", root); err != nil {
		t.Fatal(err)
	}
	if controller.observations[0].State != "exporting" || controller.observations[0].ExitCode != nil {
		t.Fatalf("failed publication became terminal: %#v", controller.observations[0])
	}
}
