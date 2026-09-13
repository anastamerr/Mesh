package storage

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"net"
	"net/http"

	"mesh.local/agent/internal/state"
)

// UseDeviceTransport verifies the key approved at pairing, independent of the
// device's changing address or the relay carrying the encrypted connection.
// Configure before the client begins a transfer. dial may provide a relay pipe.
func (c *Client) UseDeviceTransport(fingerprint string, dial func(context.Context, string, string) (net.Conn, error)) error {
	authenticate, err := DeviceAuthenticator(fingerprint)
	if err != nil {
		return err
	}
	return c.UseAuthenticatedDeviceTransport(func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return authenticate(ctx, conn)
	})
}

// DeviceAuthenticator completes TLS before a connection participates in path
// selection. A reachable service with the wrong key therefore cannot win a
// direct-versus-relay race and suppress the valid route.
func DeviceAuthenticator(fingerprint string) (func(context.Context, net.Conn) (net.Conn, error), error) {
	pin, err := hex.DecodeString(fingerprint)
	if err != nil || len(pin) != sha256.Size {
		return nil, errors.New("invalid paired device fingerprint")
	}
	config := state.PinnedDeviceTLS(fingerprint)
	return func(ctx context.Context, raw net.Conn) (net.Conn, error) {
		conn := tls.Client(raw, config.Clone())
		if err := conn.HandshakeContext(ctx); err != nil {
			_ = raw.Close()
			return nil, err
		}
		return conn, nil
	}, nil
}

// UseAuthenticatedDeviceTransport configures HTTPS over a dialer that already
// completed paired-device TLS authentication.
func (c *Client) UseAuthenticatedDeviceTransport(dial func(context.Context, string, string) (net.Conn, error)) error {
	if dial == nil {
		return errors.New("authenticated device dialer is required")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = nil
	transport.DialTLSContext = dial
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS13}
	c.http.Transport = transport
	return nil
}

func (c *Client) CloseIdleConnections() { c.http.CloseIdleConnections() }
