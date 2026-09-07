package cli

import (
	"errors"
	"flag"
	"io"
	"net"
)

type storageOptions struct {
	command     string
	keyFile     string
	root        string
	listen      string
	cert        string
	key         string
	server      string
	source      string
	id          string
	destination string
	after       string
	stateDir    string
	enrolled    bool
}

func parseStorage(args []string, logs io.Writer) (storageOptions, error) {
	var o storageOptions
	if len(args) == 0 {
		return o, errors.New("usage: mesh-agent storage keygen | serve | identify | upload | list | download")
	}
	o.command = args[0]
	f := flag.NewFlagSet("storage "+o.command, flag.ContinueOnError)
	f.SetOutput(logs)
	switch o.command {
	case "keygen", "serve", "identify", "upload", "list", "download":
	default:
		return o, errors.New("unknown storage command")
	}
	f.StringVar(&o.keyFile, "key-file", "", "private file containing the storage key")
	if o.command == "serve" {
		f.BoolVar(&o.enrolled, "enrolled", false, "validate storage grants with the enrolled control plane")
		f.StringVar(&o.stateDir, "state-dir", "", "node identity directory for enrolled serving")
		f.StringVar(&o.root, "root", "", "dedicated storage directory")
		f.StringVar(&o.listen, "listen", "127.0.0.1:7332", "listen address; non-loopback requires TLS")
		f.StringVar(&o.cert, "tls-cert", "", "TLS certificate PEM file")
		f.StringVar(&o.key, "tls-key", "", "TLS private key PEM file")
	}
	if o.command == "upload" || o.command == "list" || o.command == "download" {
		f.StringVar(&o.server, "server", "", "storage origin URL")
	}
	if o.command == "upload" || o.command == "identify" {
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
	if f.NArg() != 0 || (o.keyFile == "" && o.command != "identify" && !o.enrolled) {
		return o, errors.New("storage commands require --key-file (or serve --enrolled) and no positional arguments")
	}
	switch o.command {
	case "serve":
		if (o.enrolled && o.keyFile != "") || (!o.enrolled && o.stateDir != "") {
			return o, errors.New("choose enrolled identity or a loopback development key, not both")
		}
		if err := validateServing(o); err != nil {
			return o, err
		}
	case "identify":
		if o.source == "" {
			return o, errors.New("identify requires --source")
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

func validateServing(o storageOptions) error {
	host, _, err := net.SplitHostPort(o.listen)
	if err != nil || o.root == "" {
		return errors.New("serve requires --root and a valid --listen host:port")
	}
	if (o.cert == "") != (o.key == "") {
		return errors.New("both --tls-cert and --tls-key are required")
	}
	if !o.enrolled && !net.ParseIP(host).IsLoopback() {
		return errors.New("shared storage keys are limited to loopback development; use --enrolled for remote serving")
	}
	if o.cert == "" && !net.ParseIP(host).IsLoopback() {
		return errors.New("non-loopback listeners require TLS; use a loopback IP for local development")
	}
	return nil
}
