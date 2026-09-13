package cli

import (
	"io"
	"testing"
)

func TestComputeRequiresStorageRootAndValidDistribution(t *testing.T) {
	if _, err := parseOptions([]string{"run", "--compute"}, io.Discard); err == nil {
		t.Fatal("compute accepted without an approved storage root")
	}
	if _, err := parseOptions([]string{"run", "--compute", "--root", t.TempDir(),
		"--wsl-distribution", "bad/name"}, io.Discard); err == nil {
		t.Fatal("unsafe WSL distribution name accepted")
	}
	if _, err := parseOptions([]string{"run", "--compute", "--root", t.TempDir(),
		"--listen", "127.0.0.1:0"}, io.Discard); err != nil {
		t.Fatal("valid compute options rejected", err)
	}
}
