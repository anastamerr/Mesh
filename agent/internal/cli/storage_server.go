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

	"mesh.local/agent/internal/storage"
)

func serveStorage(ctx context.Context, o storageOptions, authorize storage.Authorizer, logs io.Writer, report storage.PublicationReporter) (err error) {
	var certificate tls.Certificate
	if o.cert != "" {
		certificate, err = tls.LoadX509KeyPair(o.cert, o.key)
		if err != nil {
			return err
		}
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
	server := &http.Server{Handler: storage.AuthorizedHandler(store, authorize, report), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 2 * time.Minute, WriteTimeout: 30 * time.Minute, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 8192}
	if o.cert != "" {
		listener = tls.NewListener(listener, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}})
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
