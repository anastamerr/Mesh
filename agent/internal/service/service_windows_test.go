//go:build windows

package service

import (
	"context"
	"io"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
)

func TestHandlerCancelsRunnerOnServiceStop(t *testing.T) {
	requests := make(chan svc.ChangeRequest)
	statuses := make(chan svc.Status, 4)
	result := make(chan uint32, 1)
	handler := &handler{logs: io.Discard, runner: func(ctx context.Context, _ io.Writer) error {
		<-ctx.Done()
		return nil
	}}
	go func() {
		_, code := handler.Execute(nil, requests, statuses)
		result <- code
	}()
	if status := <-statuses; status.State != svc.StartPending {
		t.Fatalf("first state = %v", status.State)
	}
	if status := <-statuses; status.State != svc.Running || status.Accepts&svc.AcceptStop == 0 {
		t.Fatalf("running state = %#v", status)
	}
	requests <- svc.ChangeRequest{Cmd: svc.Stop}
	if status := <-statuses; status.State != svc.StopPending {
		t.Fatalf("stop state = %v", status.State)
	}
	select {
	case code := <-result:
		if code != 0 {
			t.Fatalf("service exit code = %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("service runner did not stop after cancellation")
	}
}
