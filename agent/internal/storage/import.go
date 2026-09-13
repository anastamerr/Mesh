package storage

import (
	"context"
	"errors"
	"io"
	"os"
)

// Import copies a local folder into the same immutable, verified collection
// format used by remote uploads. Durable offsets make repeating an interrupted
// import safe without publishing partial output.
func (store *Store) Import(ctx context.Context, source string) (string, Manifest, error) {
	folder, err := Prepare(ctx, source, nil)
	if err != nil {
		return "", Manifest{}, err
	}
	defer folder.Close()
	id, err := store.importPrepared(ctx, folder)
	return id, folder.Manifest, err
}

func (store *Store) importPrepared(ctx context.Context, folder *PreparedFolder) (string, error) {
	manifest := folder.Manifest
	progress, err := store.Begin(ctx, manifest)
	if err != nil {
		return "", err
	}
	id := progress.ID
	if progress.Complete {
		return id, nil
	}
	buffer := make([]byte, ChunkSize)
	for index, entry := range manifest.Entries {
		offset := progress.Offsets[index]
		if entry.Directory || offset == entry.Size {
			continue
		}
		file, openError := regular(folder.Root, entry.Path, os.O_RDONLY)
		if openError != nil {
			return "", openError
		}
		info, statError := file.Stat()
		if statError != nil || info.Size() != entry.Size {
			file.Close()
			return "", errors.New("job output changed while it was being imported")
		}
		if _, err = file.Seek(offset, io.SeekStart); err != nil {
			file.Close()
			return "", err
		}
		for offset < entry.Size {
			if err = ctx.Err(); err != nil {
				file.Close()
				return "", err
			}
			size := min(int64(len(buffer)), entry.Size-offset)
			if _, err = io.ReadFull(file, buffer[:size]); err != nil {
				file.Close()
				return "", errors.New("job output changed while it was being imported")
			}
			next, writeError := store.Write(ctx, id, index, offset, buffer[:size])
			if writeError != nil {
				file.Close()
				return "", writeError
			}
			if next != offset+size {
				file.Close()
				return "", ErrConflict
			}
			offset = next
		}
		if err = file.Close(); err != nil {
			return "", err
		}
	}
	finished, err := store.Finish(ctx, id)
	if err != nil {
		return "", err
	}
	if !finished.Complete {
		return "", ErrConflict
	}
	return id, nil
}
