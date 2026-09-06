package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

var testContext = context.Background()

const testKey = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func privateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}
func openStore(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func entry(name string, data []byte) Entry {
	h := sha256.Sum256(data)
	return Entry{Path: name, Size: int64(len(data)), SHA256: hex.EncodeToString(h[:])}
}
func begin(t *testing.T, s *Store, m Manifest) Progress {
	t.Helper()
	p, err := s.Begin(testContext, m)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func write(t *testing.T, s *Store, id string, index int, offset int64, data []byte) {
	t.Helper()
	next, err := s.Write(testContext, id, index, offset, data)
	if err != nil || next != offset+int64(len(data)) {
		t.Fatalf("write: %d %v", next, err)
	}
}

func TestRestartResumesDurableOffsetAndDiscardsUncommittedTail(t *testing.T) {
	dir := privateDir(t)
	s := openStore(t, dir)
	data := []byte("a resumable file")
	m := Manifest{Version: 1, Entries: []Entry{entry("file.txt", data)}}
	p := begin(t, s, m)
	write(t, s, p.ID, 0, 0, data[:4])
	f, err := os.OpenFile(filepath.Join(dir, "staging", p.ID, "file.txt"), os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.WriteString("uncommitted tail"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openStore(t, dir)
	defer s.Close()
	p = begin(t, s, m)
	if p.Offsets[0] != 4 {
		t.Fatalf("lost durable progress: %+v", p)
	}
	if _, err = s.Write(testContext, p.ID, 0, 0, data[:4]); !errors.Is(err, ErrConflict) {
		t.Fatal("stale offset accepted", err)
	}
	write(t, s, p.ID, 0, 4, data[4:])
	p, err = s.Finish(testContext, p.ID)
	if err != nil || !p.Complete {
		t.Fatal(p, err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "collections", p.ID, "file.txt"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("copy mismatch", err)
	}
	again := begin(t, s, m)
	if !again.Complete || again.ID != p.ID {
		t.Fatal("lost acknowledgement was not idempotent")
	}
}
func TestIntegrityAndPublicationBoundary(t *testing.T) {
	s := openStore(t, privateDir(t))
	defer s.Close()
	data := []byte("valid")
	m := Manifest{Version: 1, Entries: []Entry{entry("file", data)}}
	p := begin(t, s, m)
	if _, _, err := s.Read(testContext, p.ID, 0); !errors.Is(err, ErrNotFound) {
		t.Fatal("incomplete file exposed", err)
	}
	if ids, err := s.List(testContext, ""); err != nil || len(ids) != 0 {
		t.Fatal(ids, err)
	}
	if _, err := s.Finish(testContext, p.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("incomplete folder published", err)
	}
	if _, err := s.Write(testContext, p.ID, 0, 0, []byte("wrong")); !errors.Is(err, ErrConflict) {
		t.Fatal("bad checksum accepted", err)
	}
	p = begin(t, s, m)
	if p.Offsets[0] != 0 {
		t.Fatal("bad file cannot restart")
	}
	write(t, s, p.ID, 0, 0, data)
	if _, err := s.Finish(testContext, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write(testContext, p.ID, 0, 0, data); !errors.Is(err, ErrConflict) {
		t.Fatal("published file writable", err)
	}
}
func TestRecoversRenameBeforeJournalCommit(t *testing.T) {
	dir := privateDir(t)
	s := openStore(t, dir)
	m := Manifest{Version: 1, Entries: []Entry{entry("file", []byte("x"))}}
	p := begin(t, s, m)
	write(t, s, p.ID, 0, 0, []byte("x"))
	if err := s.root.Rename("staging/"+p.ID, "collections/"+p.ID); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = openStore(t, dir)
	defer s.Close()
	p, err := s.Finish(testContext, p.ID)
	if err != nil || !p.Complete {
		t.Fatal(p, err)
	}
}
func TestConcurrentChunksCannotAdvanceSameOffsetTwice(t *testing.T) {
	s := openStore(t, privateDir(t))
	defer s.Close()
	m := Manifest{Version: 1, Entries: []Entry{entry("file", []byte("ab"))}}
	p := begin(t, s, m)
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := s.Write(testContext, p.ID, 0, 0, []byte("a")); results <- err }()
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal(success, conflict)
	}
}
func TestPortableManifestRejectsAmbiguousPaths(t *testing.T) {
	for _, name := range []string{"../x", "/x", "a/../x", `a\x`, "C:x", "nul.txt", "COM1", "CONIN$", "CONOUT$", "a.", "a ", "a//x", "a\x00b", "café"} {
		t.Run(name, func(t *testing.T) {
			m := Manifest{Version: 1, Entries: []Entry{entry(name, nil)}}
			if m.Validate() == nil {
				t.Fatal("accepted", name)
			}
		})
	}
	for _, entries := range [][]Entry{
		{entry("a", nil), entry("a/b", nil)},
		{{Path: "A", Directory: true}, entry("a/file", nil)},
		{entry("A", nil), entry("a", nil)},
		{entry("missing/file", nil)},
	} {
		if (Manifest{Version: 1, Entries: entries}).Validate() == nil {
			t.Fatal("ambiguous manifest accepted", entries)
		}
	}
}
func TestSymlinksCannotEscapeCollectionOrSource(t *testing.T) {
	dir := privateDir(t)
	s := openStore(t, dir)
	defer s.Close()
	outside := privateDir(t)
	target := filepath.Join(outside, "sentinel")
	if err := os.WriteFile(target, []byte("safe"), 0600); err != nil {
		t.Fatal(err)
	}
	m := Manifest{Version: 1, Entries: []Entry{{Path: "nested", Directory: true}, entry("nested/sentinel", []byte("bad!"))}}
	p := begin(t, s, m)
	root, err := s.staging(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	root.Close()
	if err = os.Symlink(outside, filepath.Join(dir, "staging", p.ID, "nested")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if _, err = s.Write(testContext, p.ID, 1, 0, []byte("bad!")); err == nil {
		t.Fatal("escaped approved root")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "safe" {
		t.Fatal("outside file changed")
	}
	source, err := os.OpenRoot(filepath.Join(dir, "staging", p.ID))
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	if _, err = Scan(testContext, source); err == nil {
		t.Fatal("source symlink accepted")
	}
}
func TestStorageRootLock(t *testing.T) {
	dir := privateDir(t)
	s := openStore(t, dir)
	defer s.Close()
	other, err := Open(dir)
	if err == nil {
		other.Close()
		t.Fatal("second writer accepted")
	}
}

func TestHTTPFolderRoundTripAndResumeAfterLostAcknowledgement(t *testing.T) {
	dir := privateDir(t)
	s := openStore(t, dir)
	source := privateDir(t)
	if err := os.MkdirAll(filepath.Join(source, "nested", "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	data := bytes.Repeat([]byte("mesh"), ChunkSize/4+11)
	for name, content := range map[string][]byte{"large.bin": data, "nested/zero": {}, "nested/small": []byte("hello")} {
		if err := os.WriteFile(filepath.Join(source, filepath.FromSlash(name)), content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	handler := Handler(s, testKey)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "PUT" {
			// Commit a chunk, then lose its acknowledgement by returning an invalid one.
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, r)
			if recorder.Code == 200 {
				w.WriteHeader(200)
				_, _ = w.Write([]byte(`{}`))
				return
			}
			w.WriteHeader(recorder.Code)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	c, err := NewClient(server.URL, testKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Upload(testContext, source); err == nil {
		t.Fatal("lost acknowledgement accepted")
	}
	server.Close()
	s.Close()
	s = openStore(t, dir)
	defer s.Close()
	server = httptest.NewServer(Handler(s, testKey))
	defer server.Close()
	c, err = NewClient(server.URL, testKey)
	if err != nil {
		t.Fatal(err)
	}
	id, err := c.Upload(testContext, source)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := c.List(testContext, "")
	if err != nil || len(ids) != 1 || ids[0] != id {
		t.Fatal(ids, err)
	}
	destination := filepath.Join(privateDir(t), "copy")
	if err = c.Download(testContext, id, destination); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"large.bin", "nested/zero", "nested/small"} {
		a, _ := os.ReadFile(filepath.Join(source, name))
		b, err := os.ReadFile(filepath.Join(destination, name))
		if err != nil || !bytes.Equal(a, b) {
			t.Fatal(name, err)
		}
	}
	if info, err := os.Stat(filepath.Join(destination, "nested", "empty")); err != nil || !info.IsDir() {
		t.Fatal("empty directory missing", err)
	}
	if err = c.Download(testContext, id, destination); err == nil {
		t.Fatal("existing destination overwritten")
	}
	same, err := c.Upload(testContext, source)
	if err != nil || same != id {
		t.Fatal("repeat upload not idempotent", err)
	}
}
func TestHTTPRejectsUnauthorizedAndOversizedInput(t *testing.T) {
	s := openStore(t, privateDir(t))
	defer s.Close()
	handler := Handler(s, testKey)
	cases := []struct {
		method, path, token, body string
		code                      int
	}{
		{"GET", "/v1/collections", "", "", 401},
		{"POST", "/v1/collections", testKey, strings.Repeat("x", MaxManifestBytes+1), 400},
		{"PUT", "/v1/collections/" + strings.Repeat("a", 64) + "/files/0", testKey, strings.Repeat("x", ChunkSize+1), 400},
	}
	for _, tc := range cases {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		r.Header.Set("Authorization", "Bearer "+tc.token)
		r.Header.Set("Upload-Offset", "0")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.code {
			t.Fatal(w.Code, tc.code)
		}
	}
}
func TestDownloadRejectsCorruptedBytesAndRemovesPartialDestination(t *testing.T) {
	s := openStore(t, privateDir(t))
	defer s.Close()
	handler := Handler(s, testKey)
	m := Manifest{Version: 1, Entries: []Entry{entry("file", []byte("safe"))}}
	p := begin(t, s, m)
	write(t, s, p.ID, 0, 0, []byte("safe"))
	if _, err := s.Finish(testContext, p.ID); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/files/") {
			_, _ = io.WriteString(w, "evil")
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	c, _ := NewClient(server.URL, testKey)
	dest := filepath.Join(privateDir(t), "copy")
	if err := c.Download(testContext, p.ID, dest); !errors.Is(err, ErrConflict) {
		t.Fatal("bad download accepted", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatal("partial destination retained", err)
	}
}

func TestEmptyFolderPublishesAndDownloads(t *testing.T) {
	s := openStore(t, privateDir(t))
	defer s.Close()
	server := httptest.NewServer(Handler(s, testKey))
	defer server.Close()
	c, err := NewClient(server.URL, testKey)
	if err != nil {
		t.Fatal(err)
	}
	id, err := c.Upload(testContext, privateDir(t))
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(privateDir(t), "empty")
	if err = c.Download(testContext, id, destination); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(destination)
	if err != nil || len(entries) != 0 {
		t.Fatal(entries, err)
	}
}

func TestClientRefusesRedirectsAndInsecureRemoteOrigins(t *testing.T) {
	for _, origin := range []string{"http://192.0.2.1", "https://example.com/path", "https://example.com?", "https://user:pass@example.com"} {
		if _, err := NewClient(origin, testKey); err == nil {
			t.Fatal("accepted origin", origin)
		}
	}
	reached := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	c, err := NewClient(redirect.URL, testKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.List(testContext, ""); err == nil || reached {
		t.Fatal("followed credential-bearing redirect")
	}
}

func TestNewerJournalIsNotDowngraded(t *testing.T) {
	dir := privateDir(t)
	s := openStore(t, dir)
	if _, err := s.db.Exec("PRAGMA user_version=2"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	newer, err := Open(dir)
	if err == nil {
		newer.Close()
		t.Fatal("opened unsupported journal")
	}
}

func TestSQLiteReplacementConnectionKeepsSafetySettings(t *testing.T) {
	// URI escaping prevents punctuation in a chosen root becoming DSN options.
	dir := filepath.Join(privateDir(t), "storage #1 & data")
	s := openStore(t, dir)
	defer s.Close()
	s.db.SetMaxIdleConns(0)
	for attempt := 0; attempt < 2; attempt++ {
		var foreignKeys, synchronous int
		if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
			t.Fatal(err)
		}
		if err := s.db.QueryRow("PRAGMA synchronous").Scan(&synchronous); err != nil {
			t.Fatal(err)
		}
		if foreignKeys != 1 || synchronous != 2 {
			t.Fatalf("connection lost settings: foreign_keys=%d synchronous=%d", foreignKeys, synchronous)
		}
	}
	s.db.SetMaxIdleConns(1)
	p := begin(t, s, Manifest{Version: 1, Entries: []Entry{entry("file", []byte("x"))}})
	write(t, s, p.ID, 0, 0, []byte("x"))
	if _, err := s.Finish(testContext, p.ID); err != nil {
		t.Fatal(err)
	}
}

func TestFinalizationDetectsMissingEmptyDirectoryDuringRecovery(t *testing.T) {
	s := openStore(t, privateDir(t))
	defer s.Close()
	m := Manifest{Version: 1, Entries: []Entry{{Path: "empty", Directory: true}}}
	p := begin(t, s, m)
	root, err := s.staging(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	root.Close()
	if err := s.root.Rename("staging/"+p.ID, "collections/"+p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Finish(testContext, p.ID); err == nil {
		t.Fatal("published incomplete recovered tree")
	}
}

func TestManifestCacheNeverCachesPublicationState(t *testing.T) {
	s := openStore(t, privateDir(t))
	defer s.Close()
	first := Manifest{Version: 1, Entries: []Entry{entry("first", []byte("a"))}}
	p := begin(t, s, first)
	if _, complete, err := s.manifest(testContext, p.ID, false); err != nil || complete {
		t.Fatal(complete, err)
	}
	write(t, s, p.ID, 0, 0, []byte("a"))
	if _, err := s.Finish(testContext, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, complete, err := s.manifest(testContext, p.ID, true); err != nil || !complete {
		t.Fatal("cached stale publication state", complete, err)
	}
	second := Manifest{Version: 1, Entries: []Entry{entry("second", []byte("b"))}}
	next := begin(t, s, second)
	if _, _, err := s.manifest(testContext, next.ID, false); err != nil {
		t.Fatal(err)
	}
	if s.cachedID != next.ID {
		t.Fatal("cache did not replace its single entry")
	}
	m, _, err := s.manifest(testContext, p.ID, true)
	if err != nil || m.Entries[0].Path != "first" {
		t.Fatal("wrong manifest after cache replacement", err)
	}
}
