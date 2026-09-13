package connectivity

import (
	"net"
	"testing"
)

func TestCandidatesForIPsPublishesOnlyUniquePrivateIPv4(t *testing.T) {
	candidates := candidatesForIPs([]net.IP{
		net.ParseIP("192.168.1.20"), net.ParseIP("10.0.0.8"), net.ParseIP("172.16.0.1"),
		net.ParseIP("127.0.0.1"), net.ParseIP("169.254.1.2"), net.ParseIP("8.8.8.8"),
		net.ParseIP("192.168.1.20"), net.ParseIP("fd00::1"),
	}, 7332)
	if len(candidates) != 3 || candidates[0].Host != "10.0.0.8" || candidates[1].Host != "172.16.0.1" || candidates[2].Host != "192.168.1.20" {
		t.Fatalf("unexpected candidates: %#v", candidates)
	}
}

func TestDiscoverLANRejectsUnsafeOrUnusableListeners(t *testing.T) {
	for _, listen := range []string{"127.0.0.1:7332", "8.8.8.8:7332", "0.0.0.0:80", "bad"} {
		if candidates, err := DiscoverLAN(listen); err == nil {
			t.Fatalf("accepted %q: %#v", listen, candidates)
		}
	}
	candidates, err := DiscoverLAN("192.168.2.4:7332")
	if err != nil || len(candidates) != 1 || candidates[0].Host != "192.168.2.4" {
		t.Fatal(candidates, err)
	}
}
