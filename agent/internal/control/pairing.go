package control

import (
	"context"
	"errors"
	"net/http"
	"time"
)

type PairingRequest struct {
	ID                   string `json:"pairingRequestId"`
	Name                 string `json:"name"`
	Platform             string `json:"platform"`
	Architecture         string `json:"architecture"`
	AgentVersion         string `json:"agentVersion"`
	PublicKeyFingerprint string `json:"publicKeyFingerprint"`
	PairingSecretHash    string `json:"pairingSecretHash"`
	NodeCredentialHash   string `json:"nodeCredentialHash"`
}

type PairingChallenge struct {
	ID                   string    `json:"id"`
	Code                 string    `json:"code"`
	Name                 string    `json:"name"`
	PublicKeyFingerprint string    `json:"publicKeyFingerprint"`
	ExpiresAt            time.Time `json:"expiresAt"`
}

type Node struct {
	ID                   string `json:"id"`
	Name                 string `json:"name"`
	PublicKeyFingerprint string `json:"publicKeyFingerprint"`
}

type PairingStatus struct {
	Status              string    `json:"status"`
	Node                Node      `json:"node"`
	CredentialExpiresAt time.Time `json:"credentialExpiresAt"`
}

func (c *Client) StartPairing(ctx context.Context, input PairingRequest) (PairingChallenge, error) {
	var challenge PairingChallenge
	err := c.request(ctx, http.MethodPost, "/v1/pairing-challenges", "", input, &challenge)
	if err == nil && (challenge.ID != input.ID || challenge.Code == "" || challenge.ExpiresAt.IsZero()) {
		err = ErrProtocol
	}
	return challenge, err
}

func (c *Client) PollPairing(ctx context.Context, id, secret string) (PairingStatus, error) {
	var status PairingStatus
	if !uuid.MatchString(id) {
		return status, ErrProtocol
	}
	err := c.request(ctx, http.MethodGet, "/v1/pairing-challenges/"+id, secret, nil, &status)
	if err == nil && status.Status != "pending" && status.Status != "approved" {
		err = ErrProtocol
	}
	return status, err
}

func (c *Client) PairingChallenges(ctx context.Context, operator string) ([]PairingChallenge, error) {
	var challenges []PairingChallenge
	err := c.request(ctx, http.MethodGet, "/v1/pairing-challenges", operator, nil, &challenges)
	return challenges, err
}

func (c *Client) ApprovePairing(ctx context.Context, operator, id, fingerprint string) error {
	if !uuid.MatchString(id) || !collectionID.MatchString(fingerprint) {
		return ErrProtocol
	}
	var result struct {
		Node Node `json:"node"`
	}
	err := c.request(ctx, http.MethodPost, "/v1/pairing-challenges/"+id+"/approve", operator,
		struct {
			PublicKeyFingerprint string `json:"publicKeyFingerprint"`
		}{fingerprint}, &result)
	if err == nil && (!uuid.MatchString(result.Node.ID) || result.Node.PublicKeyFingerprint != fingerprint) {
		return ErrProtocol
	}
	return err
}

func (c *Client) DeviceFingerprint(ctx context.Context, operator, id string) (string, error) {
	if !uuid.MatchString(id) {
		return "", ErrProtocol
	}
	var connection struct {
		NodeID               string `json:"nodeId"`
		PublicKeyFingerprint string `json:"publicKeyFingerprint"`
	}
	if err := c.request(ctx, http.MethodGet, "/v1/nodes/"+id+"/connection", operator, nil, &connection); err != nil {
		return "", err
	}
	if connection.NodeID != id || !collectionID.MatchString(connection.PublicKeyFingerprint) {
		return "", errors.New("device has no active pairing; pair it before using remote access")
	}
	return connection.PublicKeyFingerprint, nil
}

func (c *Client) RelayOrigin(ctx context.Context) (string, error) {
	var network struct {
		RelayOrigin string `json:"relayOrigin"`
	}
	err := c.request(ctx, http.MethodGet, "/v1/network", "", nil, &network)
	var api *APIError
	if errors.As(err, &api) && api.Status == 404 {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if network.RelayOrigin != "" {
		if _, err := New(network.RelayOrigin); err != nil {
			return "", ErrProtocol
		}
	}
	return network.RelayOrigin, nil
}

func (c *Client) Renew(ctx context.Context, id, credential string) (time.Time, error) {
	var result struct {
		ExpiresAt time.Time `json:"expiresAt"`
	}
	if !uuid.MatchString(id) {
		return result.ExpiresAt, ErrProtocol
	}
	err := c.request(ctx, http.MethodPost, "/v1/nodes/"+id+"/renew", credential, nil, &result)
	if err == nil && !result.ExpiresAt.After(time.Now()) {
		err = ErrProtocol
	}
	return result.ExpiresAt, err
}

func (c *Client) RelayTicket(ctx context.Context, id, role, credential string) (string, time.Time, error) {
	var result struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expiresAt"`
	}
	if !uuid.MatchString(id) || (role != "device" && role != "consumer") {
		return "", result.ExpiresAt, ErrProtocol
	}
	err := c.request(ctx, http.MethodPost, "/v1/nodes/"+id+"/relay-tickets", credential, struct {
		Role string `json:"role"`
	}{role}, &result)
	if err == nil && (!grantToken.MatchString(result.Token) || !result.ExpiresAt.After(time.Now())) {
		err = ErrProtocol
	}
	return result.Token, result.ExpiresAt, err
}
