package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStorageKeyLifecycle(t *testing.T) {
	file := filepath.Join(t.TempDir(), "storage.key")
	var output bytes.Buffer
	args := []string{"storage", "keygen", "--key-file", file}
	if err := Execute(context.Background(), args, nil, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	key, err := readStorageKey(file)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), key) {
		t.Fatal("key printed")
	}
	if err := Execute(context.Background(), args, nil, &output, io.Discard); err == nil {
		t.Fatal("existing key replaced")
	}
	got, err := readStorageKey(file)
	if err != nil || got != key {
		t.Fatal("key changed", err)
	}
}
func TestStorageValidationPrecedesSideEffects(t *testing.T) {
	root := filepath.Join(t.TempDir(), "not-created")
	for _, args := range [][]string{
		{"storage", "serve", "--root", root, "--enrolled", "--key-file", "missing"},
		{"storage", "serve", "--root", root, "--listen", "0.0.0.0:7332", "--tls-cert", "missing", "--tls-key", "missing", "--key-file", "missing"},
		{"storage", "serve", "--root", root, "--listen", "0.0.0.0:7332", "--key-file", "missing"},
		{"storage", "serve", "--root", root, "--tls-cert", "missing", "--key-file", "missing"},
		{"storage", "serve", "--root", root},
		{"storage", "upload", "--key-file", "missing"},
		{"storage", "download", "--destination", root, "--key-file", "missing"},
	} {
		if err := Execute(context.Background(), args, nil, io.Discard, io.Discard); err == nil {
			t.Fatal("invalid options accepted")
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatal("invalid command created root")
		}
	}
}
