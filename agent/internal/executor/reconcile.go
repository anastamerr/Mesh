// Package executor reconciles typed workloads against a restricted container runtime.
package executor

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"mesh.local/agent/internal/control"
)

type Request struct {
	Workload   control.Workload `json:"workload"`
	InputPath  string           `json:"inputPath"`
	OutputPath string           `json:"outputPath"`
}

type Container struct {
	WorkloadID  string
	Revision    uint64
	Kind        string
	ServicePort int
	Addresses   []string
	Image       string
	State       string
	ExitCode    int
}

type Runtime interface {
	Inspect(context.Context, string) (Container, bool, error)
	Pull(context.Context, string) error
	Create(context.Context, string, Request) error
	Start(context.Context, string) error
	Stop(context.Context, string) error
	Remove(context.Context, string) error
	Export(context.Context, string, string) error
}

var errInvalidRequest = errors.New("invalid executor request")

func containerName(id string) string { return "mesh-" + id }

func failed(revision uint64, code string) control.WorkloadObservation {
	exitCode := 1
	return control.WorkloadObservation{Revision: revision, State: "failed", ExitCode: &exitCode, FailureCode: &code}
}

func resultFor(workload control.Workload, container Container) control.WorkloadObservation {
	if container.State == "running" {
		return control.WorkloadObservation{Revision: workload.Revision, State: "running"}
	}
	if workload.Kind == "job" && container.State == "exited" {
		exitCode := container.ExitCode
		if exitCode == 0 {
			return control.WorkloadObservation{Revision: workload.Revision, State: "succeeded", ExitCode: &exitCode}
		}
		code := "runtime-failure"
		return control.WorkloadObservation{Revision: workload.Revision, State: "failed", ExitCode: &exitCode, FailureCode: &code}
	}
	return control.WorkloadObservation{Revision: workload.Revision, State: "starting"}
}

func validate(request Request) error {
	workload := request.Workload
	if !control.ValidWorkload(workload, workload.NodeID) {
		return errInvalidRequest
	}
	if workload.InputCollectionID == nil {
		if request.InputPath != "" {
			return errInvalidRequest
		}
	} else if request.InputPath == "" || !filepath.IsAbs(request.InputPath) || strings.Contains(request.InputPath, ",") {
		return errInvalidRequest
	}
	if workload.Kind == "job" {
		if request.OutputPath == "" || !filepath.IsAbs(request.OutputPath) || strings.Contains(request.OutputPath, ",") {
			return errInvalidRequest
		}
	} else if request.OutputPath != "" {
		return errInvalidRequest
	}
	return nil
}

// Reconcile is idempotent for one workload revision. It never removes a
// container unless its Mesh ownership labels match the requested workload.
func Reconcile(ctx context.Context, runtime Runtime, request Request) control.WorkloadObservation {
	workload := request.Workload
	if err := validate(request); err != nil {
		return failed(workload.Revision, "invalid-runtime")
	}
	name := containerName(workload.ID)
	container, exists, err := runtime.Inspect(ctx, name)
	if err != nil {
		return failed(workload.Revision, "runtime-failure")
	}
	if exists && (container.WorkloadID != workload.ID || container.Revision < 1 || container.Revision > workload.Revision) {
		return failed(workload.Revision, "invalid-runtime")
	}
	if workload.DesiredState == "stopped" {
		if exists && container.State == "running" {
			if err := runtime.Stop(ctx, name); err != nil {
				return failed(workload.Revision, "runtime-failure")
			}
		}
		return control.WorkloadObservation{Revision: workload.Revision, State: "stopped"}
	}
	if exists && container.Revision == workload.Revision && container.Image == workload.Image {
		result := resultFor(workload, container)
		if result.State == "succeeded" {
			if err := runtime.Export(ctx, name, request.OutputPath); err != nil {
				return control.WorkloadObservation{Revision: workload.Revision, State: "exporting"}
			}
		}
		if result.State == "running" || result.State == "succeeded" || result.State == "failed" {
			return result
		}
		if err := runtime.Start(ctx, name); err != nil {
			return failed(workload.Revision, "runtime-failure")
		}
		return control.WorkloadObservation{Revision: workload.Revision, State: "running"}
	}
	if exists {
		if container.WorkloadID != workload.ID {
			return failed(workload.Revision, "invalid-runtime")
		}
		if container.State == "running" {
			if err := runtime.Stop(ctx, name); err != nil {
				return failed(workload.Revision, "runtime-failure")
			}
		}
		if err := runtime.Remove(ctx, name); err != nil {
			return failed(workload.Revision, "runtime-failure")
		}
	}
	if err := runtime.Pull(ctx, workload.Image); err != nil {
		return failed(workload.Revision, "image-unavailable")
	}
	if err := runtime.Create(ctx, name, request); err != nil {
		return failed(workload.Revision, "resource-unavailable")
	}
	if err := runtime.Start(ctx, name); err != nil {
		return failed(workload.Revision, "runtime-failure")
	}
	return control.WorkloadObservation{Revision: workload.Revision, State: "running"}
}
