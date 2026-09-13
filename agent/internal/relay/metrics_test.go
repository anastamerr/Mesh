package relay

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type metricsAuthorizer struct{}

func (metricsAuthorizer) Authorize(_ context.Context, req AuthRequest) (Lease, error) {
	return Lease{Subject: string(req.Role) + ":" + req.NodeID, ExpiresAt: time.Now().Add(time.Minute)}, nil
}

func (metricsAuthorizer) Revalidate(_ context.Context, req AuthRequest, _ Lease) (Lease, error) {
	return Lease{Subject: string(req.Role) + ":" + req.NodeID, ExpiresAt: time.Now().Add(time.Minute)}, nil
}

func TestConnectRateLimitAndMetrics(t *testing.T) {
	server, err := NewServer(ServerConfig{
		Authorizer: metricsAuthorizer{}, MaxConnectsPerMinute: 1, MaxConnectsPerNodePerMinute: 1,
		ConnectBurst: 1, ConnectBurstPerNode: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	first := httptest.NewRecorder()
	server.ServeHTTP(first, httptest.NewRequest(http.MethodConnect, "/v1/relay/nodes/node-1/consumer", nil))
	if first.Code != http.StatusUnauthorized {
		t.Fatalf("first status = %d", first.Code)
	}
	second := httptest.NewRecorder()
	server.ServeHTTP(second, httptest.NewRequest(http.MethodConnect, "/v1/relay/nodes/node-1/consumer", nil))
	if second.Code != http.StatusTooManyRequests || second.Header().Get("Retry-After") == "" {
		t.Fatalf("second status = %d, retry-after = %q", second.Code, second.Header().Get("Retry-After"))
	}

	recorder := httptest.NewRecorder()
	server.MetricsHandler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, err := io.ReadAll(recorder.Result().Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, expected := range []string{"mesh_relay_connect_requests_total 2", "mesh_relay_rate_limited_total 1", "mesh_relay_unauthorized_total 1"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("metrics missing %q:\n%s", expected, text)
		}
	}
}
