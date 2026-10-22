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
	owner := schemaURL(t)
	return openURL(t, appURL(t, owner), postgres.WithClock(clk), postgres.WithMigrate(false), postgres.WithAdminURL(owner))
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

func TestConversationIndex(t *testing.T) {
	t.Parallel()
	storetest.RunConversationIndex(t, func(t *testing.T, clk clock.Clock) storetest.IndexStore { return open(t, clk) })
}

func TestCheckpointer(t *testing.T) {
	t.Parallel()
	storetest.RunCheckpointer(t, func(t *testing.T) flow.Checkpointer { return open(t, clock.Real()) })
}

func TestEventLog(t *testing.T) {
	t.Parallel()
	storetest.RunEventLog(t, newEvents)
}

func TestCommandLog(t *testing.T) {
	t.Parallel()
	storetest.RunCommandLog(t, func(t *testing.T, clk clock.Clock) storetest.CommandStore { return open(t, clk) })
}

func TestRunIndex(t *testing.T) {
	t.Parallel()
	storetest.RunRunIndex(t, func(t *testing.T, clk clock.Clock) storetest.RunIndexStore { return open(t, clk) })
}

func TestNotifications(t *testing.T) {
	t.Parallel()
	storetest.RunNotifications(t, func(t *testing.T, clk clock.Clock) (storetest.NotificationStore, store.Admin) {
		s := open(t, clk)
		return s, postgres.Admin(s)
	})
}

func TestToolsetStore(t *testing.T) {
	t.Parallel()
	storetest.RunToolsetStore(t, func(t *testing.T, clk clock.Clock) store.ToolsetStore { return open(t, clk) })
}

func TestItemStore(t *testing.T) {
	t.Parallel()
	storetest.RunItemStore(t, func(t *testing.T, clk clock.Clock) store.ItemStore { return open(t, clk) })
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
	t.Run("CommandLog", func(t *testing.T) {
		t.Parallel()
		storetest.RunCommandLog(t, func(t *testing.T, clk clock.Clock) storetest.CommandStore { return openPair(t, clk) })
	})
	t.Run("ToolsetStore", func(t *testing.T) {
		t.Parallel()
		storetest.RunToolsetStore(t, func(t *testing.T, clk clock.Clock) store.ToolsetStore { return openPair(t, clk) })
	})
	t.Run("ItemStore", func(t *testing.T) {
		t.Parallel()
		storetest.RunItemStore(t, func(t *testing.T, clk clock.Clock) store.ItemStore { return openPair(t, clk) })
	})

	t.Run("ConversationIndex", func(t *testing.T) {
		t.Parallel()
		storetest.RunConversationIndex(t, func(t *testing.T, clk clock.Clock) storetest.IndexStore { return openPair(t, clk) })
	})
	t.Run("RunIndex", func(t *testing.T) {
		t.Parallel()
		storetest.RunRunIndex(t, func(t *testing.T, clk clock.Clock) storetest.RunIndexStore { return openPair(t, clk) })
	})
	t.Run("Notifications", func(t *testing.T) {
		t.Parallel()
		storetest.RunNotifications(t, func(t *testing.T, clk clock.Clock) (storetest.NotificationStore, store.Admin) {
			p := openPair(t, clk)
			return p, pairAdmin{p}
		})
	})
}

// pair sends each call to one of two Stores on one schema, in turn.
type pair struct {
	s [2]*postgres.Store
	n atomic.Uint64
}

func openPair(t testing.TB, clk clock.Clock) *pair {
	owner := schemaURL(t)
	u := appURL(t, owner)
	p := &pair{}
	for i := range p.s {
		p.s[i] = openURL(t, u, postgres.WithClock(clk), postgres.WithMigrate(false), postgres.WithAdminURL(owner))
	}
	return p
}

func (p *pair) next() *postgres.Store { return p.s[p.n.Add(1)%2] }

func (p *pair) Append(ctx context.Context, id string, e store.Entry) error {
	return p.next().Append(ctx, id, e)
}

func (p *pair) AppendBatch(ctx context.Context, id string, es []store.Entry) error {
	return p.next().AppendBatch(ctx, id, es)
}

func (p *pair) History(ctx context.Context, id string, pg store.Page) (store.EntryPage, error) {
	return p.next().History(ctx, id, pg)
}

func (p *pair) Context(ctx context.Context, id string) (store.ContextView, error) {
	return p.next().Context(ctx, id)
}

func (p *pair) Create(ctx context.Context, id, conversationID string, opts ...store.CreateOption) (store.Run, error) {
	return p.next().Create(ctx, id, conversationID, opts...)
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

func (p *pair) SendCommand(ctx context.Context, runID string, c store.Command) (store.Command, error) {
	return p.next().SendCommand(ctx, runID, c)
}

func (p *pair) PendingCommands(ctx context.Context, runID string, limit int) ([]store.Command, error) {
	return p.next().PendingCommands(ctx, runID, limit)
}

func (p *pair) ConsumeCommands(ctx context.Context, runID, owner string, through int64) error {
	return p.next().ConsumeCommands(ctx, runID, owner, through)
}

func (p *pair) CreateConversation(ctx context.Context, c store.Conversation) (store.Conversation, error) {
	return p.next().CreateConversation(ctx, c)
}

func (p *pair) GetConversation(ctx context.Context, id string) (store.Conversation, error) {
	return p.next().GetConversation(ctx, id)
}

func (p *pair) ListConversations(ctx context.Context, agent string, pg store.Page) (store.ConversationPage, error) {
	return p.next().ListConversations(ctx, agent, pg)
}

func (p *pair) SetTitle(ctx context.Context, id, title string) (store.Conversation, error) {
	return p.next().SetTitle(ctx, id, title)
}

func (p *pair) DeleteConversation(ctx context.Context, id string) error {
	return p.next().DeleteConversation(ctx, id)
}

func (p *pair) PutToolset(ctx context.Context, t store.ToolsetRecord, ifVersion int64) (store.ToolsetRecord, error) {
	return p.next().PutToolset(ctx, t, ifVersion)
}

func (p *pair) GetToolset(ctx context.Context, name string) (store.ToolsetRecord, error) {
	return p.next().GetToolset(ctx, name)
}

func (p *pair) ListToolsets(ctx context.Context) ([]store.ToolsetRecord, error) {
	return p.next().ListToolsets(ctx)
}

func (p *pair) DeleteToolset(ctx context.Context, name string, ifVersion int64) error {
	return p.next().DeleteToolset(ctx, name, ifVersion)
}

func (p *pair) ListRuns(ctx context.Context, f store.RunFilter, pg store.Page) (store.RunPage, error) {
	return p.next().ListRuns(ctx, f, pg)
}

func (p *pair) CreateNotification(ctx context.Context, runID, url string) (store.Notification, error) {
	return p.next().CreateNotification(ctx, runID, url)
}

func (p *pair) GetNotification(ctx context.Context, runID string) (store.Notification, error) {
	return p.next().GetNotification(ctx, runID)
}

func (p *pair) SaveItem(ctx context.Context, v store.ItemVersion, ifLatest int64) (store.ItemVersion, error) {
	return p.next().SaveItem(ctx, v, ifLatest)
}

func (p *pair) PublishItem(ctx context.Context, kind, name string, version int64) (store.Item, error) {
	return p.next().PublishItem(ctx, kind, name, version)
}

func (p *pair) GetItem(ctx context.Context, kind, name string) (store.Item, error) {
	return p.next().GetItem(ctx, kind, name)
}

func (p *pair) ListItems(ctx context.Context, kind string) ([]store.Item, error) {
	return p.next().ListItems(ctx, kind)
}

func (p *pair) GetItemVersion(ctx context.Context, kind, name string, version int64) (store.ItemVersion, error) {
	return p.next().GetItemVersion(ctx, kind, name, version)
}

func (p *pair) ListItemVersions(ctx context.Context, kind, name string) ([]store.ItemVersion, error) {
	return p.next().ListItemVersions(ctx, kind, name)
}

func (p *pair) DeleteItemVersion(ctx context.Context, kind, name string, version int64) error {
	return p.next().DeleteItemVersion(ctx, kind, name, version)
}

type pairAdmin struct{ p *pair }

func (a pairAdmin) ClaimNotification(ctx context.Context, owner string, ttl time.Duration) (store.TenantNotification, error) {
	return postgres.Admin(a.p.next()).ClaimNotification(ctx, owner, ttl)
}

func (a pairAdmin) RecordDelivery(ctx context.Context, tenant tenancy.Tenant, runID, owner string, d store.Delivery) (store.Notification, error) {
	return postgres.Admin(a.p.next()).RecordDelivery(ctx, tenant, runID, owner, d)
}

func (a pairAdmin) ListResumableAll(ctx context.Context, now time.Time, limit int) ([]store.TenantRun, error) {
	return postgres.Admin(a.p.next()).ListResumableAll(ctx, now, limit)
}

func (a pairAdmin) ClaimNext(ctx context.Context, lanes []string, owner string, ttl time.Duration) (store.TenantRun, error) {
	return postgres.Admin(a.p.next()).ClaimNext(ctx, lanes, owner, ttl)
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
