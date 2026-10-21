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
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/credentials/credentialstest"
	"github.com/bsamanvith/agentkit/pkg/credentials/postgres"
	storepg "github.com/bsamanvith/agentkit/pkg/store/postgres"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// Tests share one database (AGENTKIT_PG_URL), as the store's do: each store
// gets a schema of its own, migrated by its owner, and is opened as appRole
// (not a superuser, no BYPASSRLS), so row-level security applies to every
// query. make integration STORE=postgres runs them.

const appRole = "agentkit_test_app"

var roleOnce sync.Once

// schema makes an empty, migrated schema and returns the URLs that use it:
// as its owner, and as appRole.
func schema(t testing.TB) (owner, app string) {
	t.Helper()
	base := testkit.IntegrationEnv(t, "AGENTKIT_PG_URL")
	name := "c_" + strings.ToLower(rand.Text())
	exec(t, base, `CREATE SCHEMA `+name)
	t.Cleanup(func() { exec(t, base, `DROP SCHEMA `+name+` CASCADE`) })
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		t.Fatalf("AGENTKIT_PG_URL must be a postgres:// URL, got %q (%v)", base, err)
	}
	q := u.Query()
	q.Set("search_path", name)
	// The database's connections are shared with the store's tests, which
	// run at the same time (go test runs packages in parallel), and CI's
	// Postgres has the default max_connections, 100. 4 per pool keeps
	// TestTwoReplicas's running tests to 32 and still lets
	// TestConcurrentCreates's 8 writers all be in flight.
	q.Set("pool_max_conns", "4")
	u.RawQuery = q.Encode()
	owner = u.String()
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
	return owner, u.String()
}

// exec runs one statement on a connection of its own to url.
func exec(t testing.TB, url, sql string, args ...any) {
	t.Helper()
	ctx := context.Background() // also from Cleanup, after t.Context is done
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// open opens a credential store on url, closed when the test ends.
func open(t testing.TB, url string) *postgres.Store {
	t.Helper()
	s, err := postgres.Open(context.Background(), url, storepg.WithMigrate(false))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func newStore(t *testing.T) credentials.BlobStore {
	_, app := schema(t)
	return open(t, app)
}

func TestBlobStore(t *testing.T) { credentialstest.RunBlobStore(t, newStore) }

func TestVault(t *testing.T) { credentialstest.RunVault(t, newStore) }

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

func (h *twoPools) Get(ctx context.Context, user, slot string) (credentials.Blob, error) {
	return h.next().Get(ctx, user, slot)
}

func (h *twoPools) Put(ctx context.Context, user, slot string, b credentials.Blob, ifVersion int64) (credentials.Meta, error) {
	return h.next().Put(ctx, user, slot, b, ifVersion)
}

func (h *twoPools) Delete(ctx context.Context, user, slot string, ifVersion int64) error {
	return h.next().Delete(ctx, user, slot, ifVersion)
}

func (h *twoPools) List(ctx context.Context, user string) ([]credentials.Meta, error) {
	return h.next().List(ctx, user)
}

// TestTwoReplicas: the blob store suite, its races and simulations
// included, over two pools on one database.
func TestTwoReplicas(t *testing.T) {
	credentialstest.RunBlobStore(t, func(t *testing.T) credentials.BlobStore {
		_, app := schema(t)
		return &twoPools{s: [2]*postgres.Store{open(t, app), open(t, app)}}
	})
}

// TestConcurrentCreates: creators racing on one new key, or on the
// tombstone of a deleted one, never see a unique violation: one wins, the
// others get ErrExists.
func TestConcurrentCreates(t *testing.T) {
	t.Parallel()
	_, app := schema(t)
	a, b := open(t, app), open(t, app)
	c := ctx(t, "a")
	for round := range 10 {
		var wg sync.WaitGroup
		var mu sync.Mutex
		var won []credentials.Meta
		for w := range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				m, err := []*postgres.Store{a, b}[w%2].Put(c, "", "crm", blob(fmt.Sprintf("r%d-w%d", round, w)), 0)
				switch {
				case err == nil:
					mu.Lock()
					won = append(won, m)
					mu.Unlock()
				case !errors.Is(err, credentials.ErrExists):
					t.Errorf("round %d writer %d: %v, want ErrExists", round, w, err)
				}
			}()
		}
		wg.Wait()
		if len(won) != 1 || won[0].Version != int64(round+1) {
			t.Fatalf("round %d: winners %+v, want one at version %d", round, won, round+1)
		}
		if err := a.Delete(c, "", "crm", 0); err != nil {
			t.Fatal(err)
		}
	}
}

// TestCanary: no secret reaches the table's rows, read as text by the
// owner (every tenant's).
func TestCanary(t *testing.T) {
	t.Parallel()
	owner, app := schema(t)
	credentialstest.CheckAtRest(t, open(t, app), func(t *testing.T) []byte {
		ctx := context.Background()
		pool, err := pgxpool.New(ctx, owner)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		var dump string
		// As the superuser that made the schema, which row-level
		// security does not apply to: every tenant's rows.
		if err := pool.QueryRow(ctx, `SELECT coalesce(string_agg(c::text || encode(coalesce(c.blob, ''), 'escape'), E'\n'), '') FROM credential_blobs c`).Scan(&dump); err != nil {
			t.Fatal(err)
		}
		return []byte(dump)
	})
}

// TestRowLevelSecurity: a query with no tenant filter, on the store's own
// connections, sees and changes only the context's tenant's credentials.
func TestRowLevelSecurity(t *testing.T) {
	t.Parallel()
	_, app := schema(t)
	st, err := storepg.Open(context.Background(), app, storepg.WithMigrate(false))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := postgres.New(st)
	for _, tn := range []string{"a", "b"} {
		if _, err := s.Put(ctx(t, tn), "", "crm", blob("blob-"+tn), 0); err != nil {
			t.Fatal(err)
		}
	}
	pool, a := st.Pool(), ctx(t, "a")
	var others, all int
	if err := pool.QueryRow(a, `SELECT count(*) FROM credential_blobs WHERE tenant <> 'a'`).Scan(&others); err != nil || others != 0 {
		t.Errorf("tenant a sees %d rows of other tenants, %v", others, err)
	}
	if tag, err := pool.Exec(a, `UPDATE credential_blobs SET blob = NULL`); err != nil || tag.RowsAffected() != 1 {
		t.Errorf("unfiltered UPDATE as a touched %d rows, %v; want 1", tag.RowsAffected(), err)
	}
	if _, err := pool.Exec(a, `INSERT INTO credential_blobs (tenant, user_id, slot, version, kind, blob, created_at, updated_at)
		VALUES ('b', '', 'planted', 1, 'api_key', 'x', 1, 1)`); err == nil {
		t.Error("tenant a wrote a row of tenant b")
	}
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM credential_blobs`).Scan(&all); !errors.Is(err, tenancy.ErrNoTenant) {
		t.Errorf("a query with no tenant: %v, want ErrNoTenant", err)
	}
	if b, err := s.Get(ctx(t, "b"), "", "crm"); err != nil || string(b.Data) != "blob-b" {
		t.Errorf("b's credential after a's attempts = %q, %v", b.Data, err)
	}
}

// TestNeedsTheMigration: a database the store has not migrated is refused
// at Open with WithMigrate(false), and gets the table with the default.
func TestNeedsTheMigration(t *testing.T) {
	t.Parallel()
	base := testkit.IntegrationEnv(t, "AGENTKIT_PG_URL")
	name := "c_" + strings.ToLower(rand.Text())
	exec(t, base, `CREATE SCHEMA `+name)
	t.Cleanup(func() { exec(t, base, `DROP SCHEMA `+name+` CASCADE`) })
	u, _ := url.Parse(base)
	q := u.Query()
	q.Set("search_path", name)
	u.RawQuery = q.Encode()
	if s, err := postgres.Open(context.Background(), u.String(), storepg.WithMigrate(false)); !errors.Is(err, storepg.ErrSchemaTooOld) {
		if s != nil {
			s.Close()
		}
		t.Fatalf("Open of an empty schema without migrating: %v, want ErrSchemaTooOld", err)
	}
	s, err := postgres.Open(context.Background(), u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Put(ctx(t, "a"), "", "crm", blob("x"), 0); err != nil {
		t.Errorf("Put after Open migrated: %v", err)
	}
}

func ctx(t testing.TB, tenant string) context.Context {
	t.Helper()
	tn, err := tenancy.NewTenant(tenant)
	if err != nil {
		t.Fatal(err)
	}
	return tenancy.With(context.Background(), tn)
}

func blob(data string) credentials.Blob {
	return credentials.Blob{
		Meta: credentials.Meta{Kind: credentials.KindAPIKey, UpdatedAt: time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC)},
		Data: []byte(data),
	}
}
