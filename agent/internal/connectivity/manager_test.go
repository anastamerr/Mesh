package connectivity

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func successfulPipe(ctx context.Context) (net.Conn, error) {
	left, right := net.Pipe()
	go func() { <-ctx.Done(); _ = right.Close() }()
	return left, nil
}

func TestManagerPrefersAuthenticatedDirectRoute(t *testing.T) {
	var relayCalls atomic.Int32
	var selected Path
	manager := Manager{DirectHeadStart: 50 * time.Millisecond,
		Direct:     []Route{{Dial: successfulPipe}},
		Relay:      &Route{Dial: func(context.Context) (net.Conn, error) { relayCalls.Add(1); return nil, errors.New("unexpected") }},
		OnSelected: func(path Path) { selected = path },
	}
	conn, err := manager.Dial(context.Background(), "tcp", "unused")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	time.Sleep(75 * time.Millisecond)
	if selected != PathDirect || relayCalls.Load() != 0 {
		t.Fatalf("selected=%s relay calls=%d", selected, relayCalls.Load())
	}
}

func TestManagerFallsBackWithoutSerialTimeouts(t *testing.T) {
	var selected Path
	manager := Manager{DirectHeadStart: 5 * time.Millisecond,
		Direct: []Route{{Dial: func(ctx context.Context) (net.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }}},
		Relay:  &Route{Dial: successfulPipe}, OnSelected: func(path Path) { selected = path },
	}
	started := time.Now()
	conn, err := manager.Dial(context.Background(), "tcp", "unused")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if selected != PathRelay || time.Since(started) > time.Second {
		t.Fatalf("selected=%s duration=%s", selected, time.Since(started))
	}
}

func TestManagerRequiresARouteAndHonorsCancellation(t *testing.T) {
	if _, err := new(Manager).Dial(context.Background(), "tcp", "unused"); !errors.Is(err, ErrNoRoute) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	manager := Manager{Direct: []Route{{Dial: func(ctx context.Context) (net.Conn, error) { return nil, ctx.Err() }}}}
	if _, err := manager.Dial(ctx, "tcp", "unused"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestPathReporterEmitsOnlyChanges(t *testing.T) {
	var calls atomic.Int32
	reporter := PathReporter{Log: func(Path) { calls.Add(1) }}
	reporter.Selected(PathDirect)
	reporter.Selected(PathDirect)
	reporter.Selected(PathRelay)
	if calls.Load() != 2 {
		t.Fatal(calls.Load())
	}
}
