package state

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validState() State {
	return State{Version: 1, Server: "https://example.com", NodeID: "12345678-1234-4234-8234-123456789abc",
		Credential: "mesh_node_" + strings.Repeat("a", 43), ExpiresAt: time.Now().Add(time.Hour)}
}

func TestMissingOrNullSequenceIsRejectedWithoutReset(t *testing.T) {
	for _, missing := range []bool{true, false} {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0700); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(validState())
		var document map[string]any
		if err := json.Unmarshal(data, &document); err != nil {
			t.Fatal(err)
		}
		if missing {
			delete(document, "nextSequence")
		} else {
			document["nextSequence"] = nil
		}
		data, _ = json.Marshal(document)
		data, err := protect(data)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "state.json")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		store, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Load(); err == nil {
			t.Fatal("missing/null sequence accepted as zero")
		}
		store.Close()
		after, _ := os.ReadFile(path)
		if !bytes.Equal(data, after) {
			t.Fatal("damaged state was changed")
		}
	}
}

func TestStateSurvivesReopenAndExcludesConcurrentWriter(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := Open(dir); err == nil {
		other.Close()
		t.Fatal("second writer acquired lock")
	}
	saved := validState()
	saved.NextSequence = 12
	if err := store.Save(saved); err != nil {
		t.Fatal(err)
	}
	store.Close()
	store, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	actual, err := store.Load()
	if err != nil || actual.NextSequence != 12 || actual.Credential != saved.Credential {
		t.Fatalf("state did not survive reopen: %v", err)
	}
	actual.NextSequence++
	if err := store.Save(actual); err != nil {
		t.Fatalf("atomic replacement failed: %v", err)
	}
}

func TestCorruptStateIsNotReset(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Load(); err == nil {
		t.Fatal("corruption was accepted")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "state.json"))
	if string(data) != "corrupt" {
		t.Fatal("corrupt state was silently overwritten")
	}
}
