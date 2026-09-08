package relay

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type staticRelayAuthorizer struct{}

func (staticRelayAuthorizer) Authorize(_ context.Context, req AuthRequest) (Lease, error) {
	return Lease{Subject: string(req.Role) + ":" + req.NodeID, ExpiresAt: time.Now().Add(time.Hour)}, nil
}

func (staticRelayAuthorizer) Revalidate(_ context.Context, req AuthRequest, _ Lease) (Lease, error) {
	return Lease{Subject: string(req.Role) + ":" + req.NodeID, ExpiresAt: time.Now().Add(time.Hour)}, nil
}

func TestIdleTimeoutTracksEitherDirection(t *testing.T) {
	relayServer, err := NewServer(ServerConfig{
		Authorizer:         staticRelayAuthorizer{},
		IdleTimeout:        100 * time.Millisecond,
		RevalidateInterval: time.Hour,
		MaxLifetime:        5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewUnstartedServer(relayServer)
	httpServer.StartTLS()
	defer httpServer.Close()
	defer relayServer.Close()
	transport := httpServer.Client().Transport.(*http.Transport)
	tlsConfig := transport.TLSClientConfig.Clone()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	deviceResult := make(chan struct {
		conn net.Conn
		err  error
	}, 1)
	go func() {
		conn, dialErr := Dial(ctx, httpServer.URL, "node-1", RoleDevice, "device-ticket", tlsConfig)
		deviceResult <- struct {
			conn net.Conn
			err  error
		}{conn, dialErr}
	}()

	var consumer net.Conn
	for consumer == nil && ctx.Err() == nil {
		consumer, err = Dial(ctx, httpServer.URL, "node-1", RoleConsumer, "consumer-ticket", tlsConfig)
		if err != nil {
			time.Sleep(time.Millisecond)
		}
	}
	if consumer == nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	deviceOutcome := <-deviceResult
	if deviceOutcome.err != nil {
		t.Fatal(deviceOutcome.err)
	}
	device := deviceOutcome.conn
	defer device.Close()

	const (
		chunks    = 60
		chunkSize = 32
	)
	readDone := make(chan error, 1)
	go func() {
		buf := make([]byte, chunkSize)
		for i := 0; i < chunks; i++ {
			if _, readErr := io.ReadFull(device, buf); readErr != nil {
				readDone <- readErr
				return
			}
		}
		readDone <- nil
	}()
	payload := make([]byte, chunkSize)
	for i := 0; i < chunks; i++ {
		if _, err = consumer.Write(payload); err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = <-readDone; err != nil {
		t.Fatalf("one-way stream expired while traffic was active: %v", err)
	}
}
