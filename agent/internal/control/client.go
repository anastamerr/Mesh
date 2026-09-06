// Package control implements the versioned control-plane HTTP contract.
package control

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Inventory struct {
	CPULogicalCores      int    `json:"cpuLogicalCores"`
	MemoryTotalBytes     uint64 `json:"memoryTotalBytes"`
	MemoryAvailableBytes uint64 `json:"memoryAvailableBytes"`
}

type Enrollment struct {
	Token        string `json:"enrollmentToken"`
	Name         string `json:"name"`
	Platform     string `json:"platform"`
	Architecture string `json:"architecture"`
	AgentVersion string `json:"agentVersion"`
}

type Identity struct {
	Node struct {
		ID string `json:"id"`
	} `json:"node"`
	Credential string    `json:"nodeCredential"`
	ExpiresAt  time.Time `json:"credentialExpiresAt"`
}

type Client struct {
	base string
	http *http.Client
}

type APIError struct{ Status int }

var ErrProtocol = errors.New("invalid control-plane response")

const maxResponseBytes = 64 * 1024

func (e *APIError) Error() string { return fmt.Sprintf("control plane returned HTTP %d", e.Status) }

func Permanent(err error) bool {
	var api *APIError
	return errors.Is(err, ErrProtocol) || (errors.As(err, &api) && api.Status >= 300 && api.Status < 500 && api.Status != 408 && api.Status != 429)
}

func New(server string) (*Client, error) {
	u, err := url.Parse(server)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") || u.RawPath != "" || u.Opaque != "" {
		return nil, errors.New("server must be an origin URL without credentials, path, query, or fragment")
	}
	loopback := u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback()
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return nil, errors.New("HTTPS is required except for loopback development servers")
	}
	return &Client{base: strings.TrimSuffix(u.String(), "/"), http: &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

func (c *Client) Enroll(ctx context.Context, input Enrollment) (Identity, error) {
	var identity Identity
	err := c.post(ctx, "/v1/nodes/enroll", "", input, &identity)
	return identity, err
}

func (c *Client) Heartbeat(ctx context.Context, id, credential string, sequence uint64, inventory Inventory) error {
	var acknowledgement struct {
		Accepted bool `json:"accepted"`
	}
	err := c.post(ctx, "/v1/nodes/"+url.PathEscape(id)+"/heartbeat", credential, struct {
		Sequence  uint64    `json:"sequence"`
		Inventory Inventory `json:"inventory"`
	}{sequence, inventory}, &acknowledgement)
	if err != nil {
		return err
	}
	if !acknowledgement.Accepted {
		return ErrProtocol
	}
	return nil
}

func (c *Client) post(ctx context.Context, path, credential string, body, output any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return errors.New("cannot encode control message")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, bytes.NewReader(data))
	if err != nil {
		return errors.New("cannot create control request")
	}
	req.Header.Set("Content-Type", "application/json")
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	response, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Avoid reflecting remote bodies or transport URLs into logs.
		return errors.New("control plane connection failed (check address, TLS, and connectivity)")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// Drain bounded error bodies so transient failures can reuse keep-alive
		// connections. Never reflect their contents into logs.
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes+1))
		return &APIError{Status: response.StatusCode}
	}
	data, err = io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return errors.New("control response interrupted")
	}
	if len(data) > maxResponseBytes {
		return ErrProtocol
	}
	if output != nil && json.Unmarshal(data, output) != nil {
		return ErrProtocol
	}
	return nil
}

// AuthorizeStorage validates one transfer request against current controller state.
func (c *Client) AuthorizeStorage(ctx context.Context, id, credential, token, access string, collectionID *string) error {
	var acknowledgement struct {
		Accepted bool `json:"accepted"`
	}
	input := struct {
		Token      string `json:"token"`
		Permission struct {
			Access       string  `json:"access"`
			CollectionID *string `json:"collectionId"`
		} `json:"permission"`
	}{Token: token}
	input.Permission.Access = access
	input.Permission.CollectionID = collectionID
	if err := c.post(ctx, "/v1/nodes/"+url.PathEscape(id)+"/storage-grants/validate", credential, input, &acknowledgement); err != nil {
		return err
	}
	if !acknowledgement.Accepted {
		return ErrProtocol
	}
	return nil
}
