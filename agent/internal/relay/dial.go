package relay

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Dial establishes one opaque stream through a relay. Both roles initiate an
// outbound connection, so neither peer needs an inbound NAT or firewall rule.
func Dial(ctx context.Context, relayOrigin, nodeID string, role Role, bearer string, tlsConfig *tls.Config) (net.Conn, error) {
	if !validRole(role) || !validNodeID(nodeID) || bearer == "" || strings.ContainsAny(bearer, "\r\n") {
		return nil, errors.New("invalid relay connection parameters")
	}
	u, err := parseOrigin(relayOrigin)
	if err != nil {
		return nil, err
	}
	host := u.Host
	if u.Port() == "" {
		if u.Scheme == "https" {
			host = net.JoinHostPort(u.Hostname(), "443")
		} else {
			host = net.JoinHostPort(u.Hostname(), "80")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, ErrUnavailable
	}
	conn := raw
	stopCancel := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer stopCancel()
	connected := false
	defer func() {
		if !connected {
			_ = conn.Close()
		}
	}()
	if u.Scheme == "https" {
		cfg := &tls.Config{MinVersion: tls.VersionTLS12}
		if tlsConfig != nil {
			cfg = tlsConfig.Clone()
			if cfg.MinVersion == 0 {
				cfg.MinVersion = tls.VersionTLS12
			}
		}
		if cfg.ServerName == "" {
			cfg.ServerName = u.Hostname()
		}
		tlsConn := tls.Client(raw, cfg)
		if err = tlsConn.HandshakeContext(ctx); err != nil {
			return nil, ErrUnavailable
		}
		conn = tlsConn
	}
	requestURL := &url.URL{Scheme: u.Scheme, Host: u.Host, Path: "/v1/relay/nodes/" + nodeID + "/" + string(role)}
	req := &http.Request{Method: http.MethodConnect, URL: requestURL, Host: u.Host, Header: make(http.Header)}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("User-Agent", "mesh-relay/1")
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err = req.Write(conn); err != nil {
		return nil, ErrUnavailable
	}
	bounded := &io.LimitedReader{R: conn, N: 16 * 1024}
	reader := bufio.NewReaderSize(bounded, 4096)
	response, readErr := http.ReadResponse(reader, req)
	if readErr != nil {
		return nil, ErrUnavailable
	}
	if response.Body != nil {
		_ = response.Body.Close()
	}
	if response.StatusCode != http.StatusOK {
		if role == RoleDevice && response.StatusCode == http.StatusRequestTimeout {
			return nil, errIdle
		}
		return nil, fmt.Errorf("%w: HTTP %d", ErrUnavailable, response.StatusCode)
	}
	bounded.N = math.MaxInt64
	_ = conn.SetDeadline(time.Time{})
	connected = true
	return &bufferedConn{Conn: conn, reader: reader}, nil
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func parseOrigin(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("relay must be an origin URL")
	}
	loopback := u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback()
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return nil, errors.New("relay HTTPS is required except on loopback")
	}
	return u, nil
}
