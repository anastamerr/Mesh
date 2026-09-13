// Package compute reconciles controller-owned workload assignments through the
// separately restricted Linux executor.
package compute

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"mesh.local/agent/internal/control"
	"mesh.local/agent/internal/executor"
)

type Controller interface {
	ReportExecutionEnvironment(context.Context, string, string, control.ExecutionEnvironmentReport) (control.ExecutionEnvironment, error)
	Workloads(context.Context, string, string) ([]control.Workload, error)
	ObserveWorkload(context.Context, string, string, string, control.WorkloadObservation) error
}

type Launcher interface {
	Ready(context.Context) (string, string, error)
	Reconcile(context.Context, executor.Request) (control.WorkloadObservation, error)
}

type OutputPublisher interface {
	Publish(context.Context, string) (string, error)
}

func failure(revision uint64) control.WorkloadObservation {
	exitCode := 1
	code := "invalid-runtime"
	return control.WorkloadObservation{Revision: revision, State: "failed", ExitCode: &exitCode, FailureCode: &code}
}

func Once(ctx context.Context, controller Controller, launcher Launcher, publisher OutputPublisher,
	nodeID, credential, storageRoot string) error {
	if storageRoot == "" {
		return errors.New("compute requires an approved storage root")
	}
	version, architecture, readinessError := launcher.Ready(ctx)
	report := control.ExecutionEnvironmentReport{Kind: "docker-linux", Architecture: runtime.GOARCH, Status: "unavailable"}
	if readinessError == nil {
		report.Status = "ready"
		report.Architecture = architecture
		report.RuntimeVersion = &version
	}
	if _, err := controller.ReportExecutionEnvironment(ctx, nodeID, credential, report); err != nil {
		return err
	}
	if readinessError != nil {
		return nil
	}
	workloads, err := controller.Workloads(ctx, nodeID, credential)
	if err != nil {
		return err
	}
	for _, workload := range workloads {
		request := executor.Request{Workload: workload}
		if workload.Kind == "job" {
			request.OutputPath, err = filepath.Abs(filepath.Join(storageRoot, ".mesh", "job-output", workload.ID,
				strconv.FormatUint(workload.Revision, 10)))
			if err != nil {
				return errors.New("cannot resolve job output path")
			}
		}
		if workload.InputCollectionID != nil {
			request.InputPath, err = filepath.Abs(filepath.Join(storageRoot, "collections", *workload.InputCollectionID))
			if err != nil {
				return errors.New("cannot resolve collection path")
			}
		}
		observation, launchErr := launcher.Reconcile(ctx, request)
		if launchErr != nil {
			observation = failure(workload.Revision)
		}
		if observation.State == "succeeded" {
			if publisher == nil {
				observation = control.WorkloadObservation{Revision: workload.Revision, State: "exporting"}
			} else if outputID, publishError := publisher.Publish(ctx, request.OutputPath); publishError != nil {
				observation = control.WorkloadObservation{Revision: workload.Revision, State: "exporting"}
			} else {
				observation.OutputCollectionID = &outputID
			}
		}
		if err := controller.ObserveWorkload(ctx, nodeID, credential, workload.ID, observation); err != nil {
			return err
		}
	}
	return nil
}

func Loop(ctx context.Context, controller Controller, launcher Launcher, publisher OutputPublisher,
	nodeID, credential, storageRoot string,
	interval time.Duration, logs io.Writer) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		err := Once(ctx, controller, launcher, publisher, nodeID, credential, storageRoot)
		if ctx.Err() != nil {
			return nil
		}
		if control.Permanent(err) {
			return fmt.Errorf("compute reconciliation stopped: %w", err)
		}
		if err != nil {
			fmt.Fprintln(logs, "Compute control unavailable; retrying.")
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
