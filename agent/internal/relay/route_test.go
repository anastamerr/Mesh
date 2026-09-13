package relay

import (
	"context"
	"io"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRelayNeverMatchesDifferentApplicationRoutes(t *testing.T) {
	server, err := NewServer(ServerConfig{Authorizer: staticRelayAuthorizer{}, RevalidateInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	routeA := "app-12345678-1234-4234-8234-123456789abc"
	routeB := "app-22345678-1234-4234-8234-123456789abc"
	deviceResult := make(chan struct {
		connection io.ReadWriteCloser
		err        error
	}, 1)
	go func() {
		connection, dialError := DialRoute(ctx, httpServer.URL, "node-1", routeA, RoleDevice, "device-ticket", nil)
		deviceResult <- struct {
			connection io.ReadWriteCloser
			err        error
		}{connection, dialError}
	}()
	for {
		server.mu.Lock()
		waiting := server.waitingCount
		server.mu.Unlock()
		if waiting == 1 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("device route did not enter relay queue")
		case <-time.After(time.Millisecond):
		}
	}
	if connection, err := DialRoute(ctx, httpServer.URL, "node-1", routeB, RoleConsumer, "consumer-ticket", nil); err == nil {
		connection.Close()
		t.Fatal("consumer connected to a different application route")
	}
	consumer, err := DialRoute(ctx, httpServer.URL, "node-1", routeA, RoleConsumer, "consumer-ticket", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	device := <-deviceResult
	if device.err != nil {
		t.Fatal(device.err)
	}
	defer device.connection.Close()
	if _, err = consumer.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 1)
	if _, err = io.ReadFull(device.connection, data); err != nil || string(data) != "x" {
		t.Fatalf("matching application route did not bridge: %q %v", data, err)
	}
}
