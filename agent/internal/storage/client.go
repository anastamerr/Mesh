package storage

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
	"strconv"
	"strings"
	"time"
)

type Client struct {
	base, key string
	http      *http.Client
}

func NewClient(server, key string) (*Client, error) {
	u, err := url.Parse(server)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Path != "" && u.Path != "/") || u.RawPath != "" || u.Opaque != "" {
		return nil, errors.New("storage server must be an origin URL")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || net.ParseIP(u.Hostname()).IsLoopback())) {
		return nil, errors.New("storage requires HTTPS except on loopback")
	}
	if !ValidKey(key) {
		return nil, errors.New("storage key must contain 43 base64url characters")
	}
	return &Client{base: strings.TrimSuffix(u.String(), "/"), key: key, http: &http.Client{Timeout: 30 * time.Minute, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func ValidKey(key string) bool {
	if len(key) != 43 {
		return false
	}
	for _, c := range key {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
func (c *Client) request(ctx context.Context, method, path string, body io.Reader, offset *int64) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	if offset != nil {
		req.Header.Set("Upload-Offset", strconv.FormatInt(*offset, 10))
		req.Header.Set("Content-Type", "application/octet-stream")
	} else {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("storage connection failed; retry the upload to resume")
	}
	if res.StatusCode != 200 {
		defer res.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
		return nil, fmt.Errorf("storage returned HTTP %d", res.StatusCode)
	}
	return res, nil
}
func (c *Client) json(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	res, err := c.request(ctx, method, path, body, nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	return readJSON(res.Body, output)
}
func readJSON(r io.Reader, value any) error {
	data, err := io.ReadAll(io.LimitReader(r, MaxManifestBytes+1))
	if err != nil {
		return err
	}
	if len(data) > MaxManifestBytes || json.Unmarshal(data, value) != nil {
		return errors.New("invalid storage response")
	}
	return nil
}

func (c *Client) List(ctx context.Context, after string) ([]string, error) {
	if after != "" && !validID(after) {
		return nil, ErrInvalid
	}
	var ids []string
	err := c.json(ctx, "GET", "/v1/collections?after="+after, nil, &ids)
	if err != nil {
		return nil, err
	}
	if ids == nil || len(ids) > 100 {
		return nil, ErrInvalid
	}
	for _, id := range ids {
		if !validID(id) || id <= after {
			return nil, ErrInvalid
		}
		after = id
	}
	return ids, nil
}
