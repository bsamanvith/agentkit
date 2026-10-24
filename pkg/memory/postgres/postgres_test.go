//go:build integration

package postgres_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/memorytest"
	"github.com/bsamanvith/agentkit/pkg/memory/postgres"
	storepg "github.com/bsamanvith/agentkit/pkg/store/postgres"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// Tests share one database (AGENTKIT_PG_URL), as the store's do: each store
// gets a schema of its own, migrated by its owner, and is opened as appRole
// (not a superuser, no BYPASSRLS), so row-level security applies to every
// query. make integration STORE=postgres runs them.

const appRole = "agentkit_test_app"

var roleOnce sync.Once

// schema makes an empty, migrated schema and returns the URL that uses it as
// appRole.
func schema(t testing.TB) string {
	t.Helper()
	base := testkit.IntegrationEnv(t, "AGENTKIT_PG_URL")
	name := "m_" + strings.ToLower(rand.Text())
	exec(t, base, `CREATE SCHEMA `+name)
	t.Cleanup(func() { exec(t, base, `DROP SCHEMA `+name+` CASCADE`) })
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		t.Fatalf("AGENTKIT_PG_URL must be a postgres:// URL, got %q (%v)", base, err)
	}
	q := u.Query()
	q.Set("search_path", name)
	// Other packages' tests share the database's 100 connections.
	q.Set("pool_max_conns", "4")
	u.RawQuery = q.Encode()
	owner := u.String()
	roleOnce.Do(func() {
		exec(t, owner, `DO $$ BEGIN
			CREATE ROLE `+appRole+` LOGIN PASSWORD '`+appRole+`' NOSUPERUSER NOBYPASSRLS;
		EXCEPTION WHEN duplicate_object OR unique_violation THEN NULL; END $$`)
	})
	if err := storepg.Migrate(context.Background(), owner); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	exec(t, owner, `GRANT USAGE ON SCHEMA `+name+` TO `+appRole)
	exec(t, owner, `GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA `+name+` TO `+appRole)
	u.User = url.UserPassword(appRole, appRole)
	return u.String()
}

// exec runs one statement on a connection of its own to url.
func exec(t testing.TB, url, sql string) {
	t.Helper()
	ctx := context.Background() // also from Cleanup, after t.Context is done
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, sql); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func open(t testing.TB, url string, cfg memory.Config) *postgres.Store {
	t.Helper()
	s, err := postgres.Open(context.Background(), url, cfg, storepg.WithMigrate(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestConformance(t *testing.T) {
	t.Parallel()
	memorytest.RunStore(t, func(t *testing.T, cfg memory.Config) memory.Store {
		return open(t, schema(t), cfg)
	})
}

// twoPools is a store whose calls alternate between two Opens of the same
// database, as two replicas.
type twoPools struct {
	s  [2]*postgres.Store
	mu sync.Mutex
	n  int
}

func (h *twoPools) next() *postgres.Store {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.n++
	return h.s[h.n%2]
}

func (h *twoPools) View(ctx context.Context, p string) (memory.View, error) {
	return h.next().View(ctx, p)
}

func (h *twoPools) Create(ctx context.Context, p, text string) error {
	return h.next().Create(ctx, p, text)
}

func (h *twoPools) StrReplace(ctx context.Context, p, old, new string) error {
	return h.next().StrReplace(ctx, p, old, new)
}

func (h *twoPools) Insert(ctx context.Context, p string, line int, text string) error {
	return h.next().Insert(ctx, p, line, text)
}
func (h *twoPools) Delete(ctx context.Context, p string) error { return h.next().Delete(ctx, p) }
func (h *twoPools) Rename(ctx context.Context, from, to string) error {
	return h.next().Rename(ctx, from, to)
}

// TestTwoReplicas: the suite, its simulation and concurrent edits included,
// over two pools on one database.
func TestTwoReplicas(t *testing.T) {
	t.Parallel()
	memorytest.RunStore(t, func(t *testing.T, cfg memory.Config) memory.Store {
		app := schema(t)
		return &twoPools{s: [2]*postgres.Store{open(t, app, cfg), open(t, app, cfg)}}
	})
}

// TestReplicasShareLimits: writers on two replicas racing to create files
// in one scope never take it past its file limit: the scope lock holds
// across processes.
func TestReplicasShareLimits(t *testing.T) {
	t.Parallel()
	app := schema(t)
	cfg := memory.Config{Limits: memory.Limits{MaxFiles: 5}}
	a, b := open(t, app, cfg), open(t, app, cfg)
	ctx := memorytest.Ctx(t, "t1", "")
	var wg sync.WaitGroup
	var mu sync.Mutex
	made := 0
	for w := range 16 {
		wg.Go(func() {
			err := []*postgres.Store{a, b}[w%2].Create(ctx, fmt.Sprintf("/memories/w%d.md", w), "x")
			switch {
			case err == nil:
				mu.Lock()
				made++
				mu.Unlock()
			case !errors.Is(err, memory.ErrLimit):
				t.Errorf("writer %d: %v, want nil or ErrLimit", w, err)
			}
		})
	}
	wg.Wait()
	files, err := memorytest.Snapshot(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if made != 5 || len(files) != 5 {
		t.Errorf("%d creates succeeded and %d files are stored, want 5 and 5", made, len(files))
	}
}

// TestRowLevelSecurity: a query with no tenant filter, on the store's own
// connections, sees and changes only the context's tenant's files.
func TestRowLevelSecurity(t *testing.T) {
	t.Parallel()
	app := schema(t)
	st, err := storepg.Open(context.Background(), app, storepg.WithMigrate(false))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s, err := postgres.New(st, memory.Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tn := range []string{"a", "b"} {
		if err := s.Create(memorytest.Ctx(t, tn, ""), "/memories/n.md", "note of "+tn); err != nil {
			t.Fatal(err)
		}
	}
	pool, a := st.Pool(), memorytest.Ctx(t, "a", "")
	var others int
	if err := pool.QueryRow(a, `SELECT count(*) FROM memory_files WHERE tenant <> 'a'`).Scan(&others); err != nil || others != 0 {
		t.Errorf("tenant a sees %d rows of other tenants, %v", others, err)
	}
	if tag, err := pool.Exec(a, `UPDATE memory_files SET content = 'x', size = 1`); err != nil || tag.RowsAffected() != 1 {
		t.Errorf("unfiltered UPDATE as a touched %d rows, %v; want 1", tag.RowsAffected(), err)
	}
	if _, err := pool.Exec(a, `INSERT INTO memory_files (tenant, user_id, path, content, size, updated)
		VALUES ('b', '', '/memories/planted.md', 'x', 1, 1)`); err == nil {
		t.Error("tenant a wrote a row of tenant b")
	}
	var all int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM memory_files`).Scan(&all); !errors.Is(err, tenancy.ErrNoTenant) {
		t.Errorf("a query with no tenant: %v, want ErrNoTenant", err)
	}
	if v, err := s.View(memorytest.Ctx(t, "b", ""), "/memories/n.md"); err != nil || v.Text != "note of b" {
		t.Errorf("b's file after a's attempts = %q, %v", v.Text, err)
	}
}

func TestOpenRefusesABadConfig(t *testing.T) {
	t.Parallel()
	if _, err := postgres.Open(context.Background(), "postgres://unused", memory.Config{Scope: "team"}); !errors.Is(err, memory.ErrInvalid) {
		t.Errorf("Open with scope team: %v, want ErrInvalid", err)
	}
}
