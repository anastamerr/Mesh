package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"time"

	"mesh.local/agent/internal/control"
	"mesh.local/agent/internal/state"
)

type Sender interface {
	Heartbeat(context.Context, string, string, uint64, control.Inventory) error
}

type Saver interface{ Save(state.State) error }
type Discover func(context.Context) (control.Inventory, error)

var ErrExpired = errors.New("node credential has expired; revoke this node and enroll a fresh identity")
var ErrPersistence = errors.New("cannot persist heartbeat sequence")
var ErrSequenceExhausted = errors.New("heartbeat sequence exhausted")

// Once reserves the sequence durably before any HTTP request. A crash or lost
// acknowledgement leaves a harmless gap rather than replaying an observation.
func Once(ctx context.Context, saved *state.State, store Saver, client Sender, discover Discover) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !saved.ExpiresAt.After(time.Now()) {
		return ErrExpired
	}
	if saved.NextSequence >= state.MaxSequence {
		return ErrSequenceExhausted
	}
	inventory, err := discover(ctx)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	sequence := saved.NextSequence
	next := *saved
	next.NextSequence++
	if err := store.Save(next); err != nil {
		return fmt.Errorf("%w: %v", ErrPersistence, err)
	}
	*saved = next
	return client.Heartbeat(ctx, saved.NodeID, saved.Credential, sequence, inventory)
}

func Loop(ctx context.Context, saved *state.State, store Saver, client Sender, discover Discover,
	interval time.Duration, log io.Writer) error {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return nil
		}
		err := Once(ctx, saved, store, client, discover)
		if ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, ErrExpired) || errors.Is(err, ErrPersistence) || errors.Is(err, ErrSequenceExhausted) || control.Permanent(err) {
			return fmt.Errorf("agent stopped: %w; inspect identity and controller state before restarting", err)
		}
		delay := interval
		if err != nil {
			fmt.Fprintf(log, "heartbeat failed: %v; reconnecting\n", err)
			delay = backoff + time.Duration(rand.Int64N(int64(backoff/2)+1))
			backoff = min(backoff*2, 30*time.Second)
		} else {
			fmt.Fprintf(log, "heartbeat accepted: node=%s sequence=%d\n", saved.NodeID, saved.NextSequence-1)
			backoff = time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
