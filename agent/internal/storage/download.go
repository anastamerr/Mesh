package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func (c *Client) Download(ctx context.Context, id, destination string) (err error) {
	if !validID(id) {
		return ErrInvalid
	}
	// Reserve a new destination instead of merging into or replacing user files.
	if err = os.Mkdir(destination, 0700); err != nil {
		return err
	}
	success := false
	defer func() {
		if !success {
			err = errors.Join(err, os.RemoveAll(destination))
		}
	}()
	root, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer root.Close()
	var m Manifest
	if err = c.json(ctx, "GET", "/v1/collections/"+id, nil, &m); err != nil {
		return err
	}
	_, actual, err := m.encoded()
	if err != nil {
		return err
	}
	if actual != id {
		return ErrConflict
	}
	_, total := m.Statistics()
	var completed int64
	c.progress(TransferEvent{Phase: "Downloading", Total: total})
	progress := func(n int64) {
		completed += n
		c.progress(TransferEvent{Phase: "Downloading", Completed: completed, Total: total})
	}
	for _, e := range m.Entries {
		if e.Directory {
			if err = root.MkdirAll(e.Path, 0700); err != nil {
				return err
			}
		} else if e.Size == 0 {
			if err = receiveFile(ctx, root, e, bytes.NewReader(nil), progress); err != nil {
				return err
			}
		}
	}
	for i := 0; i < len(m.Entries); {
		e := m.Entries[i]
		if e.Directory || e.Size == 0 {
			i++
			continue
		}
		if c.batch {
			indices := batchIndices(m, i, nil)
			if e.Size > 0 && len(indices) > 1 {
				if err = c.downloadBatch(ctx, root, m, id, indices, progress); err != nil {
					return err
				}
				i = indices[len(indices)-1] + 1
				continue
			}
		}
		if err = c.downloadFile(ctx, root, id, i, e, progress); err != nil {
			return err
		}
		i++
	}
	if err = syncTree(root, m); err != nil {
		return err
	}
	// Ensure creation of the destination itself is durable on Unix.
	parent, err := os.OpenRoot(filepath.Dir(destination))
	if err != nil {
		return err
	}
	err = syncDirectory(parent, ".")
	parent.Close()
	if err != nil {
		return err
	}
	c.progress(TransferEvent{Phase: "Complete", Completed: total, Total: total})
	success = true
	return nil
}
func (c *Client) downloadFile(ctx context.Context, root *os.Root, id string, index int, e Entry, progress func(int64)) (err error) {
	res, err := c.request(ctx, "GET", fmt.Sprintf("/v1/collections/%s/files/%d", id, index), nil, nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	return receiveFile(ctx, root, e, res.Body, progress)
}
func receiveFile(ctx context.Context, root *os.Root, e Entry, body io.Reader, progress func(int64)) (err error) {
	f, err := root.OpenFile(e.Path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if err = verify(ctx, io.TeeReader(body, &progressWriter{writer: f, progress: progress}), e); err != nil {
		return err
	}
	return f.Sync()
}

type progressWriter struct {
	writer   io.Writer
	progress func(int64)
}

func (w *progressWriter) Write(p []byte) (int, error) {
	n, err := w.writer.Write(p)
	w.progress(int64(n))
	return n, err
}
