package storage

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
	"time"
)

type legacyWriter struct{ http.ResponseWriter }

func (w legacyWriter) WriteHeader(code int) {
	w.Header().Del("Mesh-Transfer-Features")
	w.ResponseWriter.WriteHeader(code)
}
func (w legacyWriter) Write(data []byte) (int, error) {
	w.Header().Del("Mesh-Transfer-Features")
	return w.ResponseWriter.Write(data)
}

func TestBatchRoundTripAndLegacyNegotiation(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint("legacy=", legacy), func(t *testing.T) {
			source := privateDir(t)
			files := map[string][]byte{"a": []byte("first"), "b-empty": {}, "c": []byte("third"), "d/child": []byte("nested"), "e-large": bytes.Repeat([]byte{42}, maxBatchFileBytes+1), "f": []byte("last")}
			if err := os.Mkdir(filepath.Join(source, "d"), 0700); err != nil {
				t.Fatal(err)
			}
			for name, data := range files {
				if err := os.WriteFile(filepath.Join(source, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			store := openStore(t, privateDir(t))
			defer store.Close()
			handler := AuthorizedHandler(store, LocalAuthorizer(testKey), nil)
			var batches atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/batch") {
					batches.Add(1)
				}
				if legacy {
					handler.ServeHTTP(legacyWriter{w}, r)
				} else {
					handler.ServeHTTP(w, r)
				}
			}))
			defer server.Close()
			client, _ := NewClient(server.URL, testKey)
			id, err := client.Upload(testContext, source)
			if err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(privateDir(t), "copy")
			if err = client.Download(testContext, id, destination); err != nil {
				t.Fatal(err)
			}
			for name, want := range files {
				got, err := os.ReadFile(filepath.Join(destination, name))
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("%s: %v", name, err)
				}
			}
			if (batches.Load() > 0) == legacy {
				t.Fatalf("batch negotiation: %d", batches.Load())
			}
		})
	}
}
func TestBatchRejectsMalformedBoundsBeforeWriting(t *testing.T) {
	s := openStore(t, privateDir(t))
	defer s.Close()
	m := Manifest{Version: 1, Entries: []Entry{entry("a", []byte("a")), entry("b", []byte("b"))}}
	p := begin(t, s, m)
	handler := AuthorizedHandler(s, LocalAuthorizer(testKey), nil)
	for _, indices := range []string{"-1", "0,0", "1,0", "2", "0,1,2", strings.Repeat("0,", maxBatchFiles) + "0"} {
		r := httptest.NewRequest("PUT", "/v1/collections/"+p.ID+"/batch?indices="+indices, strings.NewReader("ab"))
		r.Header.Set("Authorization", "Bearer "+testKey)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("%q: %d", indices, w.Code)
		}
	}
	for _, body := range []string{"a", "abc"} {
		r := httptest.NewRequest("PUT", batchPath(p.ID, []int{0, 1}), strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+testKey)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
	got := begin(t, s, m)
	if got.Offsets[0] != 0 || got.Offsets[1] != 0 {
		t.Fatal("invalid batch wrote files")
	}
}
func TestFailedBatchKeepsOffsetsUncommitted(t *testing.T) {
	s := openStore(t, privateDir(t))
	defer s.Close()
	m := Manifest{Version: 1, Entries: []Entry{entry("a", []byte("a")), entry("b", []byte("b"))}}
	p := begin(t, s, m)
	handler := AuthorizedHandler(s, LocalAuthorizer(testKey), nil)
	r := httptest.NewRequest("PUT", batchPath(p.ID, []int{0, 1}), strings.NewReader("ax"))
	r.Header.Set("Authorization", "Bearer "+testKey)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 409 {
		t.Fatal(w.Code)
	}
	got := begin(t, s, m)
	if got.Offsets[0] != 0 || got.Offsets[1] != 0 {
		t.Fatalf("durable offsets: %v", got.Offsets)
	}
	write(t, s, p.ID, 0, 0, []byte("a"))
	write(t, s, p.ID, 1, 0, []byte("b"))
	if _, err := s.Finish(testContext, p.ID); err != nil {
		t.Fatal(err)
	}
}
func TestBatchScopeAndRevocation(t *testing.T) {
	s := openStore(t, privateDir(t))
	defer s.Close()
	m := Manifest{Version: 1, Entries: []Entry{entry("a", []byte("a")), entry("b", []byte("b"))}}
	p := begin(t, s, m)
	revoked := false
	handler := AuthorizedHandler(s, func(_ context.Context, _ string, permission Permission) error {
		if revoked || permission.Access != "write" || permission.CollectionID == nil || *permission.CollectionID != p.ID {
			return ErrUnauthorized
		}
		return nil
	}, nil)
	request := func(method string) int {
		r := httptest.NewRequest(method, batchPath(p.ID, []int{0, 1}), strings.NewReader("ab"))
		r.Header.Set("Authorization", "Bearer "+testKey)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	if request("GET") != 401 || request("PUT") != 200 {
		t.Fatal("incorrect batch scope")
	}
	revoked = true
	if request("PUT") != 401 {
		t.Fatal("cached authorization")
	}
}
func TestTruncatedBatchDownloadRemovesPartialOutput(t *testing.T) {
	s := openStore(t, privateDir(t))
	defer s.Close()
	source := privateDir(t)
	for _, name := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	handler := AuthorizedHandler(s, LocalAuthorizer(testKey), nil)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/batch") {
			w.Header().Set("Content-Length", "2")
			io.WriteString(w, "a")
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	c, _ := NewClient(server.URL, testKey)
	id, err := c.Upload(testContext, source)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(privateDir(t), "copy")
	if err = c.Download(testContext, id, dest); err == nil {
		t.Fatal("truncated download accepted")
	}
	if _, err = os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("partial destination retained")
	}
}

// Real filesystem + HTTP transfers; the authorizer adds 20 ms per request to
// expose round-trip amplification. This is controlled latency, not a WAN claim.
func BenchmarkSmallFolderTransfer(b *testing.B) {
	source := b.TempDir()
	for i := 0; i < 500; i++ {
		if err := os.WriteFile(filepath.Join(source, fmt.Sprintf("%04d", i)), bytes.Repeat([]byte{byte(i)}, 4096), 0600); err != nil {
			b.Fatal(err)
		}
	}
	for _, legacy := range []bool{true, false} {
		b.Run(fmt.Sprint("legacy=", legacy), func(b *testing.B) {
			var requests atomic.Int64
			b.SetBytes(500 * 4096 * 2)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				root := b.TempDir()
				if err := os.Chmod(root, 0700); err != nil {
					b.Fatal(err)
				}
				s, err := Open(root)
				if err != nil {
					b.Fatal(err)
				}
				handler := AuthorizedHandler(s, func(ctx context.Context, key string, p Permission) error {
					requests.Add(1)
					time.Sleep(20 * time.Millisecond)
					return LocalAuthorizer(testKey)(ctx, key, p)
				}, nil)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if legacy {
						handler.ServeHTTP(legacyWriter{w}, r)
					} else {
						handler.ServeHTTP(w, r)
					}
				}))
				c, _ := NewClient(server.URL, testKey)
				id, err := c.Upload(testContext, source)
				if err == nil {
					err = c.Download(testContext, id, filepath.Join(b.TempDir(), "copy"))
				}
				server.Close()
				s.Close()
				if err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(requests.Load())/float64(b.N), "requests/op")
		})
	}
}

func TestBatchJournalFailureRollsBackAllOffsets(t *testing.T) {
	s := openStore(t, privateDir(t))
	defer s.Close()
	m := Manifest{Version: 1, Entries: []Entry{entry("a", []byte("a")), entry("b", []byte("b"))}}
	p := begin(t, s, m)
	_, err := s.db.Exec(`CREATE TRIGGER fail_batch BEFORE UPDATE ON files WHEN NEW.ordinal=1 BEGIN SELECT RAISE(ABORT,'injected journal failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.writeBatch(testContext, p.ID, m, []int{0, 1}, []byte("ab")); err == nil {
		t.Fatal("journal failure ignored")
	}
	got := begin(t, s, m)
	if got.Offsets[0] != 0 || got.Offsets[1] != 0 {
		t.Fatal("partial journal commit", got.Offsets)
	}
	if _, err := s.db.Exec("DROP TRIGGER fail_batch"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.writeBatch(testContext, p.ID, m, []int{0, 1}, []byte("ab")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Finish(testContext, p.ID); err != nil {
		t.Fatal(err)
	}
}

func TestBatchPayloadBudget(t *testing.T) {
	m := Manifest{Version: 1, Entries: make([]Entry, 20)}
	for i := range m.Entries {
		m.Entries[i] = Entry{Path: fmt.Sprintf("%02d", i), Size: maxBatchFileBytes}
	}
	indices := batchIndices(m, 0, nil)
	if len(indices) != ChunkSize/maxBatchFileBytes {
		t.Fatalf("batch exceeds payload budget: %d files", len(indices))
	}
	parts := make([]string, 17)
	for i := range parts {
		parts[i] = fmt.Sprint(i)
	}
	if _, _, err := parseBatch(m, strings.Join(parts, ",")); err == nil {
		t.Fatal("oversized payload accepted")
	}
	m.Entries[0].Size = maxBatchFileBytes + 1
	if _, _, err := parseBatch(m, "0"); err == nil {
		t.Fatal("oversized file accepted")
	}
	if len(batchIndices(m, 0, nil)) != 0 {
		t.Fatal("large file entered batch")
	}
}
