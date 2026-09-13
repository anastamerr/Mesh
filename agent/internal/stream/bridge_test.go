package stream

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestBridgeCopiesBothDirectionsAndCloses(t *testing.T) {
	for _, cancelConnection := range []bool{false, true} {
		t.Run(map[bool]string{false: "peer closes", true: "context cancelled"}[cancelConnection], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			left, client := net.Pipe()
			right, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			deadline := time.Now().Add(5 * time.Second)
			_ = client.SetDeadline(deadline)
			_ = server.SetDeadline(deadline)
			done := make(chan struct{})
			go func() { Bridge(ctx, left, right); close(done) }()
			for _, pair := range [][2]net.Conn{{client, server}, {server, client}} {
				written := make(chan error, 1)
				go func() { _, err := io.WriteString(pair[0], "mesh"); written <- err }()
				var data [4]byte
				if _, err := io.ReadFull(pair[1], data[:]); err != nil || string(data[:]) != "mesh" {
					t.Fatalf("copy: %q %v", data, err)
				}
				if err := <-written; err != nil {
					t.Fatal(err)
				}
			}
			if cancelConnection {
				cancel()
			} else {
				_ = client.Close()
			}
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("bridge did not release its connections and workers")
			}
			if _, err := server.Read(make([]byte, 1)); err != io.EOF {
				t.Fatalf("opposite connection was not closed: %v", err)
			}
		})
	}
}
