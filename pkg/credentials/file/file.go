// Package file is a [credentials.BlobStore] in one file, for local and
// single-process use: one process holds the file at a time, and it keeps the
// whole store in memory. Replicas need a shared database (packages
// credentials/sqlite on one machine, credentials/postgres across machines).
//
// The file holds the store's contents as JSON: per credential its tenant,
// user, slot, kind, version, times and the sealed blob (base64). The blob is
// all the vault gives a store, so no plaintext ever reaches the file. A
// deleted credential stays as a tombstone with no blob, so its version never
// repeats, even across restarts.
//
// [Open] loads the file, creating it with mode 0600 if it does not exist, and
// refuses one that its group or others may read or write ([ErrInsecure]). It
// takes an exclusive lock on path+".lock" (flock), so a second Open of the
// same file, in this process or another, fails with [ErrLocked] rather than
// two writers overwriting each other. Every change writes the whole file
// again, atomically: the new contents go to path+".tmp", which is synced,
// renamed over the file, and the directory synced. A crash at any point
// leaves the old file or the new one, never a mix or a truncated one.
package file

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

var (
	// ErrLocked reports a file another Store, in this process or another,
	// has open.
	ErrLocked = errors.New("credentials file: in use by another process")
	// ErrInsecure reports a file its group or others may
	// read or write.
	ErrInsecure = errors.New("credentials file: open to others")
	// ErrClosed reports a call after Close.
	ErrClosed = errors.New("credentials file: closed")
)

// Mode is the mode the file is created with; Open refuses any other group
// or other bits.
const Mode fs.FileMode = 0o600

// format is the file's format version, in its "format" field.
const format = 1

// Store is a blob store in one file. It is safe for concurrent use. Make one
// with [Open] and [Store.Close] it when done.
type Store struct {
	path string

	mu      sync.Mutex
	lock    *os.File // nil once closed
	entries map[key]entry
}

var _ credentials.BlobStore = (*Store)(nil)

type key struct{ tenant, user, slot string }

// entry is a credential, or the tombstone of a deleted one (Data nil), which
// keeps its last version.
type entry struct {
	meta credentials.Meta
	data []byte
}

// fileData is the file's JSON.
type fileData struct {
	Format      int      `json:"format"`
	Credentials []record `json:"credentials"`
}

type record struct {
	Tenant    string           `json:"tenant"`
	User      string           `json:"user"`
	Slot      string           `json:"slot"`
	Kind      credentials.Kind `json:"kind"`
	Version   int64            `json:"version"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
	Expiry    time.Time        `json:"expiry,omitzero"`
	// Blob is the sealed bytes; a tombstone has none and Deleted set.
	Blob    []byte `json:"blob,omitempty"`
	Deleted bool   `json:"deleted,omitempty"`
}

// Open opens the store in the file at path, creating it (mode 0600) if it
// does not exist. It fails with [ErrLocked] while another Store has the file
// open, and with [ErrInsecure] if the file's mode lets its group or others
// read or write it.
func Open(path string) (*Store, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("credentials file: %w", err)
	}
	lock, err := takeLock(abs + ".lock")
	if err != nil {
		return nil, err
	}
	s := &Store{path: abs, lock: lock, entries: map[key]entry{}}
	if err := s.load(); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return s, nil
}

// takeLock opens and locks the lock file at path, creating it. The lock
// file holds nothing; the lock is on its open descriptor, which the kernel
// releases when the process ends, however it ends.
func takeLock(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, Mode)
	if err != nil {
		return nil, fmt.Errorf("credentials file: lock: %w", err)
	}
	if err := flock(f); err != nil {
		_ = f.Close()
		if errors.Is(err, errWouldBlock) {
			return nil, fmt.Errorf("%w: %s is locked (%s): stop the other agentkit, or give this one another file", ErrLocked, filepath.Base(strings.TrimSuffix(path, ".lock")), path)
		}
		return nil, fmt.Errorf("credentials file: lock %s: %w", path, err)
	}
	return f, nil
}

// checkMode refuses a file that is not regular or that its group or others
// may read or write.
func checkMode(f *os.File, path string) error {
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("credentials file: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("credentials file: %s is not a regular file", path)
	}
	if perm := fi.Mode().Perm(); perm&^Mode != 0 {
		return fmt.Errorf("%w: %s has mode %#o, but only its owner may read or write it: run chmod 600 %s", ErrInsecure, path, perm, path)
	}
	return nil
}

// load reads the file, or creates it when there is none. A temp file a
// crashed writer left is removed: under the lock, no one else writes it.
func (s *Store) load() error {
	if err := os.Remove(s.tmp()); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("credentials file: remove %s: %w", s.tmp(), err)
	}
	f, err := os.Open(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return s.save()
	}
	if err != nil {
		return fmt.Errorf("credentials file: %w", err)
	}
	defer f.Close()
	if err := checkMode(f, s.path); err != nil {
		return err
	}
	var d fileData
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return fmt.Errorf("credentials file: %s: %w", s.path, err)
	}
	if d.Format != format {
		return fmt.Errorf("credentials file: %s: format %d, this agentkit reads %d", s.path, d.Format, format)
	}
	for i, r := range d.Credentials {
		k := key{r.Tenant, r.User, r.Slot}
		if _, dup := s.entries[k]; dup || r.Tenant == "" || r.Slot == "" || r.Version < 1 || r.Deleted && r.Blob != nil {
			return fmt.Errorf("credentials file: %s: credential %d is invalid", s.path, i)
		}
		if !r.Deleted && r.Blob == nil {
			r.Blob = []byte{} // an empty blob, which omitempty left out
		}
		s.entries[k] = entry{
			meta: credentials.Meta{
				Slot: r.Slot, User: r.User, Kind: r.Kind, Version: r.Version,
				CreatedAt: r.CreatedAt.UTC(), UpdatedAt: r.UpdatedAt.UTC(), Expiry: r.Expiry.UTC(),
			},
			data: r.Blob,
		}
	}
	return nil
}

func (s *Store) tmp() string { return s.path + ".tmp" }

// crashHook, when set (by tests), runs at each step of a save: "mid-write"
// (half of the temp file written), "before-rename" and "after-rename".
var crashHook func(point string)

func crashPoint(point string) {
	if crashHook != nil {
		crashHook(point)
	}
}

// save writes the entries to the file atomically: temp file, fsync, rename,
// fsync of the directory. An error after the rename is errSynced's: the file
// holds the new entries, only the directory's sync failed.
func (s *Store) save() error {
	d := fileData{Format: format, Credentials: []record{}}
	for k, e := range s.entries {
		d.Credentials = append(d.Credentials, record{
			Tenant: k.tenant, User: k.user, Slot: k.slot, Kind: e.meta.Kind, Version: e.meta.Version,
			CreatedAt: e.meta.CreatedAt, UpdatedAt: e.meta.UpdatedAt, Expiry: e.meta.Expiry,
			Blob: e.data, Deleted: e.data == nil,
		})
	}
	slices.SortFunc(d.Credentials, func(a, b record) int {
		return strings.Compare(a.Tenant+"\x00"+a.User+"\x00"+a.Slot, b.Tenant+"\x00"+b.User+"\x00"+b.Slot)
	})
	body, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return fmt.Errorf("credentials file: %w", err)
	}
	body = append(body, '\n')
	if err := s.writeTemp(body); err != nil {
		_ = os.Remove(s.tmp())
		return fmt.Errorf("credentials file: write %s: %w", s.tmp(), err)
	}
	crashPoint("before-rename")
	if err := os.Rename(s.tmp(), s.path); err != nil {
		_ = os.Remove(s.tmp())
		return fmt.Errorf("credentials file: %w", err)
	}
	crashPoint("after-rename")
	if err := syncDir(filepath.Dir(s.path)); err != nil {
		return fmt.Errorf("%w: sync %s: %w", errSynced, filepath.Dir(s.path), err)
	}
	return nil
}

// errSynced marks a save whose rename took effect but whose directory sync
// failed: the file holds the new entries, so they stay.
var errSynced = errors.New("credentials file: written, but not synced")

// syncDir syncs the directory dir; a variable for tests.
var syncDir = func(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	return errors.Join(d.Sync(), d.Close())
}

// writeTemp writes body to the temp file, mode 0600, and syncs it.
func (s *Store) writeTemp(body []byte) error {
	f, err := os.OpenFile(s.tmp(), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, Mode)
	if err != nil {
		return err
	}
	half := len(body) / 2
	_, err = f.Write(body[:half])
	if err == nil {
		crashPoint("mid-write")
		_, err = f.Write(body[half:])
	}
	if err == nil {
		err = f.Chmod(Mode) // O_CREATE's mode passes through the umask
	}
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

// Close releases the file's lock. Calls after Close fail with [ErrClosed].
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lock == nil {
		return nil
	}
	err := s.lock.Close() // closing the descriptor releases the flock
	s.lock = nil
	return err
}

// begin locks the store and returns ctx's key, or an error when there is no
// tenant or the store is closed. The caller unlocks s.mu on success.
func (s *Store) begin(ctx context.Context, user, slot string) (key, error) {
	t, err := tenancy.Require(ctx)
	if err != nil {
		return key{}, err
	}
	s.mu.Lock()
	if s.lock == nil {
		s.mu.Unlock()
		return key{}, ErrClosed
	}
	return key{t.ID(), user, slot}, nil
}

// commit sets k to e and saves; if the save fails before the file is
// replaced, k is as it was. Once the file holds e, so does the store, even
// if the directory's sync then fails (the error is still returned): rolling
// back would leave the store answering other than its file, and hand out
// e's version again over a file that already has it.
func (s *Store) commit(k key, e entry) error {
	old, had := s.entries[k]
	s.entries[k] = e
	if err := s.save(); err != nil {
		if errors.Is(err, errSynced) {
			return err
		}
		if had {
			s.entries[k] = old
		} else {
			delete(s.entries, k)
		}
		return err
	}
	return nil
}

// Get implements [credentials.BlobStore].
func (s *Store) Get(ctx context.Context, user, slot string) (credentials.Blob, error) {
	k, err := s.begin(ctx, user, slot)
	if err != nil {
		return credentials.Blob{}, err
	}
	defer s.mu.Unlock()
	e, ok := s.entries[k]
	if !ok || e.data == nil {
		return credentials.Blob{}, fmt.Errorf("credential %q: %w", slot, credentials.ErrNotFound)
	}
	return credentials.Blob{Meta: e.meta, Data: bytes.Clone(e.data)}, nil
}

// Put implements [credentials.BlobStore].
func (s *Store) Put(ctx context.Context, user, slot string, b credentials.Blob, ifVersion int64) (credentials.Meta, error) {
	k, err := s.begin(ctx, user, slot)
	if err != nil {
		return credentials.Meta{}, err
	}
	defer s.mu.Unlock()
	cur, found := s.entries[k]
	exists := found && cur.data != nil
	switch {
	case ifVersion == 0 && exists:
		return credentials.Meta{}, fmt.Errorf("credential %q: %w", slot, credentials.ErrExists)
	case ifVersion != 0 && (!exists || cur.meta.Version != ifVersion):
		return credentials.Meta{}, fmt.Errorf("credential %q: version %d: %w", slot, ifVersion, credentials.ErrConflict)
	}
	m := credentials.Meta{
		Slot: slot, User: user, Kind: b.Kind, Version: cur.meta.Version + 1,
		CreatedAt: b.UpdatedAt.UTC(), UpdatedAt: b.UpdatedAt.UTC(), Expiry: b.Expiry.UTC(),
	}
	if exists {
		m.CreatedAt = cur.meta.CreatedAt
	}
	if m.UpdatedAt.Before(m.CreatedAt) {
		m.UpdatedAt = m.CreatedAt
	}
	data := bytes.Clone(b.Data)
	if data == nil {
		data = []byte{} // nil marks a tombstone
	}
	if err := s.commit(k, entry{meta: m, data: data}); err != nil {
		return credentials.Meta{}, err
	}
	return m, nil
}

// Delete implements [credentials.BlobStore].
func (s *Store) Delete(ctx context.Context, user, slot string, ifVersion int64) error {
	k, err := s.begin(ctx, user, slot)
	if err != nil {
		return err
	}
	defer s.mu.Unlock()
	cur, found := s.entries[k]
	switch {
	case !found || cur.data == nil:
		return fmt.Errorf("credential %q: %w", slot, credentials.ErrNotFound)
	case ifVersion != 0 && cur.meta.Version != ifVersion:
		return fmt.Errorf("credential %q: version %d: %w", slot, ifVersion, credentials.ErrConflict)
	}
	return s.commit(k, entry{meta: cur.meta})
}

// List implements [credentials.BlobStore].
func (s *Store) List(ctx context.Context, user string) ([]credentials.Meta, error) {
	k, err := s.begin(ctx, user, "")
	if err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	var out []credentials.Meta
	for ek, e := range s.entries {
		if ek.tenant == k.tenant && ek.user == user && e.data != nil {
			out = append(out, e.meta)
		}
	}
	slices.SortFunc(out, func(a, b credentials.Meta) int { return strings.Compare(a.Slot, b.Slot) })
	return out, nil
}
