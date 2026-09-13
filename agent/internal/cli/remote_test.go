package cli

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"mesh.local/agent/internal/control"
	"mesh.local/agent/internal/state"
	"mesh.local/agent/internal/storage"
)

func TestConfigureMeshClientUsesAuthenticatedDirectCandidate(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := state.Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	certificate, fingerprint, err := store.DeviceCertificate()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	requests := 0
	storageServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("g", 43) {
			t.Error("storage credential missing")
		}
		requests++
		_, _ = io.WriteString(w, `[]`)
	})}
	go storageServer.Serve(tls.NewListener(listener, &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}}))
	t.Cleanup(func() { _ = storageServer.Close() })
	nodeID := "12345678-1234-4234-8234-123456789abc"
	controllerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/nodes/"+nodeID+"/connection" {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprintf(w, `{"nodeId":%q,"publicKeyFingerprint":%q,"directCandidates":[{"transport":"tcp","host":"127.0.0.1","port":%s}],"candidatesObservedAt":"2026-09-08T00:00:00Z"}`,
			nodeID, fingerprint, strconv.Itoa(port))
	}))
	defer controllerServer.Close()
	controller, _ := control.New(controllerServer.URL)
	client, _ := storage.NewClient("https://mesh-device.invalid", strings.Repeat("g", 43))
	var logs bytes.Buffer
	if err := configureMeshClient(context.Background(), client, controller, "operator", nodeID, "", "", false,
		func(context.Context) (string, error) { return strings.Repeat("g", 43), nil }, &logs); err != nil {
		t.Fatal(err)
	}
	if _, err := client.List(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || !strings.Contains(logs.String(), "Connected directly") {
		t.Fatal(requests, logs.String())
	}
}
