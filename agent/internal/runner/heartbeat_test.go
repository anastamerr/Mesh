package runner

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"mesh.local/agent/internal/control"
	"mesh.local/agent/internal/state"
)

type fakeStore struct {
	value state.State
	err   error
}

func (s *fakeStore) Save(v state.State) error {
	if s.err == nil {
		s.value = v
	}
	return s.err
}

type fakeSender struct {
	sequences []uint64
	err       error
}

func (s *fakeSender) Heartbeat(_ context.Context, _, _ string, sequence uint64, _ control.Inventory) error {
	s.sequences = append(s.sequences, sequence)
	return s.err
}

func discover(context.Context) (control.Inventory, error) { return control.Inventory{}, nil }

func TestLostReplyReservesSequenceAcrossRestart(t *testing.T) {
	saved := state.State{ExpiresAt: time.Now().Add(time.Hour)}
	store := &fakeStore{}
	sender := &fakeSender{err: errors.New("reply lost")}
	if err := Once(context.Background(), &saved, store, sender, discover); err == nil {
		t.Fatal("expected network failure")
	}
	restarted := store.value
	sender.err = nil
	if err := Once(context.Background(), &restarted, store, sender, discover); err != nil {
		t.Fatal(err)
	}
	if len(sender.sequences) != 2 || sender.sequences[0] != 0 || sender.sequences[1] != 1 {
		t.Fatalf("sequence replayed: %v", sender.sequences)
	}
}

func TestPersistenceFailureNeverSends(t *testing.T) {
	saved := state.State{ExpiresAt: time.Now().Add(time.Hour)}
	sender := &fakeSender{}
	err := Once(context.Background(), &saved, &fakeStore{err: errors.New("disk full")}, sender, discover)
	if err == nil || len(sender.sequences) != 0 || saved.NextSequence != 0 {
		t.Fatal("sent without durable sequence")
	}
}

func TestRevokedCredentialStopsLoop(t *testing.T) {
	saved := state.State{ExpiresAt: time.Now().Add(time.Hour)}
	sender := &fakeSender{err: &control.APIError{Status: 401}}
	err := Loop(context.Background(), &saved, &fakeStore{}, sender, discover, time.Second, io.Discard)
	if err == nil || len(sender.sequences) != 1 {
		t.Fatal("revoked identity did not stop immediately")
	}
}

func TestCancelledLoopDoesNotSend(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	saved := state.State{ExpiresAt: time.Now().Add(time.Hour)}
	sender := &fakeSender{}
	if err := Loop(ctx, &saved, &fakeStore{}, sender, discover, time.Hour, io.Discard); err != nil || len(sender.sequences) != 0 {
		t.Fatal("cancelled agent attempted a request")
	}
}

type reconnectSender struct {
	calls  int
	cancel context.CancelFunc
}

func (s *reconnectSender) Heartbeat(_ context.Context, _, _ string, _ uint64, _ control.Inventory) error {
	s.calls++
	if s.calls == 1 {
		return &control.APIError{Status: 503}
	}
	s.cancel()
	return nil
}

func TestTemporaryFailureReconnects(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	saved := state.State{ExpiresAt: time.Now().Add(time.Hour)}
	sender := &reconnectSender{cancel: cancel}
	if err := Loop(ctx, &saved, &fakeStore{}, sender, discover, time.Second, io.Discard); err != nil {
		t.Fatal(err)
	}
	if sender.calls != 2 || saved.NextSequence != 2 {
		t.Fatal("temporary failure did not reconnect with a new sequence")
	}
}

func TestExpiredCredentialDoesNotSend(t *testing.T) {
	saved := state.State{ExpiresAt: time.Now().Add(-time.Hour)}
	sender := &fakeSender{}
	err := Once(context.Background(), &saved, &fakeStore{}, sender, discover)
	if !errors.Is(err, ErrExpired) || len(sender.sequences) != 0 {
		t.Fatal("expired credential sent a request")
	}
}

func TestCancellationDuringDiscoveryDoesNotPersistOrSend(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	saved := state.State{ExpiresAt: time.Now().Add(time.Hour)}
	store := &fakeStore{}
	sender := &fakeSender{}
	err := Once(ctx, &saved, store, sender, func(context.Context) (control.Inventory, error) {
		cancel()
		return control.Inventory{}, nil
	})
	if !errors.Is(err, context.Canceled) || store.value.NextSequence != 0 || len(sender.sequences) != 0 {
		t.Fatal("cancelled observation mutated state or sent a request")
	}
}
