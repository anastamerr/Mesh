package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

func validateProgress(p Progress, m Manifest, id string) error {
	if p.ID != id || len(p.Offsets) != len(m.Entries) {
		return ErrInvalid
	}
	for i, e := range m.Entries {
		if p.Offsets[i] < 0 || p.Offsets[i] > e.Size || (p.Complete && p.Offsets[i] != e.Size) {
			return ErrInvalid
		}
	}
	return nil
}
func (c *Client) Upload(ctx context.Context, source string) (string, error) {
	c.progress(TransferEvent{Phase: "Scanning"})
	folder, err := Prepare(ctx, source, c.progress)
	if err != nil {
		return "", err
	}
	defer folder.Close()
	return c.UploadPrepared(ctx, folder)
}
func (c *Client) UploadPrepared(ctx context.Context, folder *PreparedFolder) (string, error) {
	root, m := folder.Root, folder.Manifest
	_, id, err := m.encoded()
	if err != nil {
		return "", err
	}
	var p Progress
	if err = c.json(ctx, "POST", "/v1/collections", m, &p); err != nil {
		return "", err
	}
	if err = validateProgress(p, m, id); err != nil {
		return "", err
	}
	_, total := m.Statistics()
	var completed int64
	for _, offset := range p.Offsets {
		completed += offset
	}
	reused := completed
	c.progress(TransferEvent{Phase: "Uploading", Completed: completed, Total: total, Reused: reused})
	var buffer []byte
	for i := 0; i < len(m.Entries); {
		e := m.Entries[i]
		if e.Directory || p.Offsets[i] == e.Size {
			i++
			continue
		}
		if c.batch {
			indices := batchIndices(m, i, p.Offsets)
			if len(indices) > 1 {
				if err := c.uploadBatch(ctx, root, m, id, indices); err != nil {
					return "", err
				}
				for _, index := range indices {
					completed += m.Entries[index].Size
				}
				c.progress(TransferEvent{Phase: "Uploading", Completed: completed, Total: total, Reused: reused})
				i = indices[len(indices)-1] + 1
				continue
			}
		}
		f, err := regular(root, e.Path, os.O_RDONLY)
		if err != nil {
			return "", err
		}
		if buffer == nil {
			buffer = make([]byte, ChunkSize)
		}
		err = c.uploadFile(ctx, id, i, f, e.Size, p.Offsets[i], buffer, func(n int64) {
			completed += n
			c.progress(TransferEvent{Phase: "Uploading", Completed: completed, Total: total, Reused: reused})
		})
		err = errors.Join(err, f.Close())
		if err != nil {
			return "", err
		}
		i++
	}
	c.progress(TransferEvent{Phase: "Verifying", Completed: completed, Total: total, Reused: reused})
	if err = c.json(ctx, "POST", "/v1/collections/"+id+"/finish", nil, &p); err != nil {
		return "", err
	}
	if err = validateProgress(p, m, id); err != nil {
		return "", err
	}
	if !p.Complete {
		return "", ErrConflict
	}
	c.progress(TransferEvent{Phase: "Complete", Completed: total, Total: total, Reused: reused})
	return id, nil
}
func (c *Client) uploadFile(ctx context.Context, id string, index int, f *os.File, size, offset int64, buffer []byte, progress func(int64)) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() != size {
		return errors.New("source size changed; retry to scan a fresh copy")
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	for offset < size {
		n := min(int64(len(buffer)), size-offset)
		if _, err := io.ReadFull(f, buffer[:n]); err != nil {
			return err
		}
		res, err := c.request(ctx, "PUT", fmt.Sprintf("/v1/collections/%s/files/%d", id, index), bytes.NewReader(buffer[:n]), &offset)
		if err != nil {
			return err
		}
		var ack struct {
			Offset *int64 `json:"offset"`
		}
		err = readJSON(ctx, res.Body, &ack)
		res.Body.Close()
		if err != nil {
			return err
		}
		if ack.Offset == nil || *ack.Offset != offset+n {
			return errors.New("invalid upload acknowledgement")
		}
		offset += n
		progress(n)
	}
	return nil
}
