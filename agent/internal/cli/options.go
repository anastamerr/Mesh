package cli

import (
	"errors"
	"flag"
	"io"
	"os"
	"strings"
	"time"
)

type options struct {
	command    string
	stateDir   string
	server     string
	name       string
	tokenStdin bool
	interval   time.Duration
}

func parseOptions(args []string, logs io.Writer) (options, error) {
	var o options
	if len(args) == 0 {
		return o, errors.New("usage: mesh-agent info | enroll | status | heartbeat | run (use <command> --help)")
	}
	o.command = args[0]
	switch o.command {
	case "info", "enroll", "status", "heartbeat", "run":
	default:
		return o, errors.New("unknown command; use info, enroll, status, heartbeat, or run")
	}
	flags := flag.NewFlagSet(o.command, flag.ContinueOnError)
	flags.SetOutput(logs)
	if o.command != "info" {
		flags.StringVar(&o.stateDir, "state-dir", "", "private directory for this node identity (default: OS user configuration directory)")
	}
	if o.command == "enroll" {
		hostname, _ := os.Hostname()
		flags.StringVar(&o.server, "server", "", "control plane HTTPS origin (HTTP allowed only on loopback)")
		flags.StringVar(&o.name, "name", hostname, "display name of this machine")
		flags.BoolVar(&o.tokenStdin, "token-stdin", false, "read a one-time enrollment token from stdin until EOF")
	}
	if o.command == "run" {
		flags.DurationVar(&o.interval, "interval", 15*time.Second, "heartbeat interval, between 1s and 30s")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return o, err
	}
	if flags.NArg() != 0 {
		return o, errors.New("unexpected positional arguments")
	}
	if o.command == "run" && (o.interval < time.Second || o.interval > 30*time.Second) {
		return o, errors.New("heartbeat interval must be between 1s and 30s")
	}
	if o.command == "enroll" {
		if !o.tokenStdin {
			return o, errors.New("enroll requires --token-stdin; do not put tokens in command arguments")
		}
		o.name = strings.TrimSpace(o.name)
		if o.name == "" || len(o.name) > 100 {
			return o, errors.New("name must contain 1 to 100 bytes")
		}
		if o.server == "" {
			return o, errors.New("enroll requires --server")
		}
	}
	return o, nil
}
