package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestReceiveBatchWritesFilesAndSerializesProgress(t *testing.T) {
	const fileCount = 12
	m := Manifest{Version: 1, Entries: make([]Entry, fileCount)}
	var body bytes.Buffer
	for i := range m.Entries {
		data := bytes.Repeat([]byte{byte(i + 1)}, 64<<10)
		m.Entries[i] = entry(fmt.Sprintf("%02d", i), data)
		body.Write(data)
	}
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var active atomic.Int32
	var overlap atomic.Bool
	var completed atomic.Int64
	progress := func(n int64) {
		if active.Add(1) != 1 {
			overlap.Store(true)
		}
		time.Sleep(time.Millisecond)
		completed.Add(n)
		active.Add(-1)
	}
	indices := make([]int, fileCount)
	for i := range indices {
		indices[i] = i
	}
	if err := receiveBatch(ctx, cancel, io.NopCloser(bytes.NewReader(body.Bytes())), root, m, indices, progress); err != nil {
		t.Fatal(err)
	}
	if overlap.Load() {
		t.Fatal("progress callbacks overlapped")
	}
	if completed.Load() != int64(body.Len()) {
		t.Fatalf("progress = %d, want %d", completed.Load(), body.Len())
	}
	for i, e := range m.Entries {
		got, err := os.ReadFile(filepath.Join(dir, e.Path))
		if err != nil || !bytes.Equal(got, bytes.Repeat([]byte{byte(i + 1)}, 64<<10)) {
			t.Fatalf("file %s: %v", e.Path, err)
		}
	}
}

func TestReceiveBatchRejectsTruncatedAndTrailingBodies(t *testing.T) {
	m := Manifest{Version: 1, Entries: []Entry{entry("a", []byte("abc")), entry("b", []byte("def"))}}
	for _, test := range []struct {
		name string
		body string
	}{
		{name: "truncated", body: "abcde"},
		{name: "trailing", body: "abcdefx"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, err := os.OpenRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if err := receiveBatch(ctx, cancel, io.NopCloser(bytes.NewBufferString(test.body)), root, m, []int{0, 1}, nil); err == nil {
				t.Fatal("invalid body accepted")
			}
		})
	}
}

func TestReceiveBatchWorkerFailureUnblocksBodyAndWaits(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := root.WriteFile("a", []byte("occupied"), 0600); err != nil {
		t.Fatal(err)
	}
	m := Manifest{Version: 1, Entries: []Entry{entry("a", []byte("a")), entry("b", []byte("b"))}}
	body := newBlockingBody([]byte("a"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- receiveBatch(ctx, cancel, body, root, m, []int{0, 1}, nil) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("worker failure ignored")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker failure did not unblock body read")
	}
	if !body.closed.Load() {
		t.Fatal("body was not closed")
	}
	if err := root.Remove("a"); err != nil {
		t.Fatalf("worker still holds output file: %v", err)
	}
}

func TestReceiveBatchCancellationClosesBody(t *testing.T) {
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	m := Manifest{Version: 1, Entries: []Entry{entry("a", []byte("a"))}}
	body := newBlockingBody(nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- receiveBatch(ctx, cancel, body, root, m, []int{0}, nil) }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not unblock body read")
	}
	if !body.closed.Load() {
		t.Fatal("body was not closed")
	}
}

type blockingBody struct {
	prefix []byte
	once   sync.Once
	done   chan struct{}
	closed atomic.Bool
}

func newBlockingBody(prefix []byte) *blockingBody {
	return &blockingBody{prefix: prefix, done: make(chan struct{})}
}

func (r *blockingBody) Read(p []byte) (int, error) {
	if len(r.prefix) != 0 {
		n := copy(p, r.prefix)
		r.prefix = r.prefix[n:]
		return n, nil
	}
	<-r.done
	return 0, io.ErrClosedPipe
}

func (r *blockingBody) Close() error {
	r.once.Do(func() {
		r.closed.Store(true)
		close(r.done)
	})
	return nil
}

func BenchmarkReceiveBatch(b *testing.B) {
	const fileCount = 64
	m := Manifest{Version: 1, Entries: make([]Entry, fileCount)}
	indices := make([]int, fileCount)
	var data bytes.Buffer
	for i := range m.Entries {
		contents := bytes.Repeat([]byte{byte(i)}, 4<<10)
		m.Entries[i] = entry(fmt.Sprintf("%02d", i), contents)
		indices[i] = i
		data.Write(contents)
	}
	b.SetBytes(int64(data.Len()))
	for _, parallel := range []bool{false, true} {
		b.Run(fmt.Sprintf("parallel=%t", parallel), func(b *testing.B) {
			b.SetBytes(int64(data.Len()))
			for i := 0; i < b.N; i++ {
				root, err := os.OpenRoot(b.TempDir())
				if err != nil {
					b.Fatal(err)
				}
				ctx, cancel := context.WithCancel(context.Background())
				body := io.NopCloser(bytes.NewReader(data.Bytes()))
				if parallel {
					err = receiveBatch(ctx, cancel, body, root, m, indices, nil)
				} else {
					err = receiveBatchSequential(ctx, body, root, m, indices)
				}
				cancel()
				err = errors.Join(err, root.Close())
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func receiveBatchSequential(ctx context.Context, body io.Reader, root *os.Root, m Manifest, indices []int) error {
	for _, index := range indices {
		e := m.Entries[index]
		if err := receiveFile(ctx, root, e, io.LimitReader(body, e.Size), func(int64) {}); err != nil {
			return err
		}
	}
	var extra [1]byte
	n, err := io.ReadFull(body, extra[:])
	if n != 0 || err != io.EOF {
		return ErrInvalid
	}
	return nil
}
