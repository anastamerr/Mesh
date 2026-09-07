package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"mesh.local/agent/internal/control"
)

func TestGetExactIDWinsOverNamesAndStopsPagination(t *testing.T) {
	const target = "0000000000000000000000000000000000000000000000000000000000000003"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		entries := make([]control.Collection, 100)
		for i := range entries {
			entries[i] = control.Collection{ID: fmt.Sprintf("%064x", i+1), Name: "copy"}
		}
		// Two earlier display names must not make the actual ID ambiguous.
		entries[0].Name, entries[1].Name = target, target
		json.NewEncoder(w).Encode(entries)
	}))
	defer server.Close()
	client, err := control.New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	err = getManaged(context.Background(), client, "operator", "node", managedOptions{collection: target}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "copy is not confirmed yet") {
		t.Fatalf("did not resolve the exact pending collection: %v", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("looked past the exact match: %d requests", requests.Load())
	}
}

func TestGetNameRemainsAmbiguousAcrossPages(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page := requests.Add(1)
		entries := make([]control.Collection, 100)
		if page > 1 {
			entries = entries[:1]
		}
		for i := range entries {
			entries[i] = control.Collection{ID: fmt.Sprintf("%064x", int(page-1)*100+i+1), Name: "other"}
		}
		entries[0].Name = "duplicate"
		json.NewEncoder(w).Encode(entries)
	}))
	defer server.Close()
	client, err := control.New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	err = getManaged(context.Background(), client, "operator", "node", managedOptions{collection: "duplicate"}, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "collection name is ambiguous") || requests.Load() != 2 {
		t.Fatalf("ambiguous name incorrectly resolved: %v (%d requests)", err, requests.Load())
	}
}
