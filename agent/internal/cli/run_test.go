package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mesh.local/agent/internal/state"
	"mesh.local/agent/internal/storage"
)

type listenerLog struct{ ready chan string }

func (w listenerLog) Write(p []byte) (int, error) {
	if address, ok := strings.CutPrefix(string(p), "Storage listening on "); ok {
		select {
		case w.ready <- strings.TrimSpace(address):
		default:
		}
	}
	return len(p), nil
}
func TestRunServesStorageAndStopsOnRevocation(t *testing.T) {
	var heartbeats atomic.Int32
	var revoked atomic.Bool
	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/heartbeat") {
			heartbeats.Add(1)
			if revoked.Load() {
				w.WriteHeader(401)
				return
			}
			fmt.Fprint(w, `{"accepted":true}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/storage-grants/validate") {
			fmt.Fprint(w, `{"accepted":true}`)
			return
		}
		w.WriteHeader(404)
	}))
	defer controller.Close()
	dir := filepath.Join(t.TempDir(), "identity")
	saved, err := state.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = saved.Save(state.State{Version: 1, Server: controller.URL, NodeID: "12345678-1234-4234-8234-123456789abc", Name: "test", Credential: "mesh_node_" + strings.Repeat("a", 43), ExpiresAt: time.Now().Add(time.Hour)})
	saved.Close()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ready := make(chan string, 1)
	done := make(chan error, 1)
	root := filepath.Join(t.TempDir(), "storage")
	go func() {
		done <- Execute(ctx, []string{"run", "--state-dir", dir, "--root", root, "--listen", "127.0.0.1:0", "--interval", "1s"}, nil, io.Discard, listenerLog{ready})
	}()
	var address string
	select {
	case address = <-ready:
	case <-ctx.Done():
		t.Fatal("listener did not start")
	}
	// Missing credentials are rejected while the same process sends heartbeats.
	res, err := http.Get("http://" + address + "/v1/collections")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatal(res.StatusCode)
	}
	client, err := storage.NewClient("http://"+address, strings.Repeat("a", 43))
	if err != nil {
		t.Fatal(err)
	}
	ids, err := client.List(ctx, "")
	if err != nil || len(ids) != 0 {
		t.Fatal("combined node did not authorize storage", err)
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for heartbeats.Load() == 0 {
		select {
		case <-deadline.C:
			t.Fatal("no heartbeat")
		case <-time.After(10 * time.Millisecond):
		}
	}
	revoked.Store(true)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("revocation was not reported")
		}
	case <-ctx.Done():
		t.Fatal("revoked node did not stop")
	}
	reopened, err := storage.Open(root)
	if err != nil {
		t.Fatal("storage lock leaked", err)
	}
	reopened.Close()
	if err := Execute(context.Background(), []string{"status", "--state-dir", dir}, nil, io.Discard, io.Discard); err != nil {
		t.Fatal("identity lock leaked", err)
	}
}
