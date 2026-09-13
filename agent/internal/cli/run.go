package cli

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"sync"

	"mesh.local/agent/internal/compute"
	"mesh.local/agent/internal/control"
	"mesh.local/agent/internal/inventory"
	"mesh.local/agent/internal/runner"
	"mesh.local/agent/internal/state"
	"mesh.local/agent/internal/storage"
)

// One foreground process owns the identity and both lifecycles. A permanent
// heartbeat failure or a failed storage listener stops the whole node.
func runNode(ctx context.Context, saved *state.State, store *state.Store, client *control.Client, o options, logs io.Writer) (err error) {
	var sender runner.Sender = client
	if saved.PublicKeyFingerprint != "" {
		sender = &pairedHeartbeat{client: client, saved: saved, store: store}
	}
	if o.storage.root == "" && !o.compute {
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
	var deviceCertificate tls.Certificate
	var relayTLS *tls.Config
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
		deviceCertificate, relayTLS = certificate, trust
		o.storage.remote = &remoteStorage{origin: o.relay, nodeID: saved.NodeID, ticket: deviceRelayTickets(client, saved.NodeID, saved.Credential), certificate: certificate, trust: trust}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	output := &nodeLog{writer: logs}
	authorize, report := enrolledStorage(client, saved.NodeID, saved.Credential)
	storageStore, err := storage.Open(o.storage.root)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, storageStore.Close()) }()
	workers := 2
	stopped := make(chan error, 3)
	go func() { stopped <- serveStorageWithStore(ctx, o.storage, storageStore, authorize, output, report) }()
	go func() { stopped <- runner.Loop(ctx, saved, store, sender, inventory.Read, o.interval, output) }()
	if o.compute {
		workers++
		launcher := compute.NewProcessLauncher(o.wsl)
		publisher := &jobOutputPublisher{store: storageStore, controller: client,
			nodeID: saved.NodeID, credential: saved.Credential}
		go func() {
			stopped <- compute.Loop(ctx, client, launcher, publisher, saved.NodeID, saved.Credential,
				o.storage.root, o.interval, output)
		}()
		if o.relay != "" {
			workers++
			go func() {
				stopped <- compute.ApplicationRouteLoop(ctx, client, launcher, saved.NodeID, saved.Credential,
					o.relay, relayTLS, deviceCertificate, o.interval, output)
			}()
		}
	}
	first := <-stopped
	cancel()
	results := []error{first}
	for index := 1; index < workers; index++ {
		results = append(results, <-stopped)
	}
	return errors.Join(results...)
}

type jobOutputPublisher struct {
	store      *storage.Store
	controller *control.Client
	nodeID     string
	credential string
}

func (publisher *jobOutputPublisher) Publish(ctx context.Context, path string) (string, error) {
	id, manifest, err := publisher.store.Import(ctx, path)
	if err != nil {
		return "", err
	}
	count, total := manifest.Statistics()
	if err := publisher.controller.ConfirmCollection(ctx, publisher.nodeID, publisher.credential, id, count, total); err != nil {
		return "", err
	}
	return id, nil
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
