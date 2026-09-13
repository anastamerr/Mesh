package relay

import (
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
)

type relayMetrics struct {
	requestsTotal            atomic.Uint64
	rateLimitedTotal         atomic.Uint64
	unauthorizedTotal        atomic.Uint64
	authorizationErrorsTotal atomic.Uint64
	busyTotal                atomic.Uint64
	streamsStartedTotal      atomic.Uint64
	streamsCompletedTotal    atomic.Uint64
	deviceToConsumerBytes    atomic.Uint64
	consumerToDeviceBytes    atomic.Uint64
}

// MetricsHandler exposes bounded, credential-free Prometheus metrics. It is
// intended for a private monitoring network, not the public relay listener.
func (s *Server) MetricsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/metrics" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write([]byte(s.renderMetrics()))
	})
}

func (s *Server) renderMetrics() string {
	s.mu.Lock()
	waiting, active, connections := s.waitingCount, s.activeCount, len(s.connections)
	s.mu.Unlock()
	m := &s.metrics
	values := []struct {
		name  string
		help  string
		value uint64
	}{
		{"mesh_relay_connect_requests_total", "Valid CONNECT requests received by the relay.", m.requestsTotal.Load()},
		{"mesh_relay_rate_limited_total", "CONNECT requests rejected by a relay rate limit.", m.rateLimitedTotal.Load()},
		{"mesh_relay_unauthorized_total", "CONNECT requests denied by authentication.", m.unauthorizedTotal.Load()},
		{"mesh_relay_authorization_errors_total", "CONNECT requests rejected because authorization was unavailable.", m.authorizationErrorsTotal.Load()},
		{"mesh_relay_busy_total", "CONNECT requests rejected by relay capacity controls.", m.busyTotal.Load()},
		{"mesh_relay_streams_started_total", "Opaque streams successfully paired by the relay.", m.streamsStartedTotal.Load()},
		{"mesh_relay_streams_completed_total", "Opaque streams completed by the relay.", m.streamsCompletedTotal.Load()},
		{"mesh_relay_device_to_consumer_bytes_total", "Opaque bytes copied from devices to consumers.", m.deviceToConsumerBytes.Load()},
		{"mesh_relay_consumer_to_device_bytes_total", "Opaque bytes copied from consumers to devices.", m.consumerToDeviceBytes.Load()},
	}
	var out strings.Builder
	for _, value := range values {
		fmt.Fprintf(&out, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", value.name, value.help, value.name, value.name, value.value)
	}
	gauges := []struct {
		name  string
		help  string
		value int
	}{
		{"mesh_relay_waiting_connections", "Authenticated device connections waiting for a consumer.", waiting},
		{"mesh_relay_active_streams", "Currently active paired streams.", active},
		{"mesh_relay_tracked_connections", "Currently tracked hijacked connections.", connections},
		{"mesh_relay_waiting_capacity", "Configured maximum authenticated device waiters.", s.c.MaxWaiting},
		{"mesh_relay_active_capacity", "Configured maximum active paired streams.", s.c.MaxActive},
	}
	for _, gauge := range gauges {
		fmt.Fprintf(&out, "# HELP %s %s\n# TYPE %s gauge\n%s %d\n", gauge.name, gauge.help, gauge.name, gauge.name, gauge.value)
	}
	return out.String()
}
