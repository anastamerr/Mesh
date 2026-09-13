package storage

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestImportPublishesVerifiedLocalOutputIdempotently(t *testing.T) {
	source := filepath.Join(t.TempDir(), "output")
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "result.txt"), []byte("converted"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	id, manifest, err := store.Import(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if repeat, _, err := store.Import(context.Background(), source); err != nil || repeat != id {
		t.Fatalf("repeated import was not idempotent: %q %v", repeat, err)
	}
	if count, total := manifest.Statistics(); count != 1 || total != 9 {
		t.Fatalf("wrong imported statistics: %d %d", count, total)
	}
	file, entry, err := store.Read(context.Background(), id, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil || string(data) != "converted" || entry.Path != "nested/result.txt" {
		t.Fatalf("wrong imported output: %q %#v %v", data, entry, err)
	}
}
