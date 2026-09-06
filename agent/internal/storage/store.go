package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/gofrs/flock"
	_ "modernc.org/sqlite"
)

type Store struct {
	// Manifests are immutable by content ID. Keep only the most recently read
	// manifest; authorization and publication state are never cached.
	manifestMu     sync.Mutex
	cachedID       string
	cachedManifest Manifest
	mu             sync.Mutex
	root           *os.Root
	db             *sql.DB
	lock           *flock.Flock
}

// Open requires a dedicated directory owned by the agent account. SQLite and
// lock files are local trusted state; uploaded paths never address them.
func Open(dir string) (_ *Store, err error) {
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		return nil, errors.New("storage root must be a real, private directory (0700 on Unix)")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	s := &Store{root: root}
	defer func() {
		if err != nil {
			_ = s.Close()
		}
	}()
	for _, name := range []string{".mesh", "staging", "collections"} {
		if err = root.MkdirAll(name, 0700); err != nil {
			return nil, err
		}
		info, err = root.Lstat(name)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
			return nil, ErrInvalid
		}
	}
	for _, name := range []string{"journal.db", "journal.db-wal", "journal.db-shm", "storage.lock"} {
		info, statErr := root.Lstat(".mesh/" + name)
		if statErr == nil && !info.Mode().IsRegular() {
			return nil, ErrInvalid
		}
		if statErr != nil && !os.IsNotExist(statErr) {
			return nil, statErr
		}
	}
	s.lock = flock.New(filepath.Join(dir, ".mesh", "storage.lock"))
	locked, err := s.lock.TryLock()
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, errors.New("storage root is already in use")
	}
	journal, err := filepath.Abs(filepath.Join(dir, ".mesh", "journal.db"))
	if err != nil {
		return nil, err
	}
	journal = filepath.ToSlash(journal)
	if !strings.HasPrefix(journal, "/") {
		journal = "/" + journal
	}
	// Connection-local pragmas must also apply when database/sql replaces a connection.
	uri := url.URL{Scheme: "file", Path: journal, RawQuery: url.Values{
		"_pragma": {"synchronous(FULL)", "foreign_keys(ON)"},
	}.Encode()}
	s.db, err = sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	s.db.SetMaxOpenConns(1)
	var version int
	if err = s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return nil, err
	}
	if version > 1 {
		return nil, errors.New("storage journal requires a newer agent")
	}
	_, err = s.db.Exec(`PRAGMA journal_mode=WAL;
 CREATE TABLE IF NOT EXISTS collections (id TEXT PRIMARY KEY, manifest BLOB NOT NULL, complete INTEGER NOT NULL DEFAULT 0 CHECK(complete IN (0,1)));
 CREATE TABLE IF NOT EXISTS files (collection TEXT NOT NULL REFERENCES collections(id), ordinal INTEGER NOT NULL, offset INTEGER NOT NULL DEFAULT 0 CHECK(offset >= 0), PRIMARY KEY(collection,ordinal)); PRAGMA user_version=1;`)
	if err != nil {
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error {
	var errs []error
	if s.db != nil {
		errs = append(errs, s.db.Close())
	}
	if s.root != nil {
		errs = append(errs, s.root.Close())
	}
	if s.lock != nil {
		errs = append(errs, s.lock.Close())
	}
	return errors.Join(errs...)
}

func (s *Store) Begin(ctx context.Context, m Manifest) (Progress, error) {
	data, id, err := m.encoded()
	if err != nil {
		return Progress{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Progress{}, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "INSERT INTO collections(id,manifest) VALUES(?,?) ON CONFLICT(id) DO NOTHING", id, data)
	if err != nil {
		return Progress{}, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return Progress{}, err
	}
	if inserted != 0 {
		statement, err := tx.PrepareContext(ctx, "INSERT INTO files(collection,ordinal) VALUES(?,?)")
		if err != nil {
			return Progress{}, err
		}
		defer statement.Close()
		for i := range m.Entries {
			if _, err = statement.ExecContext(ctx, id, i); err != nil {
				return Progress{}, err
			}
		}
	}
	if err = tx.Commit(); err != nil {
		return Progress{}, err
	}
	var complete bool
	if err := s.db.QueryRowContext(ctx, "SELECT complete FROM collections WHERE id=?", id).Scan(&complete); err != nil {
		return Progress{}, err
	}
	return s.progress(ctx, id, m, complete)
}
func (s *Store) manifest(ctx context.Context, id string, published bool) (Manifest, bool, error) {
	var m Manifest
	var complete bool
	if !validID(id) {
		return m, false, ErrInvalid
	}
	err := s.db.QueryRowContext(ctx, "SELECT complete FROM collections WHERE id=?", id).Scan(&complete)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && published && !complete) {
		return m, false, ErrNotFound
	}
	if err != nil {
		return m, false, err
	}
	s.manifestMu.Lock()
	defer s.manifestMu.Unlock()
	if s.cachedID == id {
		return s.cachedManifest, complete, nil
	}
	var data []byte
	if err = s.db.QueryRowContext(ctx, "SELECT manifest FROM collections WHERE id=?", id).Scan(&data); err != nil {
		return m, false, err
	}
	if err = decodeManifest(data, &m); err != nil {
		return m, false, fmt.Errorf("corrupt collection journal: %w", err)
	}
	s.cachedID, s.cachedManifest = id, m
	return m, complete, nil
}
func (s *Store) progress(ctx context.Context, id string, m Manifest, complete bool) (Progress, error) {
	p := Progress{ID: id, Complete: complete, Offsets: make([]int64, len(m.Entries))}
	rows, err := s.db.QueryContext(ctx, "SELECT ordinal,offset FROM files WHERE collection=? ORDER BY ordinal", id)
	if err != nil {
		return p, err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var i int
		var offset int64
		if err = rows.Scan(&i, &offset); err != nil {
			return p, err
		}
		if i != n || i >= len(m.Entries) || offset < 0 || offset > m.Entries[i].Size {
			return p, ErrConflict
		}
		p.Offsets[i] = offset
		n++
	}
	if err = rows.Err(); err != nil {
		return p, err
	}
	if n != len(m.Entries) {
		return p, ErrConflict
	}
	return p, nil
}
func (s *Store) List(ctx context.Context, after string) ([]string, error) {
	if after != "" && !validID(after) {
		return nil, ErrInvalid
	}
	rows, err := s.db.QueryContext(ctx, "SELECT id FROM collections WHERE complete=1 AND id>? ORDER BY id LIMIT 100", after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
