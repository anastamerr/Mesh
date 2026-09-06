package cli

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"mesh.local/agent/internal/control"
	"mesh.local/agent/internal/state"
	"mesh.local/agent/internal/storage"
)

func executeStorage(ctx context.Context, args []string, input io.Reader, output, logs io.Writer) error {
	if len(args) > 0 && (args[0] == "copy" || args[0] == "get" || args[0] == "catalog") {
		return managedStorage(ctx, args, input, output, logs)
	}
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
	if o.command == "identify" {
		root, err := os.OpenRoot(o.source)
		if err != nil {
			return err
		}
		defer root.Close()
		manifest, err := storage.Scan(ctx, root)
		if err != nil {
			return err
		}
		id, err := manifest.ID()
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, id)
		return err
	}
	if o.command == "serve" && o.enrolled {
		var saved state.State
		if err := withState(o.stateDir, func(store *state.Store) error { var err error; saved, err = store.Load(); return err }); err != nil {
			return err
		}
		client, err := control.New(saved.Server)
		if err != nil {
			return err
		}
		authorize := func(ctx context.Context, token string, permission storage.Permission) error {
			err := client.AuthorizeStorage(ctx, saved.NodeID, saved.Credential, token, permission.Access, permission.CollectionID)
			if err == nil {
				return nil
			}
			var api *control.APIError
			if errors.As(err, &api) && (api.Status == 401 || api.Status == 403) {
				return storage.ErrUnauthorized
			}
			return storage.ErrAuthorizationUnavailable
		}
		return serveStorage(ctx, o, authorize, logs, func(ctx context.Context, id string, m storage.Manifest) error {
			count, total := m.Statistics()
			return client.ConfirmCollection(ctx, saved.NodeID, saved.Credential, id, count, total)
		})
	}
	key, err := readStorageKey(o.keyFile)
	if err != nil {
		return err
	}
	if o.command == "serve" {
		return serveStorage(ctx, o, storage.LocalAuthorizer(key), logs, nil)
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
