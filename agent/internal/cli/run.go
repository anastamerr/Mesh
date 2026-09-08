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
	var sender runner.Sender = client
	if saved.PublicKeyFingerprint != "" {
		sender = &pairedHeartbeat{client: client, saved: saved, store: store}
	}
	if o.storage.root == "" {
		return runner.Loop(ctx, saved, store, sender, inventory.Read, o.interval, logs)
	}
	if o.relay == "" && saved.PublicKeyFingerprint != "" {
		var err error
		o.relay, err = client.RelayOrigin(ctx)
		if err != nil {
			if saved.RelayOrigin == "" {
				return err
			}
			o.relay = saved.RelayOrigin
		} else if saved.RelayOrigin != o.relay {
			saved.RelayOrigin = o.relay
			if err := store.Save(*saved); err != nil {
				return err
			}
		}
	}
	if o.relay != "" {
		if _, err := control.New(o.relay); err != nil {
			return err
		}
		certificate, fingerprint, err := store.DeviceCertificate()
		if err != nil {
			return err
		}
		if saved.PublicKeyFingerprint == "" || saved.PublicKeyFingerprint != fingerprint {
			return errors.New("relay access requires a matching paired device identity")
		}
		if o.storage.cert != "" {
			return errors.New("relay serving uses the paired device certificate; omit manual TLS files")
		}
		trust, err := relayTrust(o.relayCA)
		if err != nil {
			return err
		}
		o.storage.remote = &remoteStorage{origin: o.relay, nodeID: saved.NodeID, ticket: deviceRelayTickets(client, saved.NodeID, saved.Credential), certificate: certificate, trust: trust}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	output := &nodeLog{writer: logs}
	authorize, report := enrolledStorage(client, saved.NodeID, saved.Credential)
	stopped := make(chan error, 2)
	go func() { stopped <- serveStorage(ctx, o.storage, authorize, output, report) }()
	go func() { stopped <- runner.Loop(ctx, saved, store, sender, inventory.Read, o.interval, output) }()
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
