package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"runtime"
)

func syncDirectory(root *os.Root, name string) error {
	if runtime.GOOS == "windows" {
		return nil
	} // Windows does not support directory fsync.
	f, err := root.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func syncParents(root *os.Root, name string) error {
	for dir := path.Dir(name); ; dir = path.Dir(dir) {
		if err := syncDirectory(root, dir); err != nil {
			return err
		}
		if dir == "." {
			return nil
		}
	}
}

// Manifests contain every directory, sorted with parents before children.
// Sync each directory once after its children, then the collection root.
func syncTree(root *os.Root, m Manifest) error {
	for i := len(m.Entries) - 1; i >= 0; i-- {
		if m.Entries[i].Directory {
			if err := syncDirectory(root, m.Entries[i].Path); err != nil {
				return err
			}
		}
	}
	return syncDirectory(root, ".")
}

func (s *Store) staging(id string) (*os.Root, error) {
	name := "staging/" + id
	if err := s.root.MkdirAll(name, 0700); err != nil {
		return nil, err
	}
	if err := syncParents(s.root, name); err != nil {
		return nil, err
	}
	return s.root.OpenRoot(name)
}
func regular(root *os.Root, name string, flags int) (*os.File, error) {
	info, err := root.Lstat(name)
	if err == nil && !info.Mode().IsRegular() {
		return nil, ErrConflict
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f, err := root.OpenFile(name, flags, 0600)
	if err != nil {
		return nil, err
	}
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, ErrConflict
	}
	return f, nil
}
func (s *Store) Write(ctx context.Context, id string, index int, offset int64, data []byte) (int64, error) {
	if len(data) == 0 || len(data) > ChunkSize || offset < 0 {
		return 0, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m, complete, err := s.manifest(ctx, id, false)
	if err != nil {
		return 0, err
	}
	if complete || index < 0 || index >= len(m.Entries) {
		return 0, ErrConflict
	}
	e := m.Entries[index]
	if e.Directory || offset > e.Size || int64(len(data)) > e.Size-offset {
		return 0, ErrInvalid
	}
	var durable int64
	if err = s.db.QueryRowContext(ctx, "SELECT offset FROM files WHERE collection=? AND ordinal=?", id, index).Scan(&durable); err != nil {
		return 0, err
	}
	if offset != durable {
		return 0, ErrConflict
	}
	root, err := s.staging(id)
	if err != nil {
		return 0, err
	}
	defer root.Close()
	reset, err := writeFile(ctx, root, e, durable, data)
	if reset {
		if _, resetErr := s.db.ExecContext(ctx, "UPDATE files SET offset=0 WHERE collection=? AND ordinal=?", id, index); resetErr != nil {
			return 0, resetErr
		}
	}
	if err != nil {
		return 0, err
	}
	if err = syncParents(root, e.Path); err != nil {
		return 0, err
	}
	next := durable + int64(len(data))
	_, err = s.db.ExecContext(ctx, "UPDATE files SET offset=? WHERE collection=? AND ordinal=?", next, id, index)
	return next, err
}

// File bytes are synced before callers persist offsets. Directory entries and
// journal commits are handled once per acknowledgement (one chunk or a batch).
func writeFile(ctx context.Context, root *os.Root, e Entry, durable int64, data []byte) (reset bool, err error) {
	if err = root.MkdirAll(path.Dir(e.Path), 0700); err != nil {
		return false, err
	}
	f, err := regular(root, e.Path, os.O_CREATE|os.O_RDWR)
	if err != nil {
		return false, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	if info.Size() < durable {
		return false, ErrConflict
	}
	// Discard a tail written before a lost journal commit.
	if info.Size() > durable {
		if err = f.Truncate(durable); err != nil {
			return false, err
		}
	}
	if _, err = f.WriteAt(data, durable); err != nil {
		return false, err
	}
	if err = f.Sync(); err != nil {
		return false, err
	}
	if durable+int64(len(data)) == e.Size {
		if _, err = f.Seek(0, io.SeekStart); err != nil {
			return false, err
		}
		if err = verify(ctx, f, e); err != nil {
			return true, err
		}
	}
	return false, nil
}

func verify(ctx context.Context, r io.Reader, e Entry) error {
	h := sha256.New()
	n, err := io.Copy(h, &contextReader{ctx: ctx, r: io.LimitReader(r, e.Size+1)})
	if err != nil {
		return err
	}
	if n != e.Size || hex.EncodeToString(h.Sum(nil)) != e.SHA256 {
		return ErrConflict
	}
	return nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func (s *Store) Finish(ctx context.Context, id string) (Progress, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, complete, err := s.manifest(ctx, id, false)
	if err != nil {
		return Progress{}, err
	}
	p, err := s.progress(ctx, id, m, complete)
	if err != nil || complete {
		return p, err
	}
	for i, e := range m.Entries {
		if p.Offsets[i] != e.Size {
			return p, ErrConflict
		}
	}
	// Recover publication if rename succeeded but its SQLite commit did not.
	root, err := s.root.OpenRoot("collections/" + id)
	published := err == nil
	if err != nil && !os.IsNotExist(err) {
		return p, err
	}
	if !published {
		root, err = s.staging(id)
		if err != nil {
			return p, err
		}
	}
	defer func() {
		if root != nil {
			_ = root.Close()
		}
	}()
	for _, e := range m.Entries {
		if e.Directory {
			if !published {
				err = root.MkdirAll(e.Path, 0700)
			} else {
				var info os.FileInfo
				info, err = root.Stat(e.Path)
				if err == nil && !info.IsDir() {
					err = ErrConflict
				}
			}
			if err != nil {
				return p, err
			}
			continue
		}
		flags := os.O_RDONLY
		if !published && e.Size == 0 {
			flags = os.O_CREATE | os.O_RDWR
		}
		f, openErr := regular(root, e.Path, flags)
		if openErr != nil {
			return p, openErr
		}
		err = verify(ctx, f, e)
		if err == nil && !published && e.Size == 0 {
			err = f.Sync()
		}
		err = errors.Join(err, f.Close())
		if err != nil {
			return p, err
		}
	}
	if !published {
		if err = syncTree(root, m); err != nil {
			return p, err
		}
		// Close the subtree handle before rename for Windows compatibility.
		err = root.Close()
		root = nil
		if err != nil {
			return p, err
		}
		if err = s.root.Rename("staging/"+id, "collections/"+id); err != nil {
			return p, err
		}
	}
	for _, dir := range []string{"staging", "collections"} {
		if err = syncDirectory(s.root, dir); err != nil {
			return p, err
		}
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE collections SET complete=1 WHERE id=?", id); err != nil {
		return p, err
	}
	p.Complete = true
	return p, nil
}
func (s *Store) Read(ctx context.Context, id string, index int) (*os.File, Entry, error) {
	m, _, err := s.manifest(ctx, id, true)
	if err != nil {
		return nil, Entry{}, err
	}
	if index < 0 || index >= len(m.Entries) || m.Entries[index].Directory {
		return nil, Entry{}, ErrInvalid
	}
	root, err := s.root.OpenRoot("collections/" + id)
	if err != nil {
		return nil, Entry{}, err
	}
	defer root.Close()
	e := m.Entries[index]
	f, err := regular(root, e.Path, os.O_RDONLY)
	return f, e, err
}
