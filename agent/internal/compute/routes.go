package compute

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"mesh.local/agent/internal/control"
	"mesh.local/agent/internal/relay"
	"mesh.local/agent/internal/stream"
)

type ApplicationRouteController interface {
	Workloads(context.Context, string, string) ([]control.Workload, error)
	ApplicationDeviceTicket(context.Context, string, string, string) (control.ApplicationRelayTicket, error)
}

type ApplicationRouteLauncher interface {
	Proxy(context.Context, control.Workload) (io.ReadWriteCloser, error)
}

type applicationRoute struct {
	revision uint64
	cancel   context.CancelFunc
	done     chan struct{}
}

type applicationRoutes struct {
	controller  ApplicationRouteController
	launcher    ApplicationRouteLauncher
	nodeID      string
	credential  string
	relayOrigin string
	relayTrust  *tls.Config
	certificate tls.Certificate
	routes      map[string]applicationRoute
}

func ApplicationRouteLoop(ctx context.Context, controller ApplicationRouteController, launcher ApplicationRouteLauncher,
	nodeID, credential, relayOrigin string, relayTrust *tls.Config, certificate tls.Certificate,
	interval time.Duration, logs io.Writer) error {
	routes := &applicationRoutes{controller: controller, launcher: launcher, nodeID: nodeID, credential: credential,
		relayOrigin: relayOrigin, relayTrust: relayTrust, certificate: certificate, routes: make(map[string]applicationRoute)}
	defer routes.close()
	for {
		workloads, err := controller.Workloads(ctx, nodeID, credential)
		if ctx.Err() != nil {
			return nil
		}
		if control.Permanent(err) {
			return fmt.Errorf("application routing stopped: %w", err)
		}
		if err != nil {
			fmt.Fprintln(logs, "Application routing control unavailable; retrying.")
		} else {
			routes.sync(ctx, workloads)
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

func (routes *applicationRoutes) sync(parent context.Context, workloads []control.Workload) {
	active := make(map[string]control.Workload)
	for _, workload := range workloads {
		if workload.Kind == "application" && workload.DesiredState == "running" {
			active[workload.ID] = workload
		}
	}
	for id, running := range routes.routes {
		workload, keep := active[id]
		if keep && workload.Revision == running.revision {
			delete(active, id)
			continue
		}
		running.cancel()
		<-running.done
		delete(routes.routes, id)
	}
	for id, workload := range active {
		workerContext, cancel := context.WithCancel(parent)
		done := make(chan struct{})
		routes.routes[id] = applicationRoute{revision: workload.Revision, cancel: cancel, done: done}
		go func() {
			defer close(done)
			_ = routes.serve(workerContext, workload)
		}()
	}
}

func (routes *applicationRoutes) serve(ctx context.Context, workload control.Workload) error {
	var mutex sync.Mutex
	var token string
	var expiry time.Time
	credential := func(ctx context.Context) (string, error) {
		mutex.Lock()
		defer mutex.Unlock()
		if time.Until(expiry) > time.Minute {
			return token, nil
		}
		ticket, err := routes.controller.ApplicationDeviceTicket(ctx, routes.nodeID, routes.credential, workload.ID)
		if err != nil {
			return "", err
		}
		token, expiry = ticket.Token, ticket.ExpiresAt
		return token, nil
	}
	return relay.ServeDevice(ctx, relay.DeviceConfig{RelayOrigin: routes.relayOrigin, NodeID: routes.nodeID,
		Route: "app-" + workload.ID, TLSConfig: routes.relayTrust, Workers: 2, Credential: credential,
		DialTarget: func(ctx context.Context) (net.Conn, error) {
			proxy, err := routes.launcher.Proxy(ctx, workload)
			if err != nil {
				return nil, err
			}
			plain, encrypted := net.Pipe()
			go serveEncryptedApplication(ctx, plain, proxy, routes.certificate)
			return encrypted, nil
		}})
}

func serveEncryptedApplication(ctx context.Context, plain net.Conn, proxy io.ReadWriteCloser, certificate tls.Certificate) {
	secure := tls.Server(plain, &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}})
	if err := secure.HandshakeContext(ctx); err != nil {
		_ = secure.Close()
		_ = proxy.Close()
		return
	}
	stream.Bridge(ctx, secure, proxy)
}

func (routes *applicationRoutes) close() {
	for _, route := range routes.routes {
		route.cancel()
	}
	for _, route := range routes.routes {
		<-route.done
	}
	clear(routes.routes)
}
