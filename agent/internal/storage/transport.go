package storage

import (
	"context"
	"crypto/sha256"
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
	pin, err := hex.DecodeString(fingerprint)
	if err != nil || len(pin) != sha256.Size {
		return errors.New("invalid paired device fingerprint")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = dial
	transport.TLSClientConfig = state.PinnedDeviceTLS(fingerprint)
	c.http.Transport = transport
	return nil
}

func (c *Client) CloseIdleConnections() { c.http.CloseIdleConnections() }
