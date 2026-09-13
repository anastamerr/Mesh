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
	var listen, certFile, keyFile, authURL, authTokenFile, metricsListen string
	var maxWaiting, maxActive, maxConnectsPerMinute, maxConnectsPerNodePerMinute int
	var insecureHTTP, allowPrivateAuthHTTP bool
	flag.StringVar(&listen, "listen", "127.0.0.1:7443", "relay listen address")
	flag.StringVar(&certFile, "tls-cert", "", "TLS certificate PEM file")
	flag.StringVar(&keyFile, "tls-key", "", "TLS private key PEM file")
	flag.BoolVar(&insecureHTTP, "insecure-http", false, "accept plaintext HTTP/1.1 from a trusted TLS-terminating proxy network")
	flag.StringVar(&authURL, "auth-url", "", "controller relay authorization endpoint")
	flag.BoolVar(&allowPrivateAuthHTTP, "allow-private-auth-http", false, "allow an HTTP authorization URL on a trusted private container network")
	flag.StringVar(&authTokenFile, "auth-service-token-file", "", "private file containing the relay service credential")
	flag.StringVar(&metricsListen, "metrics-listen", "", "private Prometheus and health listen address (disabled when empty)")
	flag.IntVar(&maxWaiting, "max-waiting", 1024, "maximum authenticated device waiters")
	flag.IntVar(&maxActive, "max-active", 512, "maximum active relayed streams")
	flag.IntVar(&maxConnectsPerMinute, "max-connects-per-minute", 6000, "global sustained CONNECT request rate")
	flag.IntVar(&maxConnectsPerNodePerMinute, "max-connects-per-node-per-minute", 600, "per-node sustained CONNECT request rate")
	flag.Parse()
	if authURL == "" || authTokenFile == "" {
		return errors.New("--auth-url and --auth-service-token-file are required")
	}
	if insecureHTTP {
		if certFile != "" || keyFile != "" {
			return errors.New("--insecure-http cannot be combined with --tls-cert or --tls-key")
		}
	} else if certFile == "" || keyFile == "" {
		return errors.New("--tls-cert and --tls-key are required unless --insecure-http is explicitly enabled")
	}
	if flag.NArg() != 0 || maxWaiting < 1 || maxActive < 1 || maxConnectsPerMinute < 1 || maxConnectsPerNodePerMinute < 1 {
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
	authorizer, err := relay.NewHTTPAuthorizerWithOptions(authURL, strings.TrimSpace(string(credential)), relay.HTTPAuthorizerOptions{AllowPrivateHTTP: allowPrivateAuthHTTP})
	if err != nil {
		return err
	}
	handler, err := relay.NewServer(relay.ServerConfig{
		Authorizer: authorizer, MaxWaiting: maxWaiting, MaxActive: maxActive,
		MaxConnectsPerMinute: maxConnectsPerMinute, MaxConnectsPerNodePerMinute: maxConnectsPerNodePerMinute,
	})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	defer listener.Close()
	serveListener := listener
	if !insecureHTTP {
		certificate, loadErr := tls.LoadX509KeyPair(certFile, keyFile)
		if loadErr != nil {
			return errors.New("cannot load relay TLS certificate and key")
		}
		serveListener = tls.NewListener(listener, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}, NextProtos: []string{"http/1.1"}})
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 * 1024, TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler))}
	var metricsServer *http.Server
	var metricsListener net.Listener
	metricsDone := make(chan error, 1)
	if metricsListen != "" {
		metricsListener, err = net.Listen("tcp", metricsListen)
		if err != nil {
			return fmt.Errorf("listen for relay metrics: %w", err)
		}
		defer metricsListener.Close()
		mux := http.NewServeMux()
		mux.Handle("/metrics", handler.MetricsHandler())
		mux.HandleFunc("/health/live", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		mux.HandleFunc("/health/ready", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		metricsServer = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 * 1024}
		go func() { metricsDone <- metricsServer.Serve(metricsListener) }()
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.Serve(serveListener) }()
	fmt.Fprintf(os.Stderr, "Relay listening on %s\n", listener.Addr())
	fmt.Fprintf(os.Stderr, "Relay proxy TLS termination enabled: %t\n", insecureHTTP)
	if metricsListener != nil {
		fmt.Fprintf(os.Stderr, "Relay metrics listening on %s\n", metricsListener.Addr())
	}
	relayReturned := false
	metricsReturned := false
	select {
	case err = <-done:
		relayReturned = true
	case err = <-metricsDone:
		metricsReturned = true
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = handler.Close()
	shutdownErr := server.Shutdown(shutdown)
	if metricsServer != nil {
		_ = metricsServer.Shutdown(shutdown)
	}
	if !relayReturned {
		relayErr := <-done
		if err == nil && !errors.Is(relayErr, http.ErrServerClosed) {
			err = relayErr
		}
	}
	if metricsServer != nil && !metricsReturned {
		metricsErr := <-metricsDone
		if err == nil && !errors.Is(metricsErr, http.ErrServerClosed) {
			err = metricsErr
		}
	}
	if err == nil {
		err = shutdownErr
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
