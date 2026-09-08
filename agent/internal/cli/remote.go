package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"os"
	"sync"
	"time"

	"mesh.local/agent/internal/control"
	"mesh.local/agent/internal/relay"
	"mesh.local/agent/internal/storage"
)

func relayTrust(file string) (*tls.Config, error) {
	if file == "" {
		return nil, nil
	}
	info, err := os.Stat(file)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, errors.New("relay CA must be a small PEM file")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, errors.New("relay CA contains no certificates")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool}, nil
}

func configureRemoteClient(ctx context.Context, client *storage.Client, controller *control.Client, operator, node, origin, ca string, grant func(context.Context) (string, error)) error {
	fingerprint, err := controller.DeviceFingerprint(ctx, operator, node)
	if err != nil {
		return err
	}
	trust, err := relayTrust(ca)
	if err != nil {
		return err
	}
	return client.UseDeviceTransport(fingerprint, func(ctx context.Context, _, _ string) (net.Conn, error) {
		// Obtain a fresh lease for each connection. HTTP keep-alive amortizes this
		// operation without sharing mutable grant state with transport goroutines.
		token, err := grant(ctx)
		if err != nil {
			return nil, err
		}
		ticket, _, err := controller.RelayTicket(ctx, node, "consumer", token)
		if err != nil {
			return nil, err
		}
		return relay.Dial(ctx, origin, node, relay.RoleConsumer, ticket, trust)
	})
}

func deviceRelayTickets(controller *control.Client, node, credential string) func(context.Context) (string, error) {
	var mu sync.Mutex
	var token string
	var expiry time.Time
	return func(ctx context.Context) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if time.Until(expiry) > time.Minute {
			return token, nil
		}
		var err error
		token, expiry, err = controller.RelayTicket(ctx, node, "device", credential)
		return token, err
	}
}
