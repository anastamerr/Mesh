package connectivity

import (
	"context"
	"errors"
	"io"
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

func TestManagerClosesEveryConnectionWhenDialsRaceCancellation(t *testing.T) {
	for range 50 {
		ctx, cancel := context.WithCancel(context.Background())
		ready := make(chan struct{}, 4)
		release := make(chan struct{})
		var peers []net.Conn
		manager := Manager{}
		for range 4 {
			connection, peer := net.Pipe()
			peers = append(peers, peer)
			manager.Direct = append(manager.Direct, Route{Dial: func(context.Context) (net.Conn, error) {
				ready <- struct{}{}
				<-release
				return connection, nil
			}})
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			connection, _ := manager.Dial(ctx, "tcp", "unused")
			if connection != nil {
				_ = connection.Close()
			}
		}()
		for range 4 {
			<-ready
		}
		close(release)
		cancel()
		<-done
		for _, peer := range peers {
			_ = peer.SetReadDeadline(time.Now().Add(time.Second))
			_, err := peer.Read(make([]byte, 1))
			_ = peer.Close()
			if !errors.Is(err, io.EOF) {
				t.Fatalf("cancelled route leaked a connection: %v", err)
			}
		}
	}
}
