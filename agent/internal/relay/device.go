package relay

import (
	"context"
	"crypto/tls"
	"errors"
	"math/rand/v2"
	"net"
	"sync"
	"time"
)

type DeviceConfig struct {
	RelayOrigin string
	NodeID      string
	TLSConfig   *tls.Config
	Workers     int
	BackoffMin  time.Duration
	BackoffMax  time.Duration
	Credential  func(context.Context) (string, error)
	DialTarget  func(context.Context) (net.Conn, error)
	OnError     func(error)
}

// ServeDevice maintains a bounded pool of outbound relay waiters and connects
// each matched stream to a local service.
func ServeDevice(ctx context.Context, c DeviceConfig) error {
	if c.Workers <= 0 {
		c.Workers = 2
	}
	if c.Workers > 16 {
		return errors.New("relay workers must not exceed 16")
	}
	if c.BackoffMin <= 0 {
		c.BackoffMin = 500 * time.Millisecond
	}
	if c.BackoffMax <= 0 {
		c.BackoffMax = 15 * time.Second
	}
	if c.BackoffMax < c.BackoffMin || c.Credential == nil || c.DialTarget == nil {
		return errors.New("invalid device relay configuration")
	}
	var wg sync.WaitGroup
	for range c.Workers {
		wg.Add(1)
		go func() { defer wg.Done(); deviceWorker(ctx, c) }()
	}
	<-ctx.Done()
	wg.Wait()
	return ctx.Err()
}

func deviceWorker(ctx context.Context, c DeviceConfig) {
	backoff := c.BackoffMin
	for ctx.Err() == nil {
		credential, err := c.Credential(ctx)
		if err == nil {
			var relayConn net.Conn
			relayConn, err = Dial(ctx, c.RelayOrigin, c.NodeID, RoleDevice, credential, c.TLSConfig)
			if err == nil {
				var target net.Conn
				target, err = c.DialTarget(ctx)
				if err == nil {
					bridgeConns(ctx, relayConn, target)
					backoff = c.BackoffMin
					continue
				}
				_ = relayConn.Close()
			}
		}
		if errors.Is(err, errIdle) {
			backoff = c.BackoffMin
			continue
		}
		if c.OnError != nil && ctx.Err() == nil {
			c.OnError(err)
		}
		jitter := time.Duration(rand.Int64N(int64(backoff/2 + 1)))
		timer := time.NewTimer(backoff + jitter)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff *= 2
		if backoff > c.BackoffMax {
			backoff = c.BackoffMax
		}
	}
}

func bridgeConns(ctx context.Context, a, b net.Conn) {
	done := make(chan struct{}, 2)
	cancelled := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = a.Close()
			_ = b.Close()
		case <-cancelled:
		}
	}()
	go copyHalf(a, b, done)
	go copyHalf(b, a, done)
	<-done
	_ = a.Close()
	_ = b.Close()
	<-done
	close(cancelled)
}
