package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"mesh.local/agent/internal/relay"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	var listen, certFile, keyFile, authURL, authTokenFile string
	var maxWaiting, maxActive int
	flag.StringVar(&listen, "listen", "127.0.0.1:7443", "relay listen address")
	flag.StringVar(&certFile, "tls-cert", "", "TLS certificate PEM file")
	flag.StringVar(&keyFile, "tls-key", "", "TLS private key PEM file")
	flag.StringVar(&authURL, "auth-url", "", "controller relay authorization endpoint")
	flag.StringVar(&authTokenFile, "auth-service-token-file", "", "private file containing the relay service credential")
	flag.IntVar(&maxWaiting, "max-waiting", 1024, "maximum authenticated device waiters")
	flag.IntVar(&maxActive, "max-active", 512, "maximum active relayed streams")
	flag.Parse()
	if certFile == "" || keyFile == "" || authURL == "" || authTokenFile == "" {
		return errors.New("--tls-cert, --tls-key, --auth-url, and --auth-service-token-file are required")
	}
	if flag.NArg() != 0 || maxWaiting < 1 || maxActive < 1 {
		return errors.New("invalid relay arguments or capacity")
	}
	info, err := os.Lstat(authTokenFile)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 257 {
		return errors.New("relay authorization credential must be a small regular file")
	}
	credential, err := os.ReadFile(authTokenFile)
	if err != nil {
		return errors.New("cannot read relay authorization credential")
	}
	authorizer, err := relay.NewHTTPAuthorizer(authURL, strings.TrimSpace(string(credential)), nil)
	if err != nil {
		return err
	}
	handler, err := relay.NewServer(relay.ServerConfig{Authorizer: authorizer, MaxWaiting: maxWaiting, MaxActive: maxActive})
	if err != nil {
		return err
	}
	certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return errors.New("cannot load relay TLS certificate and key")
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	tlsListener := tls.NewListener(listener, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}, NextProtos: []string{"http/1.1"}})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 * 1024, TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler))}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.Serve(tlsListener) }()
	fmt.Fprintf(os.Stderr, "Relay listening on %s\n", listener.Addr())
	select {
	case err = <-done:
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = handler.Close()
		err = server.Shutdown(shutdown)
		cancel()
		if err == nil {
			err = <-done
		}
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
