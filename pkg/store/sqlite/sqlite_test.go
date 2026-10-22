package sqlite_test

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/sqlite"
	"github.com/bsamanvith/agentkit/pkg/store/storetest"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// open returns a store over a new file in a temporary directory, closed when
// the test ends.
func open(t testing.TB, clk clock.Clock) *sqlite.Store {
	t.Helper()
	return openPath(t, migrated(t), sqlite.WithClock(clk))
}

// migrated returns the path of a new file in a temporary directory that
// holds an empty, fully migrated database: a copy of one that sqlite.Open
// created and migrated once for the whole test binary. Migrating a new file
// is the most expensive part of an Open under the race detector (a fifth of
// the package's time, T056), and the suites open a new store for every
// subtest; Open then finds the schema current and migrates nothing. Opening
// and migrating a new file is tested by the tests that open their own
// (migrate_test.go, Example).
func migrated(t testing.TB) string {
	t.Helper()
	src, err := template()
	if err != nil {
		t.Fatalf("migrated template: %v", err)
	}
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "agentkit.db")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// template is the file migrated copies: created by sqlite.Open in a
// directory of its own, and closed, which checkpoints its write-ahead log
// into it, before any copy is made. TestMain removes the directory.
var (
	templateDir string
	template    = sync.OnceValues(func() (string, error) {
		path := filepath.Join(templateDir, "template.db")
		s, err := sqlite.Open(context.Background(), path)
		if err != nil {
			return "", err
		}
		if err := s.Close(); err != nil {
			return "", err
		}
		switch _, err := os.Stat(path + "-wal"); {
		case err == nil:
			return "", errors.New("the closed template left a write-ahead log")
		case !errors.Is(err, fs.ErrNotExist):
			return "", err
		}
		return path, nil
	})
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "agentkit-sqlite-test-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	templateDir = dir
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func openPath(t testing.TB, path string, opts ...sqlite.Option) *sqlite.Store {
	t.Helper()
	s, err := sqlite.Open(context.Background(), path, opts...)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

func newConversations(t *testing.T, clk clock.Clock) store.ConversationStore { return open(t, clk) }

func newRuns(t *testing.T, clk clock.Clock) store.RunStore { return open(t, clk) }

func newAdmin(t *testing.T, clk clock.Clock) (store.RunStore, store.Admin) {
	s := open(t, clk)
	return s, sqlite.Admin(s)
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

func TestRunIndex(t *testing.T) {
	t.Parallel()
	storetest.RunRunIndex(t, func(t *testing.T, clk clock.Clock) storetest.RunIndexStore { return open(t, clk) })
}

func TestNotifications(t *testing.T) {
	t.Parallel()
	storetest.RunNotifications(t, func(t *testing.T, clk clock.Clock) (storetest.NotificationStore, store.Admin) {
		s := open(t, clk)
		return s, sqlite.Admin(s)
	})
}

func TestMessageLog(t *testing.T) {
	t.Parallel()
	storetest.RunMessageLog(t, func(t *testing.T, clk clock.Clock) (store.MessageLog, store.MessageAdmin) {
		s := open(t, clk)
		return s, sqlite.MessageAdmin(s)
	})
}

func TestCommandLog(t *testing.T) {
	t.Parallel()
	storetest.RunCommandLog(t, func(t *testing.T, clk clock.Clock) storetest.CommandStore { return open(t, clk) })
}

func TestToolsetStore(t *testing.T) {
	t.Parallel()
	storetest.RunToolsetStore(t, func(t *testing.T, clk clock.Clock) store.ToolsetStore { return open(t, clk) })
}

func TestItemStore(t *testing.T) {
	t.Parallel()
	storetest.RunItemStore(t, func(t *testing.T, clk clock.Clock) store.ItemStore { return open(t, clk) })
}

func TestTriggerStore(t *testing.T) {
	t.Parallel()
	storetest.RunTriggerStore(t, func(t *testing.T, clk clock.Clock) (store.TriggerStore, store.TriggerAdmin) {
		s := open(t, clk)
		return s, sqlite.TriggerAdmin(s)
	})
}

// The suites again, with every call going to one of two Stores (two
// connection pools, two writer connections) open on the same file, in turn.
// The suites' races (sequence races, concurrent appends, claims and
// transitions) then
// run between connections that only SQLite's file lock serializes: the
// primary keys, the unique index and the conditional updates must hold the
// rules, as between two processes.
func TestTwoHandles(t *testing.T) {
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
	t.Run("TriggerStore", func(t *testing.T) {
		t.Parallel()
		storetest.RunTriggerStore(t, func(t *testing.T, clk clock.Clock) (store.TriggerStore, store.TriggerAdmin) {
			p := openPair(t, clk)
			return p, pairAdmin{p}
		})
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
	t.Run("MessageLog", func(t *testing.T) {
		t.Parallel()
		storetest.RunMessageLog(t, func(t *testing.T, clk clock.Clock) (store.MessageLog, store.MessageAdmin) {
			p := openPair(t, clk)
			return p, pairAdmin{p}
		})
	})
}

// pair sends each call to one of two Stores on one file, in turn.
type pair struct {
	s [2]*sqlite.Store
	n atomic.Uint64
}

func openPair(t *testing.T, clk clock.Clock) *pair {
	path := migrated(t)
	p := &pair{}
	for i := range p.s {
		p.s[i] = openPath(t, path, sqlite.WithClock(clk))
	}
	return p
}

func (p *pair) next() *sqlite.Store { return p.s[p.n.Add(1)%2] }

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

func (p *pair) SendMessage(ctx context.Context, m store.Message) (store.Message, error) {
	return p.next().SendMessage(ctx, m)
}

func (p *pair) GetMessage(ctx context.Context, id string) (store.Message, error) {
	return p.next().GetMessage(ctx, id)
}

func (p *pair) ThreadMessages(ctx context.Context, threadID string, after, limit int) ([]store.Message, error) {
	return p.next().ThreadMessages(ctx, threadID, after, limit)
}

func (p *pair) GetThread(ctx context.Context, id string) (store.Thread, error) {
	return p.next().GetThread(ctx, id)
}

func (p *pair) ListThreads(ctx context.Context, pg store.Page) (store.ThreadPage, error) {
	return p.next().ListThreads(ctx, pg)
}

func (p *pair) StopThread(ctx context.Context, id, code, reason string) (store.Thread, error) {
	return p.next().StopThread(ctx, id, code, reason)
}

func (p *pair) ClaimMessage(ctx context.Context, id, owner string, ttl time.Duration) (store.Message, error) {
	return p.next().ClaimMessage(ctx, id, owner, ttl)
}

func (p *pair) UpdateMessage(ctx context.Context, id, owner string, u store.MessageUpdate) (store.Message, error) {
	return p.next().UpdateMessage(ctx, id, owner, u)
}

func (p *pair) PutTrigger(ctx context.Context, t store.TriggerRecord, ifVersion int64) (store.TriggerRecord, error) {
	return p.next().PutTrigger(ctx, t, ifVersion)
}

func (p *pair) GetTrigger(ctx context.Context, id string) (store.TriggerRecord, error) {
	return p.next().GetTrigger(ctx, id)
}

func (p *pair) ListTriggers(ctx context.Context) ([]store.TriggerRecord, error) {
	return p.next().ListTriggers(ctx)
}

func (p *pair) DeleteTrigger(ctx context.Context, id string, ifVersion int64) error {
	return p.next().DeleteTrigger(ctx, id, ifVersion)
}

func (p *pair) StartFiring(ctx context.Context, triggerID string, f store.Firing, due, next time.Time) (store.Firing, error) {
	return p.next().StartFiring(ctx, triggerID, f, due, next)
}

func (p *pair) EndFiring(ctx context.Context, triggerID, firingID string, end store.FiringEnd) (store.Firing, error) {
	return p.next().EndFiring(ctx, triggerID, firingID, end)
}

func (p *pair) ListFirings(ctx context.Context, triggerID string, limit int) ([]store.Firing, error) {
	return p.next().ListFirings(ctx, triggerID, limit)
}

type pairAdmin struct{ p *pair }

func (a pairAdmin) ClaimDueMessage(ctx context.Context, owner string, ttl time.Duration) (store.TenantMessage, error) {
	return sqlite.MessageAdmin(a.p.next()).ClaimDueMessage(ctx, owner, ttl)
}

func (a pairAdmin) DueTriggers(ctx context.Context, now time.Time, limit int) ([]store.TenantTrigger, error) {
	return sqlite.TriggerAdmin(a.p.next()).DueTriggers(ctx, now, limit)
}

func (a pairAdmin) TriggerByHook(ctx context.Context, hookID string) (store.TenantTrigger, error) {
	return sqlite.TriggerAdmin(a.p.next()).TriggerByHook(ctx, hookID)
}

func (a pairAdmin) ClaimNotification(ctx context.Context, owner string, ttl time.Duration) (store.TenantNotification, error) {
	return sqlite.Admin(a.p.next()).ClaimNotification(ctx, owner, ttl)
}

func (a pairAdmin) RecordDelivery(ctx context.Context, tenant tenancy.Tenant, runID, owner string, d store.Delivery) (store.Notification, error) {
	return sqlite.Admin(a.p.next()).RecordDelivery(ctx, tenant, runID, owner, d)
}

func (a pairAdmin) ListResumableAll(ctx context.Context, now time.Time, limit int) ([]store.TenantRun, error) {
	return sqlite.Admin(a.p.next()).ListResumableAll(ctx, now, limit)
}

func (a pairAdmin) ClaimNext(ctx context.Context, lanes []string, owner string, ttl time.Duration) (store.TenantRun, error) {
	return sqlite.Admin(a.p.next()).ClaimNext(ctx, lanes, owner, ttl)
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
	if _, ok := rs.(store.MessageAdmin); ok {
		t.Error("the tenant-scoped store is also a store.MessageAdmin")
	}
	admin := reflect.TypeFor[store.Admin]()
	for m := range reflect.TypeOf(s).Methods() {
		for out := range m.Type.Outs() {
			if out == admin || out.Implements(admin) {
				t.Errorf("(*sqlite.Store).%s returns a %v, which reaches other tenants' runs", m.Name, out)
			}
		}
	}
	if _, ok := sqlite.Admin(s).(store.RunStore); ok {
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

func TestConversationIndex(t *testing.T) {
	t.Parallel()
	storetest.RunConversationIndex(t, func(t *testing.T, clk clock.Clock) storetest.IndexStore { return open(t, clk) })
}
