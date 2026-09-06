// Package cli is the foreground development interface. Windows service hosting
// will wrap the same runner after the service-account lifecycle is validated.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"time"

	"mesh.local/agent/internal/buildinfo"
	"mesh.local/agent/internal/control"
	"mesh.local/agent/internal/inventory"
	"mesh.local/agent/internal/runner"
	"mesh.local/agent/internal/state"
)

func Execute(ctx context.Context, args []string, input io.Reader, output, logs io.Writer) error {
	if len(args) > 0 && args[0] == "storage" {
		return executeStorage(ctx, args[1:], output, logs)
	}
	o, err := parseOptions(args, logs)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	if o.command == "info" {
		return showInfo(ctx, output)
	}
	if o.command == "enroll" {
		// Validate transport policy before creating state or consuming a token.
		client, err := control.New(o.server)
		if err != nil {
			return err
		}
		return withState(o.stateDir, func(store *state.Store) error {
			return enroll(ctx, o, store, client, input, output)
		})
	}
	return withState(o.stateDir, func(store *state.Store) error {
		saved, err := store.Load()
		if os.IsNotExist(err) {
			return errors.New("not enrolled; run mesh-agent enroll first")
		}
		if err != nil {
			return err
		}
		if o.command == "status" {
			return showStatus(saved, output)
		}
		client, err := control.New(saved.Server)
		if err != nil {
			return err
		}
		if o.command == "run" {
			return runner.Loop(ctx, &saved, store, client, inventory.Read, o.interval, logs)
		}
		if err := runner.Once(ctx, &saved, store, client, inventory.Read); err != nil {
			return err
		}
		_, err = fmt.Fprintf(output, "Heartbeat accepted for node %s\n", saved.NodeID)
		return err
	})
}

// Keep lock ownership and cleanup in one place for all stateful commands.
func withState(dir string, command func(*state.Store) error) (err error) {
	if dir == "" {
		dir, err = state.DefaultDir()
		if err != nil {
			return err
		}
	}
	store, err := state.Open(dir)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, store.Close()) }()
	return command(store)
}

func showInfo(ctx context.Context, output io.Writer) error {
	host, err := inventory.Read(ctx)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(struct {
		Version      string            `json:"agentVersion"`
		Platform     string            `json:"platform"`
		Architecture string            `json:"architecture"`
		Inventory    control.Inventory `json:"inventory"`
	}{buildinfo.Version, runtime.GOOS, runtime.GOARCH, host})
}

func showStatus(saved state.State, output io.Writer) error {
	// Explicit projection prevents credentials from leaking into CLI output.
	return json.NewEncoder(output).Encode(struct {
		NodeID       string    `json:"nodeId"`
		Name         string    `json:"name"`
		Server       string    `json:"server"`
		ExpiresAt    time.Time `json:"credentialExpiresAt"`
		NextSequence uint64    `json:"nextSequence"`
	}{saved.NodeID, saved.Name, saved.Server, saved.ExpiresAt, saved.NextSequence})
}
