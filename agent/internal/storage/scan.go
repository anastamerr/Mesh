package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
)

// Scan rejects links and special files; ordinary files are streamed for hashes.
// The source is read again on upload, and the server verifies it against this
// manifest, so concurrent source changes cannot silently produce a bad copy.
func Scan(ctx context.Context, root *os.Root) (Manifest, error) {
	m := Manifest{Version: 1, Entries: []Entry{}}
	err := fs.WalkDir(root.FS(), ".", func(name string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		if !validPath(name) {
			return fmt.Errorf("unsupported file name %q: this release requires Windows-compatible ASCII paths", name)
		}
		if len(m.Entries) >= MaxEntries {
			return fmt.Errorf("folder exceeds the limit of %d entries", MaxEntries)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		e := Entry{Path: name, Directory: info.IsDir()}
		if e.Directory {
			m.Entries = append(m.Entries, e)
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("cannot copy %q: links and special files are not supported", name)
		}
		if info.Size() > MaxFileBytes {
			return fmt.Errorf("file %q exceeds the 1 TiB limit", name)
		}
		f, err := regular(root, name, os.O_RDONLY)
		if err != nil {
			return err
		}
		h := sha256.New()
		n, err := io.Copy(h, &contextReader{ctx: ctx, r: io.LimitReader(f, MaxFileBytes+1)})
		err = errors.Join(err, f.Close())
		if err != nil {
			return err
		}
		if n != info.Size() {
			return errors.New("source changed while building manifest")
		}
		e.Size = n
		e.SHA256 = hex.EncodeToString(h.Sum(nil))
		m.Entries = append(m.Entries, e)
		return nil
	})
	if err != nil {
		return m, err
	}
	sort.Slice(m.Entries, func(i, j int) bool { return m.Entries[i].Path < m.Entries[j].Path })
	return m, m.Validate()
}
