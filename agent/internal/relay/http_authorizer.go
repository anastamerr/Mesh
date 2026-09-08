package relay

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

type HTTPAuthorizer struct {
	endpoint      string
	serviceBearer string
	client        *http.Client
}

const maxAuthorizationResponse = 16 * 1024

func NewHTTPAuthorizer(endpoint, serviceBearer string, tlsConfig *tls.Config) (*HTTPAuthorizer, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("authorization endpoint must be an HTTP origin and path")
	}
	loopback := u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback()
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return nil, errors.New("authorization endpoint must use HTTPS except on loopback")
	}
	if serviceBearer == "" || strings.ContainsAny(serviceBearer, "\r\n") {
		return nil, errors.New("authorization service credential is required")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if tlsConfig != nil {
		transport.TLSClientConfig = tlsConfig.Clone()
	}
	return &HTTPAuthorizer{endpoint: u.String(), serviceBearer: serviceBearer, client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (a *HTTPAuthorizer) Authorize(ctx context.Context, req AuthRequest) (Lease, error) {
	return a.request(ctx, req)
}

func (a *HTTPAuthorizer) Revalidate(ctx context.Context, req AuthRequest, _ Lease) (Lease, error) {
	return a.request(ctx, req)
}

func (a *HTTPAuthorizer) request(ctx context.Context, input AuthRequest) (Lease, error) {
	var lease Lease
	payload, err := json.Marshal(struct {
		Role   Role   `json:"role"`
		NodeID string `json:"nodeId"`
		Token  string `json:"token"`
	}{input.Role, input.NodeID, input.Bearer})
	if err != nil {
		return lease, ErrUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint, bytes.NewReader(payload))
	if err != nil {
		return lease, ErrUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+a.serviceBearer)
	req.Header.Set("Content-Type", "application/json")
	res, err := a.client.Do(req)
	if err != nil {
		return lease, ErrUnavailable
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 16*1024))
		return lease, ErrDenied
	}
	if res.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 16*1024))
		return lease, ErrUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxAuthorizationResponse+1))
	if err != nil || len(body) > maxAuthorizationResponse {
		return Lease{}, ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&lease) != nil || lease.Subject == "" || lease.ExpiresAt.IsZero() {
		return Lease{}, ErrUnavailable
	}
	// A successful response is one complete JSON value. Accepting a valid
	// prefix would allow an oversized or otherwise malformed response to pass
	// authorization validation.
	if decoder.Decode(&struct{}{}) != io.EOF {
		return Lease{}, ErrUnavailable
	}
	return lease, nil
}
