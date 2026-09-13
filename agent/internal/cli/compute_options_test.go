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

func TestComputeAndDirectLANOptionsCoexist(t *testing.T) {
	o, err := parseOptions([]string{"run", "--compute", "--direct-lan", "--root", t.TempDir(),
		"--listen", "192.168.1.20:7332", "--wsl-distribution", "Mesh"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if !o.compute || !o.directLAN || !o.storage.pairedTLS || o.wsl != "Mesh" {
		t.Fatal("combined runner lost compute or paired LAN configuration")
	}
}
