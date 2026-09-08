package cli

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"mesh.local/agent/internal/relay"
	"mesh.local/agent/internal/storage"
)

func serveStorage(ctx context.Context, o storageOptions, authorize storage.Authorizer, logs io.Writer, report storage.PublicationReporter) (err error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var certificate tls.Certificate
	if o.cert != "" {
		certificate, err = tls.LoadX509KeyPair(o.cert, o.key)
		if err != nil {
			return err
		}
	}
	if o.remote != nil {
		certificate = o.remote.certificate
	}
	store, err := storage.Open(o.root)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, store.Close()) }()
	listener, err := net.Listen("tcp", o.listen)
	if err != nil {
		return err
	}
	address := listener.Addr().String()
	server := &http.Server{Handler: storage.AuthorizedHandler(store, authorize, report), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 2 * time.Minute, WriteTimeout: 30 * time.Minute, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 8192}
	if o.cert != "" || o.remote != nil {
		listener = tls.NewListener(listener, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}})
	}
	if o.remote != nil {
		relayDone := make(chan struct{})
		go func() {
			defer close(relayDone)
			_ = relay.ServeDevice(ctx, relay.DeviceConfig{RelayOrigin: o.remote.origin, NodeID: o.remote.nodeID, Workers: 4, TLSConfig: o.remote.trust,
				Credential: o.remote.ticket,
				DialTarget: func(ctx context.Context) (net.Conn, error) {
					return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", address)
				},
				OnError: func(error) { fmt.Fprintln(logs, "Relay unavailable; reconnecting automatically.") },
			})
		}()
		defer func() { cancel(); <-relayDone }()
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if server.Shutdown(shutdown) != nil {
				_ = server.Close()
			}
		case <-done:
		}
	}()
	_, _ = fmt.Fprintf(logs, "Storage listening on %s\n", listener.Addr())
	err = server.Serve(listener)
	close(done)
	<-stopped
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
