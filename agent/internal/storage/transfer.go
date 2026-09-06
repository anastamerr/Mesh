package storage

import (
	"context"
	"os"
)

// PreparedFolder keeps the source root open between hashing and upload. The
// managed CLI can obtain a scoped grant without scanning the folder again.
type PreparedFolder struct {
	Root     *os.Root
	Manifest Manifest
}

func Prepare(ctx context.Context, source string) (*PreparedFolder, error) {
	root, err := os.OpenRoot(source)
	if err != nil {
		return nil, err
	}
	manifest, err := Scan(ctx, root)
	if err != nil {
		root.Close()
		return nil, err
	}
	return &PreparedFolder{Root: root, Manifest: manifest}, nil
}
func (f *PreparedFolder) Close() error { return f.Root.Close() }
func (m Manifest) Statistics() (int, int64) {
	count := 0
	var total int64
	for _, e := range m.Entries {
		if !e.Directory {
			count++
			total += e.Size
		}
	}
	return count, total
}

type TransferEvent struct {
	Phase     string
	Completed int64
	Total     int64
	Reused    int64
}

func (c *Client) progress(event TransferEvent) {
	if c.Progress != nil {
		c.Progress(event)
	}
}
