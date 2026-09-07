package control

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"time"
)

var uuid = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$`)
var collectionID = regexp.MustCompile(`^[a-f0-9]{64}$`)
var grantToken = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

type Collection struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	FileCount   int        `json:"fileCount"`
	TotalBytes  int64      `json:"totalBytes"`
	ConfirmedAt *time.Time `json:"confirmedAt,omitempty"`
}

func (c *Client) ResolveNode(ctx context.Context, key, selector string) (string, error) {
	if uuid.MatchString(selector) {
		return selector, nil
	}
	var nodes []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := c.request(ctx, http.MethodGet, "/v1/nodes", key, nil, &nodes); err != nil {
		return "", err
	}
	found := ""
	for _, node := range nodes {
		if node.Name == selector {
			if !uuid.MatchString(node.ID) {
				return "", ErrProtocol
			}
			if found != "" {
				return "", errors.New("node name is ambiguous; use its full ID")
			}
			found = node.ID
		}
	}
	if found == "" {
		return "", errors.New("node not found among recent nodes; check its name or use its full ID")
	}
	return found, nil
}
func (c *Client) RegisterCollection(ctx context.Context, key, node string, entry Collection) error {
	body := struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		FileCount  int    `json:"fileCount"`
		TotalBytes int64  `json:"totalBytes"`
	}{entry.ID, entry.Name, entry.FileCount, entry.TotalBytes}
	return c.postAccepted(ctx, "/v1/nodes/"+url.PathEscape(node)+"/collections", key, body)
}
func (c *Client) ConfirmCollection(ctx context.Context, node, key, id string, count int, total int64) error {
	body := struct {
		ID         string `json:"id"`
		FileCount  int    `json:"fileCount"`
		TotalBytes int64  `json:"totalBytes"`
	}{id, count, total}
	return c.postAccepted(ctx, "/v1/nodes/"+url.PathEscape(node)+"/collections/confirm", key, body)
}
func (c *Client) Collections(ctx context.Context, key, node, after string) ([]Collection, error) {
	var entries []Collection
	if err := c.request(ctx, http.MethodGet, "/v1/nodes/"+url.PathEscape(node)+"/collections?after="+url.QueryEscape(after), key, nil, &entries); err != nil {
		return nil, err
	}
	if entries == nil || len(entries) > 100 {
		return nil, ErrProtocol
	}
	for _, e := range entries {
		if !collectionID.MatchString(e.ID) || e.ID <= after || e.Name == "" || e.FileCount < 0 || e.FileCount > 10000 || e.TotalBytes < 0 {
			return nil, ErrProtocol
		}
		after = e.ID
	}
	return entries, nil
}
func (c *Client) StorageGrant(ctx context.Context, key, node, access string, id *string) (string, error) {
	var grant struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expiresAt"`
	}
	body := struct {
		Access       string  `json:"access"`
		CollectionID *string `json:"collectionId"`
	}{access, id}
	if err := c.request(ctx, http.MethodPost, "/v1/nodes/"+url.PathEscape(node)+"/storage-grants", key, body, &grant); err != nil {
		return "", err
	}
	if !grantToken.MatchString(grant.Token) || !grant.ExpiresAt.After(time.Now()) {
		return "", ErrProtocol
	}
	return grant.Token, nil
}
