package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"mesh.local/agent/internal/storage"
)

func TestUploadRecoversLostBatchAcknowledgement(t *testing.T) {
	for _, partialResponse := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial-response=%t", partialResponse), func(t *testing.T) {
			testUploadRecoversLostBatchAcknowledgement(t, partialResponse)
		})
	}
}

func testUploadRecoversLostBatchAcknowledgement(t *testing.T, partialResponse bool) {
	root := filepath.Join(t.TempDir(), "store")
	store, err := storage.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source := t.TempDir()
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	key := strings.Repeat("a", 43)
	handler := storage.AuthorizedHandler(store, storage.LocalAuthorizer(key), nil)
	var writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" && writes.Add(1) == 1 {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, r)
			if recorder.Code != 200 {
				t.Errorf("commit failed: %d", recorder.Code)
			}
			if partialResponse {
				w.Header().Set("Content-Length", "100")
				w.WriteHeader(http.StatusOK)
				io.WriteString(w, "{")
				w.(http.Flusher).Flush()
			}
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	client, _ := storage.NewClient(server.URL, key)
	folder, err := storage.Prepare(context.Background(), source, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer folder.Close()
	var logs bytes.Buffer
	id, err := uploadWithRecovery(context.Background(), client, folder, &logs)
	if err != nil {
		t.Fatal(err)
	}
	if writes.Load() != 1 {
		t.Fatalf("retransmitted already committed batch: %d", writes.Load())
	}
	if !strings.Contains(logs.String(), "checking saved progress") {
		t.Fatal("retry was invisible")
	}
	destination := filepath.Join(t.TempDir(), "download")
	if err := client.Download(context.Background(), id, destination); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b"} {
		data, err := os.ReadFile(filepath.Join(destination, name))
		if err != nil || string(data) != name {
			t.Fatal("recovered bytes differ", err)
		}
	}
}

type cancelOnWrite struct{ cancel context.CancelFunc }

func (w cancelOnWrite) Write(p []byte) (int, error) { w.cancel(); return len(p), nil }
func TestUploadRetryCancellationAndPermanentDenial(t *testing.T) {
	for _, status := range []int{401, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(status) }))
			defer server.Close()
			client, _ := storage.NewClient(server.URL, strings.Repeat("a", 43))
			folder, err := storage.Prepare(context.Background(), t.TempDir(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer folder.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var logs io.Writer = io.Discard
			if status == 503 {
				logs = cancelOnWrite{cancel}
			}
			if _, err := uploadWithRecovery(ctx, client, folder, logs); err == nil {
				t.Fatal("failed operation reported success")
			}
			if requests.Load() != 1 {
				t.Fatal("retried after cancellation or permanent denial")
			}
		})
	}
}
