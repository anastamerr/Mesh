package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeviceTransportAuthenticatesBeforeHTTP(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = io.WriteString(w, `[]`)
	}))
	defer server.Close()
	hash := sha256.Sum256(server.Certificate().RawSubjectPublicKeyInfo)
	client, err := NewClient(server.URL, strings.Repeat("a", 43))
	if err != nil {
		t.Fatal(err)
	}
	if err = client.UseDeviceTransport(hex.EncodeToString(hash[:]), func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}); err != nil {
		t.Fatal(err)
	}
	if _, err = client.List(context.Background(), ""); err != nil || requests != 1 {
		t.Fatal("authenticated request failed", err, requests)
	}
}

func TestDeviceTransportRejectsWrongIdentityBeforeSendingCredential(t *testing.T) {
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
	defer server.Close()
	client, _ := NewClient(server.URL, strings.Repeat("a", 43))
	if err := client.UseDeviceTransport(strings.Repeat("0", 64), func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.List(context.Background(), ""); err == nil || requests != 0 {
		t.Fatal("wrong identity reached HTTP handler", err, requests)
	}
}
