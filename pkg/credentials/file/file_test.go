package file_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/credentials/credentialstest"
	"github.com/bsamanvith/agentkit/pkg/credentials/file"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// open opens a store at path, closed when the test ends.
func open(t *testing.T, path string) *file.Store {
	t.Helper()
	s, err := file.Open(path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newStore(t *testing.T) credentials.BlobStore {
	return open(t, filepath.Join(t.TempDir(), "credentials.json"))
}

func TestBlobStore(t *testing.T) { credentialstest.RunBlobStore(t, newStore) }

func TestVault(t *testing.T) { credentialstest.RunVault(t, newStore) }

// reopening opens the file afresh for every call, so every result the suite
// checks comes from what the file holds, not from the memory of a Store.
type reopening struct {
	t    *testing.T
	path string
	mu   sync.Mutex
}

func (r *reopening) with(fn func(s *file.Store) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, err := file.Open(r.path)
	if err != nil {
		r.t.Errorf("reopen: %v", err)
		return err
	}
	defer s.Close()
	return fn(s)
}

func (r *reopening) Get(ctx context.Context, user, slot string) (b credentials.Blob, err error) {
	err = r.with(func(s *file.Store) error { b, err = s.Get(ctx, user, slot); return err })
	return b, err
}

func (r *reopening) Put(ctx context.Context, user, slot string, b credentials.Blob, ifVersion int64) (m credentials.Meta, err error) {
	err = r.with(func(s *file.Store) error { m, err = s.Put(ctx, user, slot, b, ifVersion); return err })
	return m, err
}

func (r *reopening) Delete(ctx context.Context, user, slot string, ifVersion int64) error {
	return r.with(func(s *file.Store) error { return s.Delete(ctx, user, slot, ifVersion) })
}

func (r *reopening) List(ctx context.Context, user string) (ms []credentials.Meta, err error) {
	err = r.with(func(s *file.Store) error { ms, err = s.List(ctx, user); return err })
	return ms, err
}

// The suites again, with the file closed and read again around every call.
func TestReopened(t *testing.T) {
	newReopening := func(t *testing.T) credentials.BlobStore {
		return &reopening{t: t, path: filepath.Join(t.TempDir(), "credentials.json")}
	}
	credentialstest.RunBlobStore(t, newReopening)
	credentialstest.RunVault(t, newReopening)
}

// TestCanary: no secret reaches the file's bytes.
func TestCanary(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "credentials.json")
	credentialstest.CheckAtRest(t, open(t, path), func(t *testing.T) []byte {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return b
	})
}

// TestNewFileMode: the file is 0600 when Open creates it and after every
// write, whatever the umask allows.
func TestNewFileMode(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "credentials.json")
	s := open(t, path)
	checkMode(t, path)
	if _, err := s.Put(ctx(t, "a"), "", "crm", blob("x"), 0); err != nil {
		t.Fatal(err)
	}
	checkMode(t, path)
}

func checkMode(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("%s has mode %#o, want 0600", path, fi.Mode().Perm())
	}
}

// TestRefusesOpenFile: a file its group or others may read or write is
// refused at open, with the path and the fix in the message.
func TestRefusesOpenFile(t *testing.T) {
	t.Parallel()
	for _, mode := range []os.FileMode{0o640, 0o604, 0o620, 0o602, 0o644, 0o666} {
		path := filepath.Join(t.TempDir(), "credentials.json")
		if err := open(t, path).Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		s, err := file.Open(path)
		if err == nil {
			s.Close()
		}
		if !errors.Is(err, file.ErrInsecure) || !strings.Contains(err.Error(), "chmod 600 "+path) {
			t.Errorf("Open of a %#o file: %v, want ErrInsecure naming %s and chmod 600", mode, err, path)
		}
	}
}

// TestOneWriter: a second Open of an open file fails with ErrLocked, and
// works once the first is closed.
func TestOneWriter(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "credentials.json")
	first := open(t, path)
	if s, err := file.Open(path); !errors.Is(err, file.ErrLocked) {
		if s != nil {
			s.Close()
		}
		t.Fatalf("second Open: %v, want ErrLocked", err)
	}
	if _, err := first.Put(ctx(t, "a"), "", "crm", blob("x"), 0); err != nil {
		t.Fatalf("the first store after the refused open: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Get(ctx(t, "a"), "", "crm"); !errors.Is(err, file.ErrClosed) {
		t.Errorf("Get after Close: %v, want ErrClosed", err)
	}
	second := open(t, path)
	if b, err := second.Get(ctx(t, "a"), "", "crm"); err != nil || string(b.Data) != "x" {
		t.Errorf("Get after reopening = %q, %v", b.Data, err)
	}
}

// TestBadFile: a file that is not the store's is an error at open, which
// leaves the file as it is.
func TestBadFile(t *testing.T) {
	t.Parallel()
	for name, body := range map[string]string{
		"truncated":     `{"format": 1, "credentials": [{"tenant": "a"`,
		"other format":  `{"format": 2, "credentials": []}`,
		"unknown field": `{"format": 1, "credentials": [], "extra": 1}`,
		"no version":    `{"format": 1, "credentials": [{"tenant": "a", "user": "", "slot": "s", "kind": "api_key", "blob": "eA=="}]}`,
		"duplicate": `{"format": 1, "credentials": [
			{"tenant": "a", "user": "", "slot": "s", "kind": "api_key", "version": 1, "blob": "eA=="},
			{"tenant": "a", "user": "", "slot": "s", "kind": "api_key", "version": 2, "blob": "eA=="}]}`,
	} {
		path := filepath.Join(t.TempDir(), "credentials.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if s, err := file.Open(path); err == nil {
			s.Close()
			t.Errorf("%s: Open succeeded", name)
		}
		if got, _ := os.ReadFile(path); string(got) != body {
			t.Errorf("%s: Open changed the file", name)
		}
	}
}

// TestVersionsSurviveRestart: a deleted credential's version is kept in the
// file, so a stale version from before a delete and a restart never matches.
func TestVersionsSurviveRestart(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "credentials.json")
	s := open(t, path)
	c := ctx(t, "a")
	m, _ := s.Put(c, "", "crm", blob("one"), 0)
	m, _ = s.Put(c, "", "crm", blob("two"), m.Version)
	if err := s.Delete(c, "", "crm", m.Version); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s = open(t, path)
	if _, err := s.Put(c, "", "crm", blob("stale"), 2); !errors.Is(err, credentials.ErrConflict) {
		t.Errorf("Put if 2 after delete and restart: %v", err)
	}
	if m, err := s.Put(c, "", "crm", blob("three"), 0); err != nil || m.Version != 3 {
		t.Errorf("recreate after restart = %+v, %v; want version 3", m, err)
	}
}

// TestFailedWriteChangesNothing: a write that cannot reach the disk fails,
// and the store still answers what the file holds.
func TestFailedWriteChangesNothing(t *testing.T) {
	t.Parallel()
	if os.Geteuid() == 0 {
		t.Skip("root writes to a read-only directory")
	}
	dir := t.TempDir()
	s := open(t, filepath.Join(dir, "credentials.json"))
	c := ctx(t, "a")
	if _, err := s.Put(c, "", "crm", blob("one"), 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if _, err := s.Put(c, "", "crm", blob("two"), 1); err == nil {
		t.Fatal("Put into a read-only directory succeeded")
	}
	if err := s.Delete(c, "", "crm", 0); err == nil {
		t.Fatal("Delete in a read-only directory succeeded")
	}
	if b, err := s.Get(c, "", "crm"); err != nil || string(b.Data) != "one" || b.Version != 1 {
		t.Errorf("after the failed writes Get = %q v%d, %v; want one v1", b.Data, b.Version, err)
	}
}

func ctx(t *testing.T, tenant string) context.Context {
	t.Helper()
	tn, err := tenancy.NewTenant(tenant)
	if err != nil {
		t.Fatal(err)
	}
	return tenancy.With(t.Context(), tn)
}

func blob(data string) credentials.Blob {
	return credentials.Blob{
		Meta: credentials.Meta{Kind: credentials.KindAPIKey, UpdatedAt: time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)},
		Data: []byte(data),
	}
}
