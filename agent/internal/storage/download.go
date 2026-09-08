package storage

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/gofrs/flock"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const downloadStateVersion = 1

type downloadState struct {
	Version     int    `json:"version"`
	Collection  string `json:"collection"`
	Destination string `json:"destination"`
	Staging     string `json:"staging"`
}

type downloadCheckpoint struct {
	Index  int    `json:"index"`
	Offset int64  `json:"offset"`
	SHA256 string `json:"sha256"`
}

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

// DownloadResumable retrieves a collection through an owned sibling staging
// directory. Synced prefix checkpoints survive cancellation and process exit;
// the destination only appears after complete manifest verification.
func (c *Client) DownloadResumable(ctx context.Context, id, destination string) error {
	if !validID(id) {
		return ErrInvalid
	}
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	absolute = filepath.Clean(absolute)
	parentName, destinationName := filepath.Dir(absolute), filepath.Base(absolute)
	if destinationName == "." || destinationName == string(filepath.Separator) {
		return ErrInvalid
	}
	var m Manifest
	if err = c.json(ctx, "GET", "/v1/collections/"+id, nil, &m); err != nil {
		return err
	}
	if _, actual, encodeErr := m.encoded(); encodeErr != nil || actual != id {
		return ErrConflict
	}
	parent, err := os.OpenRoot(parentName)
	if err != nil {
		return err
	}
	defer parent.Close()
	stateName := "." + destinationName + ".mesh-download"
	lockName := filepath.Join(parentName, stateName+".lock")
	if info, err := os.Lstat(lockName); err == nil && !info.Mode().IsRegular() {
		return ErrConflict
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	lock := flock.New(lockName)
	locked, err := lock.TryLock()
	if err != nil {
		_ = lock.Close()
		return err
	}
	if !locked {
		_ = lock.Close()
		return errors.New("another process is using this download destination")
	}
	defer lock.Close()
	state, checkpoints, created, err := openDownloadState(parent, stateName, destinationName, id)
	if err != nil {
		return err
	}
	if info, statErr := parent.Lstat(destinationName); statErr == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrConflict
		}
		if created {
			_ = parent.Remove(stateName)
			return errors.New("destination already exists; choose a new folder")
		}
		if err = verifyDownloadedTree(ctx, parent, destinationName, m); err != nil {
			return fmt.Errorf("destination exists but does not match the saved download: %w", err)
		}
		if err = parent.Remove(stateName); err == nil {
			err = syncDirectory(parent, ".")
		}
		return err
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	if created {
		if err = parent.Mkdir(state.Staging, 0700); err != nil {
			_ = parent.Remove(stateName)
			return err
		}
		if err = syncDirectory(parent, "."); err != nil {
			return err
		}
	}
	if info, statErr := parent.Lstat(state.Staging); statErr == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return ErrConflict
	} else if statErr != nil && !os.IsNotExist(statErr) {
		return statErr
	}
	root, err := parent.OpenRoot(state.Staging)
	if os.IsNotExist(err) && !created {
		err = parent.Mkdir(state.Staging, 0700)
		if err == nil {
			err = syncDirectory(parent, ".")
		}
		if err == nil {
			root, err = parent.OpenRoot(state.Staging)
		}
	}
	if err != nil {
		return fmt.Errorf("saved download staging is unavailable: %w", err)
	}
	journal, err := parent.OpenFile(stateName, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		root.Close()
		return err
	}
	if err = c.resumeDownload(ctx, root, journal, m, id, checkpoints); err != nil {
		journal.Close()
		root.Close()
		return err
	}
	if err = errors.Join(journal.Close(), syncTree(root, m), root.Close()); err != nil {
		return err
	}
	if err = publishDownload(parent, state.Staging, destinationName); err != nil {
		return err
	}
	if err = syncDirectory(parent, "."); err != nil {
		return err
	}
	if err = parent.Remove(stateName); err != nil {
		return err
	}
	if err = syncDirectory(parent, "."); err != nil {
		return err
	}
	_, total := m.Statistics()
	c.progress(TransferEvent{Phase: "Complete", Completed: total, Total: total})
	return nil
}

func openDownloadState(parent *os.Root, name, destination, id string) (downloadState, map[int]downloadCheckpoint, bool, error) {
	if info, err := parent.Lstat(name); err == nil && (!info.Mode().IsRegular() || info.Size() > 64<<20) {
		return downloadState{}, nil, false, ErrConflict
	} else if err != nil && !os.IsNotExist(err) {
		return downloadState{}, nil, false, err
	}
	f, err := parent.OpenFile(name, os.O_RDWR, 0)
	if os.IsNotExist(err) {
		var token [12]byte
		if _, err = rand.Read(token[:]); err != nil {
			return downloadState{}, nil, false, err
		}
		state := downloadState{Version: downloadStateVersion, Collection: id, Destination: destination, Staging: ".mesh-download-" + hex.EncodeToString(token[:])}
		f, err = parent.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return downloadState{}, nil, false, err
		}
		encodeErr := json.NewEncoder(f).Encode(state)
		syncErr := f.Sync()
		closeErr := f.Close()
		if err = errors.Join(encodeErr, syncErr, closeErr); err != nil {
			_ = parent.Remove(name)
			return downloadState{}, nil, false, err
		}
		return state, make(map[int]downloadCheckpoint), true, nil
	}
	if err != nil {
		return downloadState{}, nil, false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return downloadState{}, nil, false, ErrConflict
	}
	reader := bufio.NewReaderSize(f, 4096)
	header, readErr := reader.ReadSlice('\n')
	if readErr != nil || len(header) > 4096 {
		return downloadState{}, nil, false, errors.New("saved download state is corrupt; move it aside to restart safely")
	}
	var state downloadState
	if json.Unmarshal(header, &state) != nil || state.Version != downloadStateVersion || state.Collection != id || state.Destination != destination || !validDownloadStaging(state.Staging) {
		return downloadState{}, nil, false, errors.New("saved download state belongs to a different or corrupt download")
	}
	checkpoints := make(map[int]downloadCheckpoint)
	validBytes := int64(len(header))
	for {
		line, lineErr := reader.ReadSlice('\n')
		if errors.Is(lineErr, io.EOF) {
			// A killed append may leave an incomplete final record. Its file tail
			// is intentionally ignored and truncated to the prior checkpoint later.
			if len(line) > 0 {
				if err = f.Truncate(validBytes); err != nil {
					return downloadState{}, nil, false, err
				}
				if err = f.Sync(); err != nil {
					return downloadState{}, nil, false, err
				}
			}
			break
		}
		if lineErr != nil || len(line) > 4096 {
			return downloadState{}, nil, false, errors.New("saved download state is corrupt; move it aside to restart safely")
		}
		var checkpoint downloadCheckpoint
		if json.Unmarshal(line, &checkpoint) != nil || checkpoint.Index < 0 || checkpoint.Index >= MaxEntries || checkpoint.Offset < 0 || !validID(checkpoint.SHA256) {
			return downloadState{}, nil, false, errors.New("saved download state is corrupt; move it aside to restart safely")
		}
		checkpoints[checkpoint.Index] = checkpoint
		validBytes += int64(len(line))
	}
	return state, checkpoints, false, nil
}

func validDownloadStaging(name string) bool {
	const prefix = ".mesh-download-"
	if !strings.HasPrefix(name, prefix) || len(name) != len(prefix)+24 {
		return false
	}
	for _, c := range name[len(prefix):] {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func appendCheckpoint(journal *os.File, checkpoint downloadCheckpoint) error {
	if err := json.NewEncoder(journal).Encode(checkpoint); err != nil {
		return err
	}
	return journal.Sync()
}

func (c *Client) resumeDownload(ctx context.Context, root *os.Root, journal *os.File, m Manifest, id string, checkpoints map[int]downloadCheckpoint) error {
	for index, checkpoint := range checkpoints {
		if index < 0 || index >= len(m.Entries) || m.Entries[index].Directory || m.Entries[index].Size == 0 || checkpoint.Offset > m.Entries[index].Size || checkpoint.Offset == 0 && checkpoint.SHA256 != emptySHA256() {
			return errors.New("saved download state does not match the collection")
		}
	}
	offsets := make([]int64, len(m.Entries))
	_, total := m.Statistics()
	var completed, reused int64
	for i, e := range m.Entries {
		if e.Directory {
			if err := ensureDirectory(root, e.Path); err != nil {
				return err
			}
			continue
		}
		if e.Size == 0 {
			if err := ensureCompleteFile(ctx, root, e); err != nil {
				return err
			}
			continue
		}
		offset, err := recoverFilePrefix(ctx, root, e, checkpoints[i])
		if err != nil {
			return err
		}
		offsets[i] = offset
		if offset == 0 {
			if err := root.Remove(e.Path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		completed += offset
		reused += offset
	}
	c.progress(TransferEvent{Phase: "Downloading", Completed: completed, Total: total, Reused: reused})
	progress := func(n int64) {
		completed += n
		c.progress(TransferEvent{Phase: "Downloading", Completed: completed, Total: total, Reused: reused})
	}
	for i := 0; i < len(m.Entries); {
		e := m.Entries[i]
		if e.Directory || e.Size == 0 || offsets[i] == e.Size {
			i++
			continue
		}
		if c.batch && offsets[i] == 0 {
			indices := batchIndices(m, i, offsets)
			if len(indices) > 1 {
				if err := c.downloadBatch(ctx, root, m, id, indices, progress); err != nil {
					return err
				}
				for _, index := range indices {
					offsets[index] = m.Entries[index].Size
				}
				i = indices[len(indices)-1] + 1
				continue
			}
		}
		before := offsets[i]
		startCompleted := completed
		if err := c.downloadFileResumable(ctx, root, journal, id, i, e, before, progress); err != nil {
			if !errors.Is(err, ErrConflict) {
				return err
			}
			completed = startCompleted - before
			reused -= before
			if truncateErr := truncateDownload(root, e.Path); truncateErr != nil {
				return errors.Join(err, truncateErr)
			}
			if checkpointErr := appendCheckpoint(journal, downloadCheckpoint{Index: i, Offset: 0, SHA256: emptySHA256()}); checkpointErr != nil {
				return checkpointErr
			}
			if retryErr := c.downloadFileResumable(ctx, root, journal, id, i, e, 0, progress); retryErr != nil {
				return retryErr
			}
		}
		offsets[i] = e.Size
		i++
	}
	c.progress(TransferEvent{Phase: "Verifying", Completed: total, Total: total, Reused: reused})
	return verifyDownloadedRoot(ctx, root, m)
}

func ensureDirectory(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if os.IsNotExist(err) {
		if err = root.MkdirAll(name, 0700); err != nil {
			return err
		}
		info, err = root.Lstat(name)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrConflict
	}
	return nil
}

func ensureCompleteFile(ctx context.Context, root *os.Root, e Entry) error {
	info, err := root.Lstat(e.Path)
	if os.IsNotExist(err) {
		return receiveFile(ctx, root, e, bytes.NewReader(nil), func(int64) {})
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return ErrConflict
	}
	f, err := root.OpenFile(e.Path, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	err = verify(ctx, f, e)
	return errors.Join(err, f.Close())
}

func recoverFilePrefix(ctx context.Context, root *os.Root, e Entry, checkpoint downloadCheckpoint) (int64, error) {
	info, err := root.Lstat(e.Path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, ErrConflict
	}
	f, err := root.OpenFile(e.Path, os.O_RDWR, 0600)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return 0, ErrConflict
	}
	if checkpoint.Offset == 0 && info.Size() == e.Size {
		if err = verify(ctx, f, e); err == nil {
			return e.Size, nil
		}
	}
	if checkpoint.Offset == e.Size && checkpoint.SHA256 == e.SHA256 && info.Size() == e.Size {
		if err = verify(ctx, f, e); err == nil {
			return e.Size, nil
		}
	}
	if checkpoint.Offset == e.Size {
		return 0, f.Truncate(0)
	}
	if checkpoint.Offset <= 0 || checkpoint.Offset > e.Size || info.Size() < checkpoint.Offset {
		return 0, f.Truncate(0)
	}
	if info.Size() > checkpoint.Offset {
		if err = f.Truncate(checkpoint.Offset); err != nil {
			return 0, err
		}
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	h := sha256.New()
	if _, err = io.CopyN(h, &contextReader{ctx: ctx, r: f}, checkpoint.Offset); err != nil {
		return 0, err
	}
	if hex.EncodeToString(h.Sum(nil)) != checkpoint.SHA256 {
		return 0, f.Truncate(0)
	}
	return checkpoint.Offset, nil
}

func (c *Client) downloadFileResumable(ctx context.Context, root *os.Root, journal *os.File, id string, index int, e Entry, offset int64, progress func(int64)) (err error) {
	f, err := root.OpenFile(e.Path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if err = f.Truncate(offset); err != nil {
		return err
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	h := sha256.New()
	if offset > 0 {
		if _, err = io.CopyN(h, &contextReader{ctx: ctx, r: f}, offset); err != nil {
			return err
		}
	}
	res, requestErr := c.requestRange(ctx, "GET", fmt.Sprintf("/v1/collections/%s/files/%d", id, index), nil, nil, fmt.Sprintf("bytes=%d-", offset))
	if requestErr != nil {
		return requestErr
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusOK {
		// Older peers may ignore Range. A fresh download remains compatible;
		// a saved prefix is retried from zero by the caller.
		if offset != 0 {
			return ErrConflict
		}
	} else if res.Header.Get("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", offset, e.Size-1, e.Size) {
		return ErrInvalid
	}
	if res.ContentLength >= 0 && res.ContentLength != e.Size-offset {
		return ErrInvalid
	}
	if _, err = f.Seek(offset, io.SeekStart); err != nil {
		return err
	}
	buffer := make([]byte, 32*1024)
	for offset < e.Size {
		expected := min(ChunkSize, e.Size-offset)
		writer := io.MultiWriter(f, h)
		n, copyErr := io.CopyBuffer(writer, &contextReader{ctx: ctx, r: io.LimitReader(res.Body, expected)}, buffer)
		if n > 0 {
			offset += n
			progress(n)
			if syncErr := f.Sync(); syncErr != nil {
				return syncErr
			}
			if checkpointErr := appendCheckpoint(journal, downloadCheckpoint{Index: index, Offset: offset, SHA256: hex.EncodeToString(h.Sum(nil))}); checkpointErr != nil {
				return checkpointErr
			}
		}
		if copyErr != nil || n != expected {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &UnavailableError{Reason: "storage response interrupted; rerun the same command to resume"}
		}
	}
	var extra [1]byte
	if n, err := io.ReadFull(res.Body, extra[:]); n != 0 || err != io.EOF {
		return ErrInvalid
	}
	if hex.EncodeToString(h.Sum(nil)) != e.SHA256 {
		return ErrConflict
	}
	return nil
}

func truncateDownload(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return ErrConflict
	}
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	return errors.Join(f.Sync(), f.Close())
}

func emptySHA256() string { sum := sha256.Sum256(nil); return hex.EncodeToString(sum[:]) }

func verifyDownloadedTree(ctx context.Context, parent *os.Root, name string, m Manifest) error {
	root, err := parent.OpenRoot(name)
	if err != nil {
		return err
	}
	defer root.Close()
	return verifyDownloadedRoot(ctx, root, m)
}

func verifyDownloadedRoot(ctx context.Context, root *os.Root, m Manifest) error {
	expected := make(map[string]struct{}, len(m.Entries))
	for _, e := range m.Entries {
		expected[e.Path] = struct{}{}
		if e.Directory {
			info, err := root.Lstat(e.Path)
			if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return ErrConflict
			}
			continue
		}
		info, err := root.Lstat(e.Path)
		if err != nil || !info.Mode().IsRegular() {
			return ErrConflict
		}
		f, err := root.OpenFile(e.Path, os.O_RDONLY, 0)
		if err != nil {
			return err
		}
		err = errors.Join(verify(ctx, f, e), f.Close())
		if err != nil {
			return err
		}
	}
	return verifyNoUnexpected(ctx, root, ".", expected)
}

func verifyNoUnexpected(ctx context.Context, root *os.Root, directory string, expected map[string]struct{}) error {
	f, err := root.Open(directory)
	if err != nil {
		return err
	}
	var visitErr error
	for visitErr == nil {
		if err = ctx.Err(); err != nil {
			visitErr = err
			break
		}
		entries, readErr := f.ReadDir(128)
		for _, entry := range entries {
			name := entry.Name()
			if directory != "." {
				name = path.Join(directory, name)
			}
			if _, ok := expected[name]; !ok {
				visitErr = ErrConflict
				break
			}
			info, statErr := root.Lstat(name)
			if statErr != nil {
				visitErr = statErr
				break
			}
			if info.Mode()&os.ModeSymlink != 0 {
				visitErr = ErrConflict
				break
			}
			if info.IsDir() {
				visitErr = verifyNoUnexpected(ctx, root, name, expected)
				if visitErr != nil {
					break
				}
			}
		}
		if visitErr != nil || readErr == io.EOF {
			break
		}
		if readErr != nil {
			visitErr = readErr
			break
		}
	}
	return errors.Join(visitErr, f.Close())
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
