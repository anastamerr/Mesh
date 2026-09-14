package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"mesh.local/agent/internal/control"
	"mesh.local/agent/internal/storage"
)

func TestJobOutputPublisherImportsAndConfirmsCollection(t *testing.T) {
	nodeID := "12345678-1234-4234-8234-123456789abc"
	credential := "mesh_node_abcdefghijklmnopqrstuvwxyzABCDEFGH123456789"
	var confirmation struct {
		ID         string `json:"id"`
		FileCount  int    `json:"fileCount"`
		TotalBytes int64  `json:"totalBytes"`
	}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/nodes/"+nodeID+"/collections/confirm" ||
			request.Header.Get("Authorization") != "Bearer "+credential {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		if err := json.NewDecoder(request.Body).Decode(&confirmation); err != nil {
			http.Error(writer, "invalid", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"accepted":true}`))
	}))
	defer server.Close()
	client, err := control.New(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.Open(privateTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	source := filepath.Join(t.TempDir(), "output")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "result.txt"), []byte("done"), 0600); err != nil {
		t.Fatal(err)
	}
	publisher := &jobOutputPublisher{store: store, controller: client, nodeID: nodeID, credential: credential}
	id, err := publisher.Publish(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if confirmation.ID != id || confirmation.FileCount != 1 || confirmation.TotalBytes != 4 {
		t.Fatalf("wrong confirmation: %#v", confirmation)
	}
}
