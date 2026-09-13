package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"mesh.local/agent/internal/control"
)

func TestSetupPairsInitializesStorageAndResumes(t *testing.T) {
	var pairing control.PairingRequest
	starts := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/v1/pairing-challenges":
			starts++
			if err := json.NewDecoder(request.Body).Decode(&pairing); err != nil {
				t.Error(err)
			}
			fmt.Fprintf(writer, `{"id":%q,"code":"ABCD-1234","name":%q,"publicKeyFingerprint":%q,"expiresAt":%q}`,
				pairing.ID, pairing.Name, pairing.PublicKeyFingerprint, time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano))
		case request.Method == http.MethodGet && request.URL.Path == "/v1/pairing-challenges/"+pairing.ID:
			fmt.Fprintf(writer, `{"status":"approved","node":{"id":"12345678-1234-4234-8234-123456789abc","name":%q,"publicKeyFingerprint":%q},"credentialExpiresAt":%q}`,
				pairing.Name, pairing.PublicKeyFingerprint, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano))
		case request.Method == http.MethodGet && request.URL.Path == "/v1/network":
			fmt.Fprint(writer, `{"relayOrigin":""}`)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	stateDirectory, root := t.TempDir(), t.TempDir()
	caPath := filepath.Join(t.TempDir(), "relay-ca.pem")
	arguments := []string{"setup", "--server", server.URL, "--name", "Spare laptop",
		"--state-dir", stateDirectory, "--root", root, "--direct-lan", "--relay-ca", caPath}
	var output bytes.Buffer
	if err := Execute(context.Background(), arguments, strings.NewReader(""), &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Setup complete") || starts != 1 {
		t.Fatalf("setup did not complete one pairing: starts=%d output=%q", starts, output.String())
	}
	output.Reset()
	if err := Execute(context.Background(), arguments, strings.NewReader(""), &output, io.Discard); err != nil {
		t.Fatal("resumable setup failed", err)
	}
	if starts != 1 || !strings.Contains(output.String(), "Setup complete") || !strings.Contains(output.String(), "--direct-lan") ||
		!strings.Contains(output.String(), fmt.Sprintf("--relay-ca %q", caPath)) {
		t.Fatalf("setup repeated pairing or omitted completion: starts=%d output=%q", starts, output.String())
	}
}

func TestSetupRejectsCaseAliasedWindowsDirectories(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows paths compare without case")
	}
	root := t.TempDir()
	if !pathsOverlap(root, strings.ToUpper(root)) {
		t.Fatal("identity and storage can alias the same Windows directory")
	}
}

func TestSetupRejectsOverlappingIdentityAndStorage(t *testing.T) {
	root := t.TempDir()
	err := Execute(context.Background(), []string{"setup", "--server", "http://127.0.0.1:1", "--name", "node",
		"--state-dir", root, "--root", root}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil {
		t.Fatal("setup accepted overlapping identity and storage directories")
	}
}
