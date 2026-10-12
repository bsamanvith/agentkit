//go:build integration

package postgres_test

import (
	"context"
	"crypto/rand"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/postgres"
	"github.com/bsamanvith/agentkit/pkg/store/storetest"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// Tests share one database (AGENTKIT_PG_URL). Each store gets a schema of its
// own, made for it and dropped when its test ends: its connections put that
// schema first on their search_path, and the store's tables, migrations and
// migration lock all live in it.

// schemaURL makes an empty schema and returns AGENTKIT_PG_URL with its
// connections set to use it, and a pool of at most 8 connections.
func schemaURL(t testing.TB) string {
	t.Helper()
	base := testkit.IntegrationEnv(t, "AGENTKIT_PG_URL")
	name := "t_" + strings.ToLower(rand.Text())
	exec(t, base, `CREATE SCHEMA `+name)
	t.Cleanup(func() { exec(t, base, `DROP SCHEMA `+name+` CASCADE`) })
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		t.Fatalf("AGENTKIT_PG_URL must be a postgres:// URL, got %q (%v)", base, err)
	}
	q := u.Query()
	q.Set("search_path", name) // pgx sends unknown parameters to the server
	q.Set("pool_max_conns", "8")
	u.RawQuery = q.Encode()
	return u.String()
}

// exec runs one statement on a connection of its own to url.
func exec(t testing.TB, url, sql string, args ...any) {
	t.Helper()
	// Not t.Context: it runs from Cleanup too, when that is done. A pool,
	// unlike pgx.Connect, reads pool_max_conns from the URL.
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// queryRow reads one row from url into dest.
func queryRow(t testing.TB, url, sql string, args []any, dest ...any) {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	if err := pool.QueryRow(ctx, sql, args...).Scan(dest...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// openURL opens a store on url, closed when the test ends.
func openURL(t testing.TB, url string, opts ...postgres.Option) *postgres.Store {
	t.Helper()
	s, err := postgres.Open(context.Background(), url, opts...)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

// open returns a store over a new schema.
func open(t testing.TB, clk clock.Clock) *postgres.Store {
	t.Helper()
	return openURL(t, schemaURL(t), postgres.WithClock(clk))
}

func tenantCtx(t testing.TB, id string) context.Context {
	t.Helper()
	tn, err := tenancy.NewTenant(id)
	if err != nil {
		t.Fatalf("NewTenant: %v", err)
	}
	return tenancy.With(context.Background(), tn)
}

func newConversations(t *testing.T, clk clock.Clock) store.ConversationStore { return open(t, clk) }

func newRuns(t *testing.T, clk clock.Clock) store.RunStore { return open(t, clk) }

func newAdmin(t *testing.T, clk clock.Clock) (store.RunStore, store.Admin) {
	s := open(t, clk)
	return s, postgres.Admin(s)
}

func newEvents(t *testing.T, clk clock.Clock) store.EventLog { return open(t, clk) }

func TestConversationStore(t *testing.T) {
	t.Parallel()
	storetest.RunConversationStore(t, newConversations)
}

func TestRunStore(t *testing.T) {
	t.Parallel()
	storetest.RunRunStore(t, newRuns)
}

func TestAdmin(t *testing.T) {
	t.Parallel()
	storetest.RunAdmin(t, newAdmin)
}

func TestCheckpointer(t *testing.T) {
	t.Parallel()
	storetest.RunCheckpointer(t, func(t *testing.T) flow.Checkpointer { return open(t, clock.Real()) })
}

func TestEventLog(t *testing.T) {
	t.Parallel()
	storetest.RunEventLog(t, newEvents)
}

// The suites again, with every call going to one of two Stores (two
// connection pools) on the same schema, in turn, as two processes would. The
// suites' races (sequence races, concurrent appends, claims and transitions)
// then run between pools that only the database serializes: the primary
// keys, the unique index and the conditional updates must hold the rules.
func TestTwoPools(t *testing.T) {
	t.Parallel()
	t.Run("ConversationStore", func(t *testing.T) {
		t.Parallel()
		storetest.RunConversationStore(t, func(t *testing.T, clk clock.Clock) store.ConversationStore { return openPair(t, clk) })
	})
	t.Run("RunStore", func(t *testing.T) {
		t.Parallel()
		storetest.RunRunStore(t, func(t *testing.T, clk clock.Clock) store.RunStore { return openPair(t, clk) })
	})
	t.Run("Admin", func(t *testing.T) {
		t.Parallel()
		storetest.RunAdmin(t, func(t *testing.T, clk clock.Clock) (store.RunStore, store.Admin) {
			p := openPair(t, clk)
			return p, pairAdmin{p}
		})
	})
	t.Run("Checkpointer", func(t *testing.T) {
		t.Parallel()
		storetest.RunCheckpointer(t, func(t *testing.T) flow.Checkpointer { return openPair(t, clock.Real()) })
	})
	t.Run("EventLog", func(t *testing.T) {
		t.Parallel()
		storetest.RunEventLog(t, func(t *testing.T, clk clock.Clock) store.EventLog { return openPair(t, clk) })
	})
}

// pair sends each call to one of two Stores on one schema, in turn.
type pair struct {
	s [2]*postgres.Store
	n atomic.Uint64
}

func openPair(t testing.TB, clk clock.Clock) *pair {
	u := schemaURL(t)
	p := &pair{}
	for i := range p.s {
		p.s[i] = openURL(t, u, postgres.WithClock(clk))
	}
	return p
}

func (p *pair) next() *postgres.Store { return p.s[p.n.Add(1)%2] }

func (p *pair) Append(ctx context.Context, id string, e store.Entry) error {
	return p.next().Append(ctx, id, e)
}

func (p *pair) History(ctx context.Context, id string, pg store.Page) (store.EntryPage, error) {
	return p.next().History(ctx, id, pg)
}

func (p *pair) Context(ctx context.Context, id string) (store.ContextView, error) {
	return p.next().Context(ctx, id)
}

func (p *pair) Create(ctx context.Context, id, conversationID string) (store.Run, error) {
	return p.next().Create(ctx, id, conversationID)
}

func (p *pair) Get(ctx context.Context, id string) (store.Run, error) { return p.next().Get(ctx, id) }

func (p *pair) List(ctx context.Context, conversationID string, pg store.Page) (store.RunPage, error) {
	return p.next().List(ctx, conversationID, pg)
}

func (p *pair) Transition(ctx context.Context, id string, from, to store.Status, d store.Details) (store.Run, error) {
	return p.next().Transition(ctx, id, from, to, d)
}

func (p *pair) ListResumable(ctx context.Context, now time.Time, limit int) ([]store.Run, error) {
	return p.next().ListResumable(ctx, now, limit)
}

func (p *pair) ListSuspended(ctx context.Context, pg store.Page) (store.RunPage, error) {
	return p.next().ListSuspended(ctx, pg)
}

func (p *pair) Unfinished(ctx context.Context, conversationID string) (store.Run, error) {
	return p.next().Unfinished(ctx, conversationID)
}

func (p *pair) Claim(ctx context.Context, runID, owner string, ttl time.Duration) (store.Lease, error) {
	return p.next().Claim(ctx, runID, owner, ttl)
}

func (p *pair) Renew(ctx context.Context, runID, owner string, ttl time.Duration) (store.Lease, error) {
	return p.next().Renew(ctx, runID, owner, ttl)
}

func (p *pair) Release(ctx context.Context, runID, owner string) error {
	return p.next().Release(ctx, runID, owner)
}

func (p *pair) Save(ctx context.Context, id string, c flow.Checkpoint) error {
	return p.next().Save(ctx, id, c)
}

func (p *pair) Load(ctx context.Context, id string) (flow.Checkpoint, error) {
	return p.next().Load(ctx, id)
}

func (p *pair) AppendEvents(ctx context.Context, runID string, events []store.Event) error {
	return p.next().AppendEvents(ctx, runID, events)
}

func (p *pair) EventsAfter(ctx context.Context, runID string, after int64, limit int) ([]store.Event, error) {
	return p.next().EventsAfter(ctx, runID, after, limit)
}

func (p *pair) LastEventSeq(ctx context.Context, runID string) (int64, error) {
	return p.next().LastEventSeq(ctx, runID)
}

type pairAdmin struct{ p *pair }

func (a pairAdmin) ListResumableAll(ctx context.Context, now time.Time, limit int) ([]store.TenantRun, error) {
	return postgres.Admin(a.p.next()).ListResumableAll(ctx, now, limit)
}

// The cross-tenant read is not reachable from the tenant-scoped store: code
// holding it cannot type-assert its way to other tenants' runs, neither to
// store.Admin itself nor to any method that hands one out.
func TestAdminIsASeparateValue(t *testing.T) {
	t.Parallel()
	s := open(t, clock.Real())
	var rs store.RunStore = s
	if _, ok := rs.(store.Admin); ok {
		t.Error("the tenant-scoped store is also a store.Admin")
	}
	admin := reflect.TypeFor[store.Admin]()
	for m := range reflect.TypeOf(s).Methods() {
		for out := range m.Type.Outs() {
			if out == admin || out.Implements(admin) {
				t.Errorf("(*postgres.Store).%s returns a %v, which reaches other tenants' runs", m.Name, out)
			}
		}
	}
	if _, ok := postgres.Admin(s).(store.RunStore); ok {
		t.Error("the Admin view is also a store.RunStore")
	}
}

func FuzzConversationSimulation(f *testing.F) {
	for seed := range uint64(8) {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed uint64) {
		storetest.SimulateConversations(t, newConversations, seed)
	})
}

func FuzzRunSimulation(f *testing.F) {
	for seed := range uint64(8) {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed uint64) {
		storetest.SimulateRuns(t, newRuns, seed)
	})
}

func FuzzAdminSimulation(f *testing.F) {
	for seed := range uint64(8) {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed uint64) {
		storetest.SimulateAdmin(t, newAdmin, seed)
	})
}

func FuzzEventSimulation(f *testing.F) {
	for seed := range uint64(8) {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed uint64) {
		storetest.SimulateEvents(t, newEvents, seed)
	})
}
