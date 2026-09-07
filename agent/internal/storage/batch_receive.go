package storage

import (
	"bytes"
	"context"
	"io"
	"os"
	"sync"
)

const maxBatchReceiveWorkers = 4

type batchReceiveJob struct {
	entry Entry
	data  []byte
}

// receiveBatch reads one bounded HTTP body sequentially while distinct files are
// verified, written, and synced concurrently. It returns only after every worker
// has released its file, including after cancellation or an error.
func receiveBatch(ctx context.Context, cancel context.CancelFunc, body io.ReadCloser, root *os.Root, m Manifest, indices []int, progress func(int64)) error {
	stopClose := context.AfterFunc(ctx, func() { _ = body.Close() })
	defer stopClose()
	workerCount := min(maxBatchReceiveWorkers, len(indices))
	jobs := make(chan batchReceiveJob)
	var workers sync.WaitGroup
	var failOnce sync.Once
	var errorMu sync.Mutex
	var firstErr error
	fail := func(err error) {
		if err == nil {
			return
		}
		failOnce.Do(func() {
			errorMu.Lock()
			firstErr = err
			errorMu.Unlock()
			cancel()
			_ = body.Close()
		})
	}
	getError := func() error {
		errorMu.Lock()
		defer errorMu.Unlock()
		return firstErr
	}

	var progressMu sync.Mutex
	worker := func() {
		defer workers.Done()
		for job := range jobs {
			if ctx.Err() != nil {
				fail(ctx.Err())
				continue
			}
			err := receiveFile(ctx, root, job.entry, bytes.NewReader(job.data), func(n int64) {
				progressMu.Lock()
				if progress != nil {
					progress(n)
				}
				progressMu.Unlock()
			})
			fail(err)
		}
	}
	workers.Add(workerCount)
	for range workerCount {
		go worker()
	}

	for _, index := range indices {
		e := m.Entries[index]
		data := make([]byte, e.Size)
		if _, err := io.ReadFull(body, data); err != nil {
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			fail(err)
			break
		}
		select {
		case jobs <- batchReceiveJob{entry: e, data: data}:
		case <-ctx.Done():
			fail(ctx.Err())
		}
		if getError() != nil {
			break
		}
	}
	if getError() == nil {
		var extra [1]byte
		n, err := io.ReadFull(body, extra[:])
		if n != 0 || err != io.EOF {
			if ctx.Err() != nil {
				fail(ctx.Err())
			} else {
				fail(ErrInvalid)
			}
		}
	}
	close(jobs)
	workers.Wait()
	return getError()
}
