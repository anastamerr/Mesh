// Package storage implements immutable folder copies and resumable file uploads.
package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"strings"
)

const (
	ChunkSize              = 4 << 20
	MaxManifestBytes       = 2 << 20
	MaxEntries             = 10000
	MaxFileBytes     int64 = 1 << 40
)

var (
	ErrInvalid  = errors.New("invalid storage request")
	ErrConflict = errors.New("transfer conflicts with durable progress or checksum")
	ErrNotFound = errors.New("collection not found")
)

type Entry struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
	Directory bool   `json:"directory,omitempty"`
}
type Manifest struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}
type Progress struct {
	ID       string  `json:"id"`
	Complete bool    `json:"complete"`
	Offsets  []int64 `json:"offsets"`
}

// Paths use a conservative, case-insensitive Windows-compatible subset on every
// host so a collection never acquires different meanings after a move.
func validPath(p string) bool {
	if p == "" || len(p) > 1024 || path.Clean(p) != p || strings.HasPrefix(p, "/") {
		return false
	}
	for part := range strings.SplitSeq(p, "/") {
		if part == "." || part == ".." || len(part) > 200 || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") {
			return false
		}
		for _, c := range part {
			if c < 32 || c > 126 || strings.ContainsRune(`<>:"\|?*`, c) {
				return false
			}
		}
		base, _, _ := strings.Cut(part, ".")
		base = strings.ToUpper(base)
		if base == "CON" || base == "PRN" || base == "AUX" || base == "NUL" || base == "CONIN$" || base == "CONOUT$" || base == "CLOCK$" || (len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) && base[3] >= '0' && base[3] <= '9') {
			return false
		}
	}
	return true
}
func validID(id string) bool {
	if len(id) != sha256.Size*2 {
		return false
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func (m Manifest) Validate() error {
	if m.Version != 1 || m.Entries == nil || len(m.Entries) > MaxEntries {
		return ErrInvalid
	}
	seen := make(map[string]int, len(m.Entries))
	previous := ""
	for i, e := range m.Entries {
		if !validPath(e.Path) || e.Path <= previous || e.Size < 0 || e.Size > MaxFileBytes {
			return ErrInvalid
		}
		previous = e.Path
		key := strings.ToLower(e.Path)
		if _, ok := seen[key]; ok {
			return ErrInvalid
		}
		parent := path.Dir(key)
		if parent != "." {
			if index, ok := seen[parent]; !ok || !m.Entries[index].Directory || m.Entries[index].Path != path.Dir(e.Path) {
				return ErrInvalid
			}
		}
		if e.Directory {
			if e.Size != 0 || e.SHA256 != "" {
				return ErrInvalid
			}
		} else if !validID(e.SHA256) {
			return ErrInvalid
		}
		seen[key] = i
	}
	return nil
}
func (m Manifest) encoded() ([]byte, string, error) {
	if err := m.Validate(); err != nil {
		return nil, "", err
	}
	data, err := json.Marshal(m)
	if err != nil || len(data) > MaxManifestBytes {
		return nil, "", ErrInvalid
	}
	hash := sha256.Sum256(data)
	return data, hex.EncodeToString(hash[:]), nil
}

func decodeManifest(data []byte, m *Manifest) error {
	if len(data) > MaxManifestBytes || json.Unmarshal(data, m) != nil {
		return ErrInvalid
	}
	return m.Validate()
}

// ID returns the stable collection identifier used to scope transfer grants.
func (m Manifest) ID() (string, error) {
	_, id, err := m.encoded()
	return id, err
}
