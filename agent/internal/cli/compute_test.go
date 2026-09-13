package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadBundleDigestAcceptsSha256sumSidecar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bundle.sha256")
	expected := strings.Repeat("a", 64)
	if err := os.WriteFile(path, []byte(expected+"  mesh-wsl-rootfs-amd64.tar\n"), 0600); err != nil {
		t.Fatal(err)
	}
	digest, err := readBundleDigest(path)
	if err != nil || digest != expected {
		t.Fatalf("valid digest sidecar rejected: %q %v", digest, err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("z", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readBundleDigest(path); err == nil {
		t.Fatal("invalid digest accepted")
	}
}
