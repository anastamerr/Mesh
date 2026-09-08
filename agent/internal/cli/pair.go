package cli

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"mesh.local/agent/internal/buildinfo"
	"mesh.local/agent/internal/control"
	"mesh.local/agent/internal/state"
)

func pairCommand(ctx context.Context, args []string, output, logs io.Writer) error {
	flags := flag.NewFlagSet("pair", flag.ContinueOnError)
	flags.SetOutput(logs)
	var server, name, dir string
	host, _ := os.Hostname()
	flags.StringVar(&server, "server", "", "Mesh controller HTTPS origin")
	flags.StringVar(&name, "name", host, "device name shown during pairing")
	flags.StringVar(&dir, "state-dir", "", "private device identity directory")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	name = strings.TrimSpace(name)
	if flags.NArg() != 0 || name == "" || len(name) > 100 {
		return errors.New("pair requires a device name of 1 to 100 bytes")
	}
	client, err := control.New(server)
	if err != nil {
		return err
	}
	return withState(dir, func(store *state.Store) error {
		if _, err := store.Load(); err == nil {
			return errors.New("device is already enrolled; identity was not replaced")
		} else if !os.IsNotExist(err) {
			return err
		}
		_, fingerprint, err := store.DeviceCertificate()
		if err != nil {
			return err
		}
		pending, err := store.LoadPairing()
		if os.IsNotExist(err) {
			id := make([]byte, 16)
			if _, err := rand.Read(id); err != nil {
				return err
			}
			id[6] = (id[6] & 15) | 64
			id[8] = (id[8] & 63) | 128
			secret := make([]byte, 64)
			if _, err := rand.Read(secret); err != nil {
				return err
			}
			pending = state.PendingPairing{ID: fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:]),
				Server: strings.TrimSuffix(server, "/"), Name: name, AgentVersion: buildinfo.Version, Secret: "mesh_pair_" + base64.RawURLEncoding.EncodeToString(secret[:32]), Credential: "mesh_node_" + base64.RawURLEncoding.EncodeToString(secret[32:])}
			if err := store.SavePairing(pending); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		if pending.Server != strings.TrimSuffix(server, "/") || pending.Name != name {
			return errors.New("an unfinished pairing belongs to a different controller or name; resume with its original settings")
		}
		hash := func(value string) string {
			digest := sha256.Sum256([]byte(value))
			return hex.EncodeToString(digest[:])
		}
		if pending.AgentVersion == "" {
			pending.AgentVersion = buildinfo.Version
			if err := store.SavePairing(pending); err != nil {
				return err
			}
		}
		challenge, err := client.StartPairing(ctx, control.PairingRequest{ID: pending.ID, Name: name, Platform: runtime.GOOS, Architecture: runtime.GOARCH, AgentVersion: pending.AgentVersion, PublicKeyFingerprint: fingerprint, PairingSecretHash: hash(pending.Secret), NodeCredentialHash: hash(pending.Credential)})
		if err != nil {
			return pairingFailure(store, err)
		}
		if err := json.NewEncoder(output).Encode(challenge); err != nil {
			return err
		}
		fmt.Fprintf(logs, "Pair %s using code %s. Confirm this device fingerprint on your main laptop:\n%s\nWaiting for approval...\n", name, challenge.Code, fingerprint)
		for {
			status, err := client.PollPairing(ctx, pending.ID, pending.Secret)
			if err == nil && status.Status == "approved" {
				if status.Node.PublicKeyFingerprint != fingerprint || status.Node.Name != name {
					return control.ErrProtocol
				}
				saved := state.State{Version: 1, Server: pending.Server, NodeID: status.Node.ID, Name: name, Credential: pending.Credential, ExpiresAt: status.CredentialExpiresAt, PublicKeyFingerprint: fingerprint}
				saved.RelayOrigin, err = client.RelayOrigin(ctx)
				if err != nil {
					return err
				}
				if err := store.Save(saved); err != nil {
					return err
				}
				if err := store.ClearPairing(); err != nil {
					return err
				}
				fmt.Fprintln(logs, "Device paired. Its identity is saved.")
				return showStatus(saved, output)
			}
			if err != nil && control.Permanent(err) {
				return pairingFailure(store, err)
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if time.Now().After(challenge.ExpiresAt) {
				return errors.New("pairing expired; request a fresh pairing before retrying")
			}
			timer := time.NewTimer(2 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	})
}

func pairingFailure(store *state.Store, err error) error {
	var api *control.APIError
	if errors.As(err, &api) && api.Status == 410 {
		if clearErr := store.ClearPairing(); clearErr != nil {
			return errors.Join(err, clearErr)
		}
		return errors.New("pairing expired; rerun pair to request a fresh code")
	}
	return err
}
