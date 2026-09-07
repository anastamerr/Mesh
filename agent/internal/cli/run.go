package cli

import (
	"context"
	"errors"
	"io"
	"sync"

	"mesh.local/agent/internal/control"
	"mesh.local/agent/internal/inventory"
	"mesh.local/agent/internal/runner"
	"mesh.local/agent/internal/state"
)

// One foreground process owns the identity and both lifecycles. A permanent
// heartbeat failure or a failed storage listener stops the whole node.
func runNode(ctx context.Context, saved *state.State, store *state.Store, client *control.Client, o options, logs io.Writer) error {
	if o.storage.root == "" {
		return runner.Loop(ctx, saved, store, client, inventory.Read, o.interval, logs)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	output := &nodeLog{writer: logs}
	authorize, report := enrolledStorage(client, saved.NodeID, saved.Credential)
	stopped := make(chan error, 2)
	go func() { stopped <- serveStorage(ctx, o.storage, authorize, output, report) }()
	go func() { stopped <- runner.Loop(ctx, saved, store, client, inventory.Read, o.interval, output) }()
	first := <-stopped
	cancel()
	return errors.Join(first, <-stopped)
}

type nodeLog struct {
	mu     sync.Mutex
	writer io.Writer
}

func (w *nodeLog) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(p)
}
