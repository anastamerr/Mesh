// Package stream owns the lifetime of bidirectional application connections.
package stream

import (
	"context"
	"io"
)

// Bridge copies both directions until either side finishes or ctx is cancelled.
// It closes both connections and waits for both copies before returning.
func Bridge(ctx context.Context, left, right io.ReadWriteCloser) {
	done := make(chan struct{}, 2)
	stop := context.AfterFunc(ctx, func() {
		_ = left.Close()
		_ = right.Close()
	})
	go func() { _, _ = io.Copy(left, right); done <- struct{}{} }()
	go func() { _, _ = io.Copy(right, left); done <- struct{}{} }()
	<-done
	_ = left.Close()
	_ = right.Close()
	<-done
	stop()
}
