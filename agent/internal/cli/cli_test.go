package cli

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
	"testing"
	"time"
)

func TestEnrollmentAndStatusNeverPrintCredential(t *testing.T) {
	credential := "mesh_node_" + strings.Repeat("a", 43)
	enrollments := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enrollments++
		fmt.Fprintf(w, `{"node":{"id":"12345678-1234-4234-8234-123456789abc"},"nodeCredential":%q,"credentialExpiresAt":%q}`,
			credential, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	}))
	defer server.Close()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	args := []string{"enroll", "--server", server.URL, "--state-dir", dir, "--name", "Lenovo", "--token-stdin"}
	var output bytes.Buffer
	if err := Execute(context.Background(), args, strings.NewReader("mesh_enroll_"+strings.Repeat("b", 43)), &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := Execute(context.Background(), []string{"status", "--state-dir", dir}, nil, &output, io.Discard); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), credential) {
		t.Fatal("credential leaked into output")
	}
	if err := Execute(context.Background(), args, nil, &output, io.Discard); err == nil || enrollments != 1 {
		t.Fatal("existing identity was re-enrolled")
	}
}

func TestInvalidCommandsDoNotCreateState(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-created")
	for _, args := range [][]string{
		{"enroll", "--state-dir", dir},
		{"enroll", "--state-dir", dir, "--token-stdin", "--server", "http://remote.example"},
		{"run", "--state-dir", dir, "--interval", "0s"},
		{"run", "--state-dir", dir, "--root", "files", "--listen", "0.0.0.0:7332"},
		{"run", "--state-dir", dir, "--listen", "127.0.0.1:7332"},
		{"run", "--state-dir", dir, "--root", "files", "--tls-cert", "missing"},
	} {
		if err := Execute(context.Background(), args, nil, io.Discard, io.Discard); err == nil {
			t.Fatal("invalid command accepted")
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("invalid command created state")
		}
	}
}

func TestInfoHelpDoesNotReadHardwareOrState(t *testing.T) {
	var help bytes.Buffer
	if err := Execute(context.Background(), []string{"info", "--help"}, nil, io.Discard, &help); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(help.String(), "Usage of info") {
		t.Fatal("missing help")
	}
}
