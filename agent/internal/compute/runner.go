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
	"sync"
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

func Once(ctx context.Context, controller Controller, launcher Launcher, publisher OutputPublisher,
	nodeID, credential, storageRoot string) error {
	if storageRoot == "" {
		return errors.New("compute requires an approved storage root")
	}
	readyContext, cancelReady := context.WithTimeout(ctx, 30*time.Second)
	version, architecture, readinessError := launcher.Ready(readyContext)
	cancelReady()
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
	// Two workers let an image pull coexist with another application's lifecycle
	// while bounding concurrent executor processes on older machines.
	failures := make([]error, len(workloads))
	indices := make(chan int)
	var workers sync.WaitGroup
	for range min(2, len(workloads)) {
		workers.Go(func() {
			for index := range indices {
				failures[index] = reconcileWorkload(ctx, controller, launcher, publisher,
					nodeID, credential, storageRoot, workloads[index])
			}
		})
	}
	for index := range workloads {
		select {
		case indices <- index:
		case <-ctx.Done():
			close(indices)
			workers.Wait()
			return ctx.Err()
		}
	}
	close(indices)
	workers.Wait()
	return errors.Join(failures...)
}

func reconcileWorkload(ctx context.Context, controller Controller, launcher Launcher, publisher OutputPublisher,
	nodeID, credential, storageRoot string, workload control.Workload) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var err error
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
	launchContext, cancelLaunch := context.WithTimeout(ctx, 15*time.Minute)
	observation, launchErr := launcher.Reconcile(launchContext, request)
	cancelLaunch()
	if launchErr != nil {
		// A failed process invocation gives no evidence that the job failed.
		// Leave it assigned for recovery and still reconcile the other workloads.
		return errors.New("executor unavailable; workload will be retried")
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
	return controller.ObserveWorkload(ctx, nodeID, credential, workload.ID, observation)
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
