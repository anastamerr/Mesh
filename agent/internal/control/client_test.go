package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHeartbeatPublishesDirectCandidatesWithoutChangingLegacyShape(t *testing.T) {
	var bodies []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
		_, _ = io.WriteString(w, `{"accepted":true}`)
	}))
	defer server.Close()
	client, _ := New(server.URL)
	inventory := Inventory{CPULogicalCores: 4, MemoryTotalBytes: 8, MemoryAvailableBytes: 4}
	if err := client.Heartbeat(context.Background(), "node", "credential", 0, inventory); err != nil {
		t.Fatal(err)
	}
	if _, exists := bodies[0]["directCandidates"]; exists {
		t.Fatal("legacy heartbeat unexpectedly published candidate field")
	}
	if err := client.HeartbeatWithCandidates(context.Background(), "node", "credential", 1, inventory,
		[]DirectCandidate{{Transport: "tcp", Host: "192.168.1.20", Port: 7332}}); err != nil {
		t.Fatal(err)
	}
	if candidates, ok := bodies[1]["directCandidates"].([]any); !ok || len(candidates) != 1 {
		t.Fatalf("missing candidates: %#v", bodies[1])
	}
}

func TestDeviceConnectionValidatesControllerResponse(t *testing.T) {
	id := "12345678-1234-4234-8234-123456789abc"
	fingerprint := strings.Repeat("a", 64)
	for _, body := range []string{
		fmt.Sprintf(`{"nodeId":%q,"publicKeyFingerprint":%q,"directCandidates":[{"transport":"tcp","host":"192.168.1.20","port":7332}],"candidatesObservedAt":"2026-09-08T00:00:00Z"}`, id, fingerprint),
		fmt.Sprintf(`{"nodeId":%q,"publicKeyFingerprint":%q,"directCandidates":[{"transport":"udp","host":"192.168.1.20","port":7332}]}`, id, fingerprint),
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, body) }))
		client, _ := New(server.URL)
		connection, err := client.DeviceConnection(context.Background(), "operator", id)
		server.Close()
		if strings.Contains(body, `"tcp"`) {
			if err != nil || len(connection.DirectCandidates) != 1 {
				t.Fatal(connection, err)
			}
		} else if !errors.Is(err, ErrProtocol) {
			t.Fatal("invalid candidate accepted", err)
		}
	}
}

func TestOriginPolicy(t *testing.T) {
	for _, origin := range []string{"http://example.com", "https://user:secret@example.com", "https://example.com/api", "https://example.com?token=x", "https://example.com?", "https://example.com/%2f", "file:///tmp/x"} {
		if _, err := New(origin); err == nil {
			t.Fatalf("accepted unsafe origin %q", origin)
		}
	}
	for _, origin := range []string{"http://127.0.0.1:3000", "http://[::1]:3000", "https://example.com"} {
		if _, err := New(origin); err != nil {
			t.Fatalf("rejected valid origin: %v", err)
		}
	}
}

func TestHeartbeatRequiresValidAcknowledgement(t *testing.T) {
	for _, body := range []string{`{}`, `{"accepted":false}`, `{"accepted":"true"}`, `<html>wrong service</html>`, strings.Repeat("x", maxResponseBytes+1)} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(body))
		}))
		client, _ := New(server.URL)
		err := client.Heartbeat(context.Background(), "node", "secret", 0, Inventory{})
		server.Close()
		if !errors.Is(err, ErrProtocol) || !Permanent(err) {
			t.Fatalf("invalid acknowledgement was accepted: %v", err)
		}
	}
}

func TestTransientResponseReusesConnection(t *testing.T) {
	var connections atomic.Int32
	var requests atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(503)
			w.Write([]byte(`{"message":"temporarily unavailable"}`))
			return
		}
		w.Write([]byte(`{"accepted":true}`))
	}))
	server.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			connections.Add(1)
		}
	}
	server.Start()
	defer server.Close()
	client, _ := New(server.URL)
	if err := client.Heartbeat(context.Background(), "node", "secret", 0, Inventory{}); err == nil || Permanent(err) {
		t.Fatalf("expected retryable failure: %v", err)
	}
	if err := client.Heartbeat(context.Background(), "node", "secret", 1, Inventory{}); err != nil {
		t.Fatal(err)
	}
	if connections.Load() != 1 {
		t.Fatal("error response unnecessarily discarded keep-alive connection")
	}
}

func TestRedirectDoesNotForwardCredential(t *testing.T) {
	called := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	client, _ := New(source.URL)
	err := client.Heartbeat(context.Background(), "node", "secret", 0, Inventory{})
	var api *APIError
	if !errors.As(err, &api) || api.Status != 307 || called {
		t.Fatalf("redirect leaked request or did not fail: %v", err)
	}
}

func TestResponseBodyIsNotReflectedInErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte("sensitive reflected credential"))
	}))
	defer server.Close()
	client, _ := New(server.URL)
	err := client.Heartbeat(context.Background(), "node", "secret", 0, Inventory{})
	if err == nil || err.Error() != "control plane returned HTTP 401" || !Permanent(err) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestStorageAuthorizationRequiresPositiveAcknowledgement(t *testing.T) {
	for _, body := range []string{`{}`, `{"accepted":false}`, `invalid`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer node-credential" {
				t.Error("missing node authentication")
			}
			_, _ = io.WriteString(w, body)
		}))
		client, err := New(server.URL)
		if err != nil {
			t.Fatal(err)
		}
		if err = client.AuthorizeStorage(context.Background(), "node", "node-credential", "token", "list", nil); !errors.Is(err, ErrProtocol) {
			t.Fatal("accepted malformed authorization", err)
		}
		server.Close()
	}
}
