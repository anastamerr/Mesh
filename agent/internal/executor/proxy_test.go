package executor

import (
	"bytes"
	"context"
	"net"
	"testing"
)

type proxyDialer struct{ peer net.Conn }

func (dialer proxyDialer) DialApplication(context.Context, string, uint64, int) (net.Conn, error) {
	client, server := net.Pipe()
	dialer.peer = server
	return client, nil
}

func TestProxyRejectsInvalidScopeBeforeDial(t *testing.T) {
	if err := Proxy(context.Background(), proxyDialer{}, "not-a-workload", 1, 8080,
		bytes.NewReader(nil), &bytes.Buffer{}); err == nil {
		t.Fatal("proxy accepted an invalid workload identity")
	}
}
