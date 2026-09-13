package cli

import (
	"context"
	"errors"
	"time"

	"mesh.local/agent/internal/control"
	"mesh.local/agent/internal/state"
)

type pairedHeartbeat struct {
	client     *control.Client
	saved      *state.State
	store      *state.Store
	candidates func() ([]control.DirectCandidate, error)
}

func (p *pairedHeartbeat) Heartbeat(ctx context.Context, id, credential string, sequence uint64, inventory control.Inventory) error {
	var candidates []control.DirectCandidate
	var err error
	if p.candidates != nil {
		candidates, err = p.candidates()
		if err != nil {
			return err
		}
	}
	if err := p.client.HeartbeatWithCandidates(ctx, id, credential, sequence, inventory, candidates); err != nil {
		return err
	}
	if time.Until(p.saved.ExpiresAt) < 7*24*time.Hour {
		return renewIdentity(ctx, p.saved, p.store, p.client)
	}
	return nil
}

func renewIdentity(ctx context.Context, saved *state.State, store *state.Store, client *control.Client) error {
	if saved.PublicKeyFingerprint == "" {
		return errors.New("only paired devices can renew their lease")
	}
	expires, err := client.Renew(ctx, saved.NodeID, saved.Credential)
	if err != nil {
		return err
	}
	next := *saved
	next.ExpiresAt = expires
	if err := store.Save(next); err != nil {
		return err
	}
	*saved = next
	return nil
}
