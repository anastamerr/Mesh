package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mesh.local/agent/internal/storage"
)

// The helper runs the real CLI lifecycle in a separate process so Kill exercises
// SQLite recovery and OS lock release, not the orderly Store.Close path.
func TestStorageProcessHelper(t *testing.T) {
	if os.Getenv("MESH_STORAGE_TEST_HELPER") != "1" {
		return
	}
	separator := 0
	for i, arg := range os.Args {
		if arg == "--" {
			separator = i + 1
			break
		}
	}
	if separator == 0 {
		os.Exit(2)
	}
	if err := Execute(context.Background(), os.Args[separator:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}
func startStorageProcess(t *testing.T, root, keyFile string) (string, func()) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestStorageProcessHelper$", "--", "storage", "serve", "--root", root, "--key-file", keyFile, "--listen", "127.0.0.1:0")
	cmd.Env = append(os.Environ(), "MESH_STORAGE_TEST_HELPER=1")
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if !stopped {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			stopped = true
		}
	}
	t.Cleanup(stop)
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			if address, ok := strings.CutPrefix(scanner.Text(), "Storage listening on "); ok {
				ready <- "http://" + address
				return
			}
		}
		ready <- ""
	}()
	select {
	case address := <-ready:
		if address == "" {
			t.Fatal("storage process exited before ready")
		}
		return address, stop
	case <-time.After(30 * time.Second):
		t.Fatal("storage process did not become ready")
	}
	return "", stop
}
func TestStorageCLIResumesAfterProcessKill(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	base := t.TempDir()
	keyFile := filepath.Join(base, "key")
	if err := generateStorageKey(keyFile); err != nil {
		t.Fatal(err)
	}
	key, err := readStorageKey(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(base, "source")
	if err = os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat([]byte("native storage\n"), 4096)
	if err = os.WriteFile(filepath.Join(source, "file"), content, 0600); err != nil {
		t.Fatal(err)
	}
	sourceRoot, err := os.OpenRoot(source)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := storage.Scan(ctx, sourceRoot)
	sourceRoot.Close()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	storageRoot := filepath.Join(base, "storage")
	server, stop := startStorageProcess(t, storageRoot, keyFile)
	request := func(method, path string, body []byte) *http.Response {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, server+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Upload-Offset", "0")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != 200 {
			res.Body.Close()
			t.Fatal(res.StatusCode)
		}
		return res
	}
	res := request("POST", "/v1/collections", data)
	var progress storage.Progress
	err = json.NewDecoder(res.Body).Decode(&progress)
	res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	res = request("PUT", "/v1/collections/"+progress.ID+"/files/0", content[:1234])
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	stop()
	server, _ = startStorageProcess(t, storageRoot, keyFile)
	var output bytes.Buffer
	if err = Execute(ctx, []string{"storage", "upload", "--key-file", keyFile, "--server", server, "--source", source}, nil, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output.String()) != progress.ID {
		t.Fatal("collection changed after restart")
	}
	destination := filepath.Join(base, "download")
	if err = Execute(ctx, []string{"storage", "download", "--key-file", keyFile, "--server", server, "--id", progress.ID, "--destination", destination}, nil, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(destination, "file"))
	if err != nil || !bytes.Equal(got, content) {
		t.Fatal("download mismatch", err)
	}
}
