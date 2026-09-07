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
)

const (
	maxBatchFiles     = 128
	maxBatchFileBytes = 256 << 10
	batchFeature      = "batch-v1"
)

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
		func() {
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
		}()
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, size))
	if err != nil || int64(len(data)) != size {
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
	p, err := s.progress(ctx, id, m, false)
	if err != nil {
		return nil, err
	}
	for _, index := range indices {
		if p.Offsets[index] != 0 {
			return nil, ErrConflict
		}
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
		if _, err := writeFile(ctx, root, e, 0, data[:e.Size]); err != nil {
			return nil, err
		}
		data = data[e.Size:]
		offsets[i] = e.Size
		for dir := path.Dir(e.Path); ; dir = path.Dir(dir) {
			parents[dir] = true
			if dir == "." {
				break
			}
		}
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
	for i, index := range indices {
		if _, err = tx.ExecContext(ctx, "UPDATE files SET offset=? WHERE collection=? AND ordinal=?", offsets[i], id, index); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return offsets, nil
}

func (c *Client) uploadBatch(ctx context.Context, root *os.Root, m Manifest, id string, indices []int) error {
	var data bytes.Buffer
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
			_, err = io.CopyN(&data, f, e.Size)
		}
		err = errors.Join(err, f.Close())
		if err != nil {
			return err
		}
	}
	res, err := c.request(ctx, "PUT", batchPath(id, indices), bytes.NewReader(data.Bytes()), nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var ack struct {
		Offsets []int64 `json:"offsets"`
	}
	if err := readJSON(res.Body, &ack); err != nil {
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
