// Package state stores one host identity and serializes agent processes.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"time"

	"github.com/gofrs/flock"
)

const MaxSequence = uint64(9007199254740991)
const maxStateBytes = 64 * 1024

var nodeID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
var credential = regexp.MustCompile(`^mesh_node_[A-Za-z0-9_-]{43}$`)

type State struct {
	Version      int       `json:"version"`
	Server       string    `json:"server"`
	NodeID       string    `json:"nodeId"`
	Name         string    `json:"name"`
	Credential   string    `json:"credential"`
	ExpiresAt    time.Time `json:"credentialExpiresAt"`
	NextSequence uint64    `json:"nextSequence"`
}

func (s State) Validate() error {
	if s.Version != 1 || !nodeID.MatchString(s.NodeID) || !credential.MatchString(s.Credential) ||
		s.Server == "" || s.ExpiresAt.IsZero() || s.NextSequence > MaxSequence {
		return errors.New("invalid agent identity; recover or re-enroll rather than resetting its sequence")
	}
	return nil
}

type Store struct {
	dir  string
	lock *flock.Flock
}

func DefaultDir() (string, error) {
	dir, err := os.UserConfigDir()
	return filepath.Join(dir, "Mesh", "agent"), err
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("state directory must be a real directory")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("state directory must have permissions 0700")
	}
	for _, name := range []string{"state.json", "agent.lock"} {
		if info, err := os.Lstat(filepath.Join(dir, name)); err == nil && !info.Mode().IsRegular() {
			return nil, errors.New("state and lock paths must be regular files")
		} else if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
	}
	lock := flock.New(filepath.Join(dir, "agent.lock"))
	locked, err := lock.TryLock()
	if err != nil {
		lock.Close()
		return nil, fmt.Errorf("lock agent state: %w", err)
	}
	if !locked {
		lock.Close()
		return nil, errors.New("another agent command is using this state directory")
	}
	return &Store{dir: dir, lock: lock}, nil
}

func (s *Store) Close() error { return s.lock.Close() }

func (s *Store) Load() (State, error) {
	var value State
	path := filepath.Join(s.dir, "state.json")
	info, err := os.Lstat(path)
	if err != nil {
		return value, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxStateBytes ||
		(runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return value, errors.New("state must be a small regular file with private permissions")
	}
	file, err := os.Open(path)
	if err != nil {
		return value, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxStateBytes+1))
	if err != nil {
		return value, err
	}
	if len(data) > maxStateBytes {
		return value, errors.New("agent state exceeds size limit")
	}
	data, err = unprotect(data)
	if err != nil {
		return value, errors.New("cannot decrypt state for the current OS user")
	}
	// Distinguish an explicitly stored zero from a missing/null counter. Go's
	// default unmarshalling otherwise silently resets a damaged identity to zero.
	var document struct {
		State
		NextSequence *uint64 `json:"nextSequence"`
	}
	if json.Unmarshal(data, &document) != nil || document.NextSequence == nil {
		return value, errors.New("agent state is corrupt; it was not reset")
	}
	value = document.State
	value.NextSequence = *document.NextSequence
	return value, value.Validate()
}

func (s *Store) Save(value State) error {
	if err := value.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	data, err = protect(data)
	if err != nil {
		return errors.New("cannot protect agent state")
	}
	f, err := os.CreateTemp(s.dir, ".state-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(s.dir, "state.json")); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		dir, err := os.Open(s.dir)
		if err != nil {
			return err
		}
		defer dir.Close()
		return dir.Sync()
	}
	return nil
}
