package cli

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"mesh.local/agent/internal/connectivity"
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

func configureMeshClient(ctx context.Context, client *storage.Client, controller *control.Client, operator, node, origin, ca string,
	relayOnly bool, grant func(context.Context) (string, error), logs io.Writer) error {
	connection, err := controller.DeviceConnection(ctx, operator, node)
	if err != nil {
		return err
	}
	authenticate, err := storage.DeviceAuthenticator(connection.PublicKeyFingerprint)
	if err != nil {
		return err
	}
	trust, err := relayTrust(ca)
	if err != nil {
		return err
	}
	authenticated := func(dial func(context.Context) (net.Conn, error)) func(context.Context) (net.Conn, error) {
		return func(ctx context.Context) (net.Conn, error) {
			raw, err := dial(ctx)
			if err != nil {
				return nil, err
			}
			return authenticate(ctx, raw)
		}
	}
	direct := make([]connectivity.Route, 0, len(connection.DirectCandidates))
	if !relayOnly {
		for _, candidate := range connection.DirectCandidates {
			address := net.JoinHostPort(candidate.Host, fmt.Sprint(candidate.Port))
			direct = append(direct, connectivity.Route{Dial: authenticated(func(ctx context.Context) (net.Conn, error) {
				return (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, "tcp", address)
			})})
		}
	}
	var relayRoute *connectivity.Route
	if origin != "" {
		route := connectivity.Route{Dial: authenticated(func(ctx context.Context) (net.Conn, error) {
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
		})}
		relayRoute = &route
	}
	if len(direct) == 0 && relayRoute == nil {
		return connectivity.ErrNoRoute
	}
	reporter := &connectivity.PathReporter{Log: func(path connectivity.Path) {
		if path == connectivity.PathDirect {
			fmt.Fprintln(logs, "Connected directly to Mesh device on the local network.")
		} else {
			fmt.Fprintln(logs, "Connected through Mesh relay with device-to-device encryption.")
		}
	}}
	manager := &connectivity.Manager{Direct: direct, Relay: relayRoute, DirectHeadStart: 250 * time.Millisecond, OnSelected: reporter.Selected}
	return client.UseAuthenticatedDeviceTransport(manager.Dial)
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
