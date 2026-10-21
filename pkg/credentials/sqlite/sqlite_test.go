package sqlite_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/credentials/credentialstest"
	"github.com/bsamanvith/agentkit/pkg/credentials/sqlite"
	"github.com/bsamanvith/agentkit/pkg/store"
	storesqlite "github.com/bsamanvith/agentkit/pkg/store/sqlite"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

func open(t *testing.T, path string) *sqlite.Store {
	t.Helper()
	s, err := sqlite.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newStore(t *testing.T) credentials.BlobStore {
	return open(t, filepath.Join(t.TempDir(), "agentkit.db"))
}

func TestBlobStore(t *testing.T) { credentialstest.RunBlobStore(t, newStore) }

func TestVault(t *testing.T) { credentialstest.RunVault(t, newStore) }

// twoHandles is a store whose calls alternate between two Opens of the same
// file: two *sql.DB writers, as two processes would have.
type twoHandles struct {
	s  [2]*sqlite.Store
	mu sync.Mutex
	n  int
}

func (h *twoHandles) next() *sqlite.Store {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.n++
	return h.s[h.n%2]
}

func (h *twoHandles) Get(ctx context.Context, user, slot string) (credentials.Blob, error) {
	return h.next().Get(ctx, user, slot)
}

func (h *twoHandles) Put(ctx context.Context, user, slot string, b credentials.Blob, ifVersion int64) (credentials.Meta, error) {
	return h.next().Put(ctx, user, slot, b, ifVersion)
}

func (h *twoHandles) Delete(ctx context.Context, user, slot string, ifVersion int64) error {
	return h.next().Delete(ctx, user, slot, ifVersion)
}

func (h *twoHandles) List(ctx context.Context, user string) ([]credentials.Meta, error) {
	return h.next().List(ctx, user)
}

// TestTwoHandles: the suites, the concurrent writers of their races and
// simulations included, over two *sql.DB handles on one file: compare-and-swap
// holds across them.
func TestTwoHandles(t *testing.T) {
	newTwo := func(t *testing.T) credentials.BlobStore {
		path := filepath.Join(t.TempDir(), "agentkit.db")
		return &twoHandles{s: [2]*sqlite.Store{open(t, path), open(t, path)}}
	}
	credentialstest.RunBlobStore(t, newTwo)
	credentialstest.RunVault(t, newTwo)
}

// TestConcurrentHandles: writers on two handles race on one version, many
// rounds; exactly one wins each.
func TestConcurrentHandles(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "agentkit.db")
	a, b := open(t, path), open(t, path)
	c := ctx(t, "a")
	m, err := a.Put(c, "", "crm", blob("v1"), 0)
	if err != nil {
		t.Fatal(err)
	}
	for round := range 20 {
		var wg sync.WaitGroup
		wins := make(chan credentials.Meta, 8)
		for w := range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				s := []*sqlite.Store{a, b}[w%2]
				got, err := s.Put(c, "", "crm", blob(fmt.Sprintf("r%d-w%d", round, w)), m.Version)
				switch {
				case err == nil:
					wins <- got
				case !errors.Is(err, credentials.ErrConflict):
					t.Errorf("round %d writer %d: %v", round, w, err)
				}
			}()
		}
		wg.Wait()
		close(wins)
		var won []credentials.Meta
		for w := range wins {
			won = append(won, w)
		}
		if len(won) != 1 || won[0].Version != m.Version+1 {
			t.Fatalf("round %d: winners %+v, want exactly one at version %d", round, won, m.Version+1)
		}
		m = won[0]
	}
}

// TestCanary: no secret reaches the database file or its write-ahead log.
func TestCanary(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "agentkit.db")
	credentialstest.CheckAtRest(t, open(t, path), func(t *testing.T) []byte {
		var all []byte
		for _, f := range []string{path, path + "-wal", path + "-shm"} {
			b, err := os.ReadFile(f)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
			all = append(all, b...)
		}
		return all
	})
}

// TestSameFileAsTheStore: the credentials live in the store's file, beside
// its tables, on its connections (New), and a second Open of the file sees
// them.
func TestSameFileAsTheStore(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "agentkit.db")
	st, err := storesqlite.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c := ctx(t, "a")
	if _, err := st.PutToolset(c, store.ToolsetRecord{Name: "x", Data: []byte(`{"name":"x","type":"demo"}`)}, 0); err != nil {
		t.Fatal(err)
	}
	creds := sqlite.New(st)
	if _, err := creds.Put(c, "", "crm", blob("x"), 0); err != nil {
		t.Fatal(err)
	}
	if err := creds.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetToolset(c, "x"); err != nil {
		t.Errorf("the store after closing its credential view: %v", err)
	}
	if b, err := open(t, path).Get(c, "", "crm"); err != nil || string(b.Data) != "x" {
		t.Errorf("Get through another Open = %q, %v", b.Data, err)
	}
}

// TestTimeRange: a time Unix nanoseconds cannot hold is refused, not
// wrapped around.
func TestTimeRange(t *testing.T) {
	t.Parallel()
	s := newStore(t)
	b := blob("x")
	b.Expiry = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := s.Put(ctx(t, "a"), "", "crm", b, 0); !errors.Is(err, credentials.ErrInvalidCredential) {
		t.Errorf("Put with expiry in 9999: %v, want ErrInvalidCredential", err)
	}
	b = blob("x")
	b.Expiry = time.Date(2200, 1, 1, 0, 0, 0, 1, time.UTC)
	if m, err := s.Put(ctx(t, "a"), "", "crm", b, 0); err != nil || !m.Expiry.Equal(b.Expiry) {
		t.Errorf("Put with expiry in 2200 = %+v, %v", m, err)
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
