package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
)

const (
	maxBatchFiles        = 128
	maxBatchFileBytes    = 256 << 10
	maxBatchWriteWorkers = 4
	batchFeature         = "batch-v1"
)

type batchWriteJob struct {
	entry Entry
	data  []byte
}

type batchFileWriter func(context.Context, *os.Root, Entry, int64, []byte) (bool, error)

// Batches contain whole small files in manifest order. Large or partially sent
// files retain the chunk protocol; batch offsets commit only after all files sync.
func batchIndices(m Manifest, start int, offsets []int64) []int {
	indices := make([]int, 0, maxBatchFiles)
	var size int64
	for i := start; i < len(m.Entries); i++ {
		e := m.Entries[i]
		if e.Directory || e.Size == 0 || (offsets != nil && offsets[i] == e.Size) {
			continue
		}
		if e.Size > maxBatchFileBytes || (offsets != nil && offsets[i] != 0) || len(indices) == maxBatchFiles || size+e.Size > ChunkSize {
			break
		}
		indices = append(indices, i)
		size += e.Size
	}
	return indices
}
func batchPath(id string, indices []int) string {
	parts := make([]string, len(indices))
	for i, index := range indices {
		parts[i] = strconv.Itoa(index)
	}
	return "/v1/collections/" + id + "/batch?indices=" + strings.Join(parts, ",")
}
func parseBatch(m Manifest, value string) ([]int, int64, error) {
	if len(value) > maxBatchFiles*6 {
		return nil, 0, ErrInvalid
	}
	parts := strings.Split(value, ",")
	if len(parts) == 0 || len(parts) > maxBatchFiles {
		return nil, 0, ErrInvalid
	}
	indices := make([]int, len(parts))
	previous := -1
	var size int64
	for i, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n <= previous || n >= len(m.Entries) {
			return nil, 0, ErrInvalid
		}
		e := m.Entries[n]
		if e.Directory || e.Size <= 0 || e.Size > maxBatchFileBytes {
			return nil, 0, ErrInvalid
		}
		size += e.Size
		if size > ChunkSize {
			return nil, 0, ErrInvalid
		}
		indices[i] = n
		previous = n
	}
	return indices, size, nil
}
func (s *Store) batchHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	reading := r.Method == http.MethodGet
	m, _, err := s.manifest(r.Context(), id, reading)
	if err != nil {
		storageError(w, err)
		return
	}
	indices, size, err := parseBatch(m, r.URL.Query().Get("indices"))
	if err != nil {
		storageError(w, err)
		return
	}
	if reading {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		root, err := s.root.OpenRoot("collections/" + id)
		if err != nil {
			panic(http.ErrAbortHandler)
		}
		defer func() {
			if root.Close() != nil {
				panic(http.ErrAbortHandler)
			}
		}()
		for _, index := range indices {
			e := m.Entries[index]
			f, err := regular(root, e.Path, os.O_RDONLY)
			if err != nil {
				panic(http.ErrAbortHandler)
			}
			_, err = io.CopyN(w, f, e.Size)
			closeErr := f.Close()
			if err != nil || closeErr != nil {
				panic(http.ErrAbortHandler)
			}
		}
		return
	}
	data, err := readUploadBody(http.MaxBytesReader(w, r.Body, size), size)
	if err != nil {
		storageError(w, ErrInvalid)
		return
	}
	offsets, err := s.writeBatch(r.Context(), id, m, indices, data)
	if err != nil {
		storageError(w, err)
		return
	}

	jsonReply(w, struct {
		Offsets []int64 `json:"offsets"`
	}{offsets})
}

// Group commit amortizes directory and journal syncs. Acknowledged offsets are
// atomic for this bounded batch; all file bytes and parents are durable first.
func (s *Store) writeBatch(ctx context.Context, id string, m Manifest, indices []int, data []byte) ([]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, complete, err := s.manifest(ctx, id, false)
	if err != nil {
		return nil, err
	}
	if complete {
		return nil, ErrConflict
	}
	// Validate only this batch using the (collection, ordinal) primary key.
	// Begin and Finish still validate the complete durable progress document.
	args := make([]any, 1, len(indices)+1)
	args[0] = id
	for _, index := range indices {
		args = append(args, index)
	}
	var pending int
	query := "SELECT count(*) FROM files WHERE collection=? AND offset=0 AND ordinal IN (" + strings.TrimSuffix(strings.Repeat("?,", len(indices)), ",") + ")"
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&pending); err != nil {
		return nil, err
	}
	if pending != len(indices) {
		return nil, ErrConflict
	}
	root, err := s.staging(id)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	offsets := make([]int64, len(indices))
	parents := make(map[string]bool)
	for i, index := range indices {
		e := m.Entries[index]
		offsets[i] = e.Size
		for dir := path.Dir(e.Path); ; dir = path.Dir(dir) {
			parents[dir] = true
			if dir == "." {
				break
			}
		}
	}
	if err := writeBatchFiles(ctx, root, m, indices, data, maxBatchWriteWorkers, writeFile); err != nil {
		return nil, err
	}
	// Sync every touched directory after all its entries have been created.
	for dir := range parents {
		if err := syncDirectory(root, dir); err != nil {
			return nil, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	statement, err := tx.PrepareContext(ctx, "UPDATE files SET offset=? WHERE collection=? AND ordinal=?")
	if err != nil {
		return nil, err
	}
	defer statement.Close()
	for i, index := range indices {
		if _, err = statement.ExecContext(ctx, offsets[i], id, index); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return offsets, nil
}

// writeBatchFiles bounds concurrent file syncs while the caller retains the
// store lock. On the first failure it cancels pending work and waits for every
// worker to close its file before returning.
func writeBatchFiles(ctx context.Context, root *os.Root, m Manifest, indices []int, data []byte, workerLimit int, write batchFileWriter) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan batchWriteJob)
	workerCount := min(workerLimit, len(indices))
	var workers sync.WaitGroup
	var failOnce sync.Once
	var firstErr error
	fail := func(err error) {
		if err == nil {
			return
		}
		failOnce.Do(func() {
			firstErr = err
			cancel()
		})
	}
	worker := func() {
		defer workers.Done()
		for job := range jobs {
			if ctx.Err() != nil {
				continue
			}
			_, err := write(ctx, root, job.entry, 0, job.data)
			fail(err)
		}
	}
	workers.Add(workerCount)
	for range workerCount {
		go worker()
	}

sendJobs:
	for _, index := range indices {
		e := m.Entries[index]
		job := batchWriteJob{entry: e, data: data[:e.Size]}
		data = data[e.Size:]
		select {
		case jobs <- job:
		case <-ctx.Done():
			break sendJobs
		}
	}
	close(jobs)
	workers.Wait()
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}

func (c *Client) uploadBatch(ctx context.Context, root *os.Root, m Manifest, id string, indices []int) error {
	var size int64
	for _, index := range indices {
		size += m.Entries[index].Size
	}
	data := make([]byte, size)
	var offset int64
	for _, index := range indices {
		e := m.Entries[index]
		f, err := regular(root, e.Path, os.O_RDONLY)
		if err != nil {
			return err
		}
		info, err := f.Stat()
		if err == nil && info.Size() != e.Size {
			err = errors.New("source size changed; retry to scan a fresh copy")
		}
		if err == nil {
			_, err = io.ReadFull(&contextReader{ctx: ctx, r: f}, data[offset:offset+e.Size])
		}
		err = errors.Join(err, f.Close())
		if err != nil {
			return err
		}
		offset += e.Size
	}
	res, err := c.request(ctx, "PUT", batchPath(id, indices), bytes.NewReader(data), nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var ack struct {
		Offsets []int64 `json:"offsets"`
	}
	if err := readJSON(ctx, res.Body, &ack); err != nil {
		return err
	}
	if len(ack.Offsets) != len(indices) {
		return ErrInvalid
	}
	for i, index := range indices {
		if ack.Offsets[i] != m.Entries[index].Size {
			return ErrInvalid
		}
	}
	return nil
}
func (c *Client) downloadBatch(ctx context.Context, root *os.Root, m Manifest, id string, indices []int, progress func(int64)) error {
	requestCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	res, err := c.request(requestCtx, "GET", batchPath(id, indices), nil, nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	return receiveBatch(requestCtx, cancel, res.Body, root, m, indices, progress)
}
