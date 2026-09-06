package cli

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"mesh.local/agent/internal/storage"
)

type storageOptions struct{ command, keyFile, root, listen, cert, key, server, source, id, destination, after string }

func parseStorage(args []string, logs io.Writer) (storageOptions, error) {
	var o storageOptions
	if len(args) == 0 {
		return o, errors.New("usage: mesh-agent storage keygen | serve | upload | list | download")
	}
	o.command = args[0]
	f := flag.NewFlagSet("storage "+o.command, flag.ContinueOnError)
	f.SetOutput(logs)
	switch o.command {
	case "keygen", "serve", "upload", "list", "download":
	default:
		return o, errors.New("unknown storage command")
	}
	f.StringVar(&o.keyFile, "key-file", "", "private file containing the storage key")
	if o.command == "serve" {
		f.StringVar(&o.root, "root", "", "dedicated storage directory")
		f.StringVar(&o.listen, "listen", "127.0.0.1:7332", "listen address; non-loopback requires TLS")
		f.StringVar(&o.cert, "tls-cert", "", "TLS certificate PEM file")
		f.StringVar(&o.key, "tls-key", "", "TLS private key PEM file")
	}
	if o.command == "upload" || o.command == "list" || o.command == "download" {
		f.StringVar(&o.server, "server", "", "storage origin URL")
	}
	if o.command == "upload" {
		f.StringVar(&o.source, "source", "", "folder to copy")
	}
	if o.command == "download" {
		f.StringVar(&o.id, "id", "", "collection ID")
		f.StringVar(&o.destination, "destination", "", "new destination folder (must not exist)")
	}
	if o.command == "list" {
		f.StringVar(&o.after, "after", "", "last collection ID of the previous page")
	}
	if err := f.Parse(args[1:]); err != nil {
		return o, err
	}
	if f.NArg() != 0 || o.keyFile == "" {
		return o, errors.New("storage commands require --key-file and no positional arguments")
	}
	switch o.command {
	case "serve":
		host, _, err := net.SplitHostPort(o.listen)
		if err != nil || o.root == "" {
			return o, errors.New("serve requires --root and a valid --listen host:port")
		}
		if (o.cert == "") != (o.key == "") {
			return o, errors.New("both --tls-cert and --tls-key are required")
		}
		if o.cert == "" && !net.ParseIP(host).IsLoopback() {
			return o, errors.New("non-loopback listeners require TLS; use a loopback IP for local development")
		}
	case "upload":
		if o.source == "" || o.server == "" {
			return o, errors.New("upload requires --source and --server")
		}
	case "download":
		if o.id == "" || o.destination == "" || o.server == "" {
			return o, errors.New("download requires --id, --destination and --server")
		}
	case "list":
		if o.server == "" {
			return o, errors.New("list requires --server")
		}
	}
	return o, nil
}
func executeStorage(ctx context.Context, args []string, output, logs io.Writer) error {
	o, err := parseStorage(args, logs)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	if o.command == "keygen" {
		return generateStorageKey(o.keyFile)
	}
	key, err := readStorageKey(o.keyFile)
	if err != nil {
		return err
	}
	if o.command == "serve" {
		return serveStorage(ctx, o, key, logs)
	}
	client, err := storage.NewClient(o.server, key)
	if err != nil {
		return err
	}
	switch o.command {
	case "upload":
		id, err := client.Upload(ctx, o.source)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, id)
		return err
	case "list":
		ids, err := client.List(ctx, o.after)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(ids)
	case "download":
		return client.Download(ctx, o.id, o.destination)
	}
	return errors.New("unknown storage command")
}
func generateStorageKey(name string) (err error) {
	data := make([]byte, 32)
	if _, err = rand.Read(data); err != nil {
		return err
	}
	f, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if _, err = f.WriteString(base64.RawURLEncoding.EncodeToString(data) + "\n"); err != nil {
		return err
	}
	return f.Sync()
}
func readStorageKey(name string) (string, error) {
	info, err := os.Lstat(name)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > 128 || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return "", errors.New("storage key must be a small private regular file (0600 on Unix)")
	}
	f, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 129))
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(string(data))
	if len(data) > 128 || !storage.ValidKey(key) {
		return "", errors.New("invalid storage key file")
	}
	return key, nil
}
func serveStorage(ctx context.Context, o storageOptions, key string, logs io.Writer) (err error) {
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
	server := &http.Server{Handler: storage.Handler(store, key), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 2 * time.Minute, WriteTimeout: 30 * time.Minute, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 8192}
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
