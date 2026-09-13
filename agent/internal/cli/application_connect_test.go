package cli

import (
	"crypto/tls"
	"crypto/x509"
	"strings"
	"testing"

	"mesh.local/agent/internal/state"
)

func TestApplicationTLSVerifiesPairedDeviceKey(t *testing.T) {
	store, err := state.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	certificate, fingerprint, err := store.DeviceCertificate()
	if err != nil {
		t.Fatal(err)
	}
	connection := tls.ConnectionState{PeerCertificates: []*x509.Certificate{certificate.Leaf}}
	if err := state.PinnedDeviceTLS(fingerprint).VerifyConnection(connection); err != nil {
		t.Fatal("paired device key was rejected", err)
	}
	if err := state.PinnedDeviceTLS(strings.Repeat("0", 64)).VerifyConnection(connection); err == nil {
		t.Fatal("different device key was accepted")
	}
}
