package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mesh.local/agent/internal/control"
)

type fakeRuntime struct {
	container Container
	exists    bool
	actions   []string
	fail      string
}

func (runtime *fakeRuntime) action(name string) error {
	runtime.actions = append(runtime.actions, name)
	if runtime.fail == name {
		return context.DeadlineExceeded
	}
	return nil
}
func (runtime *fakeRuntime) Inspect(context.Context, string) (Container, bool, error) {
	if err := runtime.action("inspect"); err != nil {
		return Container{}, false, err
	}
	return runtime.container, runtime.exists, nil
}
func (runtime *fakeRuntime) Pull(context.Context, string) error { return runtime.action("pull") }
func (runtime *fakeRuntime) Create(context.Context, string, Request) error {
	return runtime.action("create")
}
func (runtime *fakeRuntime) Start(context.Context, string) error  { return runtime.action("start") }
func (runtime *fakeRuntime) Stop(context.Context, string) error   { return runtime.action("stop") }
func (runtime *fakeRuntime) Remove(context.Context, string) error { return runtime.action("remove") }
func (runtime *fakeRuntime) Export(context.Context, string, string) error {
	return runtime.action("export")
}

func outputPath() string  { return filepath.Join(os.TempDir(), "mesh-output", "job", "1") }
func jobRequest() Request { return Request{Workload: job(), OutputPath: outputPath()} }

func job() control.Workload {
	return control.Workload{WorkloadSpec: control.WorkloadSpec{ID: "12345678-1234-4234-8234-123456789abc",
		NodeID: "22345678-1234-4234-8234-123456789abc", Name: "job", Kind: "job",
		Image: "example/job@sha256:" + strings.Repeat("a", 64), Command: []string{"run"},
		Resources: control.WorkloadResources{CPUMillis: 500, MemoryBytes: 128 * 1024 * 1024}, DesiredState: "running"},
		ExecutionEnvironmentID: "32345678-1234-4234-8234-123456789abc", Revision: 1}
}

func TestReconcileCreatesRestrictedWorkloadOnce(t *testing.T) {
	runtime := &fakeRuntime{}
	result := Reconcile(context.Background(), runtime, jobRequest())
	if result.State != "running" || strings.Join(runtime.actions, ",") != "inspect,pull,create,start" {
		t.Fatalf("unexpected first reconciliation: %#v %v", result, runtime.actions)
	}
	runtime.actions = nil
	runtime.exists = true
	runtime.container = Container{WorkloadID: job().ID, Revision: 1, Image: job().Image, State: "running"}
	result = Reconcile(context.Background(), runtime, jobRequest())
	if result.State != "running" || strings.Join(runtime.actions, ",") != "inspect" {
		t.Fatalf("running workload was recreated: %#v %v", result, runtime.actions)
	}
}

func TestReconcileReportsCompletedJobWithoutRestart(t *testing.T) {
	workload := job()
	runtime := &fakeRuntime{exists: true, container: Container{WorkloadID: workload.ID, Revision: 1,
		Image: workload.Image, State: "exited", ExitCode: 0}}
	result := Reconcile(context.Background(), runtime, Request{Workload: workload, OutputPath: outputPath()})
	if result.State != "succeeded" || result.ExitCode == nil || *result.ExitCode != 0 ||
		strings.Join(runtime.actions, ",") != "inspect,export" {
		t.Fatalf("completed job restarted: %#v %v", result, runtime.actions)
	}
}

func TestReconcileRetriesOutputExportWithoutRestartingJob(t *testing.T) {
	workload := job()
	runtime := &fakeRuntime{exists: true, fail: "export", container: Container{WorkloadID: workload.ID, Revision: 1,
		Image: workload.Image, State: "exited", ExitCode: 0}}
	result := Reconcile(context.Background(), runtime, Request{Workload: workload, OutputPath: outputPath()})
	if result.State != "exporting" || strings.Join(runtime.actions, ",") != "inspect,export" {
		t.Fatalf("failed export did not remain retryable: %#v %v", result, runtime.actions)
	}
}

func TestReconcileNeverRemovesUnownedContainer(t *testing.T) {
	workload := job()
	runtime := &fakeRuntime{exists: true, container: Container{WorkloadID: "different", Revision: 1,
		Image: workload.Image, State: "running"}}
	result := Reconcile(context.Background(), runtime, Request{Workload: workload, OutputPath: outputPath()})
	if result.State != "failed" || result.FailureCode == nil || *result.FailureCode != "invalid-runtime" || len(runtime.actions) != 1 {
		t.Fatalf("unowned container was changed: %#v %v", result, runtime.actions)
	}
}
