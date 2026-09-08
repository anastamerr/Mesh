package storage

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"time"
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
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS13,
		// Pairing pins the public key instead of a DNS name or public CA chain.
		InsecureSkipVerify: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) != 1 {
				return errors.New("unexpected device certificate chain")
			}
			cert := cs.PeerCertificates[0]
			hash := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
			if hex.EncodeToString(hash[:]) != fingerprint || time.Now().Before(cert.NotBefore) || time.Now().After(cert.NotAfter) {
				return errors.New("device identity does not match pairing")
			}
			return nil
		}}
	c.http.Transport = transport
	return nil
}

func (c *Client) CloseIdleConnections() { c.http.CloseIdleConnections() }
