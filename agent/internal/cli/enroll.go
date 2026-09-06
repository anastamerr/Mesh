package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"runtime"
	"strings"

	"mesh.local/agent/internal/buildinfo"
	"mesh.local/agent/internal/control"
	"mesh.local/agent/internal/state"
)

var tokenPattern = regexp.MustCompile(`^mesh_enroll_[A-Za-z0-9_-]{43}$`)

func enroll(ctx context.Context, o options, store *state.Store, client *control.Client, input io.Reader, output io.Writer) error {
	_, err := store.Load()
	if err == nil {
		return errors.New("already enrolled; existing identity was not overwritten")
	}
	if !os.IsNotExist(err) {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(input, 257))
	if err != nil || len(data) > 256 || !tokenPattern.MatchString(strings.TrimSpace(string(data))) {
		return errors.New("stdin must contain a valid one-time enrollment token")
	}
	identity, err := client.Enroll(ctx, control.Enrollment{
		Token: strings.TrimSpace(string(data)), Name: o.name, Platform: runtime.GOOS,
		Architecture: runtime.GOARCH, AgentVersion: buildinfo.Version,
	})
	if err != nil {
		return fmt.Errorf("enrollment failed: %w; if the response was lost, check the controller before issuing a new token", err)
	}
	saved := state.State{Version: 1, Server: strings.TrimSuffix(o.server, "/"), NodeID: identity.Node.ID,
		Name: o.name, Credential: identity.Credential, ExpiresAt: identity.ExpiresAt}
	if err := store.Save(saved); err != nil {
		return fmt.Errorf("enrolled node %s but local state was not saved; revoke that identity before retrying: %w", identity.Node.ID, err)
	}
	_, err = fmt.Fprintf(output, "Enrolled %s as node %s\n", saved.Name, saved.NodeID)
	return err
}
