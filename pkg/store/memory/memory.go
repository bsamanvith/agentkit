// Package memory is an in-process store: a [store.ConversationStore],
// [store.RunStore], [store.EventLog] and [flow.Checkpointer] in one value, and a [store.Admin]
// view of it as a separate value ([Admin]). Nothing survives the
// process. It is for tests, single-process use, and as the reference every
// other backend is compared with: it passes the same conformance suites
// (package storetest, and flowtest for checkpoints).
package memory

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/internal/backend"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

var (
	_ store.ConversationStore = (*Store)(nil)
	_ store.RunStore          = (*Store)(nil)
	_ store.EventLog          = (*Store)(nil)
	_ store.CommandLog        = (*Store)(nil)
	_ store.ConversationIndex = (*Store)(nil)
	_ store.ToolsetStore      = (*Store)(nil)
	_ store.RunIndex          = (*Store)(nil)
	_ store.NotificationLog   = (*Store)(nil)
	_ store.ItemStore         = (*Store)(nil)
	_ store.MessageLog        = (*Store)(nil)
	_ flow.Checkpointer       = (*Store)(nil)
	_ store.Admin             = admin{}
	_ store.MessageAdmin      = admin{}
	_ store.TriggerStore      = (*Store)(nil)
	_ store.TriggerAdmin      = admin{}
)

// Option configures a [Store].
type Option func(*Store)

// WithClock sets the clock that entry, event and run times and lease expiry
// are read from. The default, also for nil, is [clock.Real].
func WithClock(c clock.Clock) Option {
	return func(s *Store) {
		if c != nil {
			s.clk = c
		}
	}
}

// Store keeps conversations, runs, event logs and checkpoints in memory, per
// tenant. It is safe for concurrent use. Make one with [New].
type Store struct {
	clk clock.Clock

	mu          sync.Mutex
	convs       map[key][]entry
	records     map[key]store.Conversation
	runs        map[key]store.Run
	events      map[key][]store.Event   // by run id
	commands    map[key][]store.Command // by run id
	consumed    map[key]int64           // by run id: the last consumed command
	checkpoints map[key]flow.Checkpoint
	toolsets    map[key]store.ToolsetRecord // by name

	notifications map[key]store.Notification // by run id
	items         map[key]*storedItem        // by itemID
	messages      map[key]store.Message      // by message id
	threads       map[key]store.Thread       // by thread id
	triggers      map[key]*memTrigger        // by trigger id
}

// key scopes an id by tenant: the same id in two tenants is two things.
type key struct {
	tenant tenancy.Tenant
	id     string
}

type entry struct {
	backend.Stored
	created time.Time
}

// New returns an empty store.
func New(opts ...Option) *Store {
	s := &Store{
		clk:         clock.Real(),
		convs:       map[key][]entry{},
		records:     map[key]store.Conversation{},
		runs:        map[key]store.Run{},
		events:      map[key][]store.Event{},
		commands:    map[key][]store.Command{},
		consumed:    map[key]int64{},
		checkpoints: map[key]flow.Checkpoint{},
		toolsets:    map[key]store.ToolsetRecord{},

		notifications: map[key]store.Notification{},
		items:         map[key]*storedItem{},
		messages:      map[key]store.Message{},
		threads:       map[key]store.Thread{},
		triggers:      map[key]*memTrigger{},
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// scope checks ctx and returns the key of id in ctx's tenant.
func scope(ctx context.Context, id string) (key, error) {
	if err := ctx.Err(); err != nil {
		return key{}, err
	}
	t, err := tenancy.Require(ctx)
	if err != nil {
		return key{}, err
	}
	return key{tenant: t, id: id}, nil
}

// now reads the clock without its monotonic reading, so that times compare
// the same after a round trip through any backend.
func (s *Store) now() time.Time { return s.clk.Now().Round(0) }

// Append implements [store.ConversationStore].
func (s *Store) Append(ctx context.Context, id string, e store.Entry) error {
	k, err := scope(ctx, id)
	if err != nil {
		return err
	}
	if err := backend.CheckID("conversation", id); err != nil {
		return err
	}
	st, err := backend.CheckEntry(e)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	log := s.convs[k]
	n := int64(len(log))
	switch {
	case e.Seq <= n:
		if log[e.Seq-1].Same(st) {
			return nil
		}
		return fmt.Errorf("%w: conversation %q already has a different entry %d", store.ErrConflict, id, e.Seq)
	case e.Seq > n+1:
		return fmt.Errorf("%w: conversation %q has %d entries, got entry %d", store.ErrGap, id, n, e.Seq)
	}
	created := s.now()
	if n > 0 && created.Before(log[n-1].created) {
		created = log[n-1].created // times never go backwards along the log
	}
	if err := backend.CheckTime("the clock's time", created); err != nil {
		return err
	}
	s.convs[k] = append(log, entry{Stored: st, created: created})
	s.counted(k, created, st)
	return nil
}

// AppendBatch implements [store.ConversationStore].
func (s *Store) AppendBatch(ctx context.Context, id string, entries []store.Entry) error {
	k, err := scope(ctx, id)
	if err != nil {
		return err
	}
	if err := backend.CheckID("conversation", id); err != nil {
		return err
	}
	batch, first, err := backend.CheckEntries(entries)
	if err != nil || len(batch) == 0 {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	log := s.convs[k]
	last := int64(len(log))
	var taken []backend.Stored
	for seq := first; seq <= min(last, first+int64(len(batch))-1); seq++ {
		taken = append(taken, log[seq-1].Stored)
	}
	add, err := backend.NewEntries(id, first, batch, last, taken)
	if err != nil || len(add) == 0 {
		return err
	}
	created := s.now()
	if last > 0 && created.Before(log[last-1].created) {
		created = log[last-1].created // times never go backwards along the log
	}
	if err := backend.CheckTime("the clock's time", created); err != nil {
		return err
	}
	for _, st := range add {
		log = append(log, entry{Stored: st, created: created})
	}
	s.convs[k] = log
	s.counted(k, created, add...)
	return nil
}

// counted adds appended entries to the conversation's record, if it has one.
// Called with s.mu held.
func (s *Store) counted(k key, at time.Time, added ...backend.Stored) {
	c, ok := s.records[k]
	if !ok {
		return
	}
	for _, st := range added {
		c.Messages += st.MessageCount()
	}
	if at.After(c.UpdatedAt) {
		c.UpdatedAt = at
	}
	s.records[k] = c
}

// History implements [store.ConversationStore].
func (s *Store) History(ctx context.Context, id string, p store.Page) (store.EntryPage, error) {
	k, err := scope(ctx, id)
	if err != nil {
		return store.EntryPage{}, err
	}
	if err := backend.CheckID("conversation", id); err != nil {
		return store.EntryPage{}, err
	}
	limit, start, err := backend.ParseHistoryPage(p)
	if err != nil {
		return store.EntryPage{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	log := s.convs[k]
	n := len(log)

	// i is the index of the first entry of the page; step is +1 or -1.
	var i, step int
	if p.Order == store.Ascending {
		step = 1
		switch {
		case start.HasAfter:
			i = int(min(start.After, int64(n)))
		case start.FromSeq > 0:
			i = int(min(start.FromSeq-1, int64(n)))
		case !start.FromTime.IsZero():
			i = sort.Search(n, func(j int) bool { return !log[j].created.Before(start.FromTime) })
		}
	} else {
		step = -1
		i = n - 1
		switch {
		case start.HasAfter:
			i = int(min(start.After-1, int64(n))) - 1
		case start.FromSeq > 0:
			i = int(min(start.FromSeq, int64(n))) - 1
		case !start.FromTime.IsZero():
			i = sort.Search(n, func(j int) bool { return log[j].created.After(start.FromTime) }) - 1
		}
	}

	var page store.EntryPage
	for ; i >= 0 && i < n && len(page.Entries) < limit; i += step {
		e, err := log[i].Entry(int64(i+1), log[i].created)
		if err != nil {
			return store.EntryPage{}, err
		}
		page.Entries = append(page.Entries, e)
	}
	if i >= 0 && i < n && len(page.Entries) > 0 {
		page.Next = backend.HistoryCursor(p.Order, page.Entries[len(page.Entries)-1].Seq)
	}
	return page, nil
}

// Context implements [store.ConversationStore].
func (s *Store) Context(ctx context.Context, id string) (store.ContextView, error) {
	k, err := scope(ctx, id)
	if err != nil {
		return store.ContextView{}, err
	}
	if err := backend.CheckID("conversation", id); err != nil {
		return store.ContextView{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	log := s.convs[k]
	from := 0
	for i := len(log) - 1; i >= 0; i-- {
		if log[i].Kind == store.KindCompaction {
			from = i
			break
		}
	}
	view := store.ContextView{Seq: int64(len(log))}
	for i := from; i < len(log); i++ {
		e, err := log[i].Entry(int64(i+1), log[i].created)
		if err != nil {
			return store.ContextView{}, err
		}
		view.Entries = append(view.Entries, e)
	}
	return view, nil
}

// Create implements [store.RunStore].
func (s *Store) Create(ctx context.Context, id, conversationID string, opts ...store.CreateOption) (store.Run, error) {
	o := store.NewCreateOptions(opts...)
	k, err := scope(ctx, id)
	if err != nil {
		return store.Run{}, err
	}
	if err := backend.CheckID("run", id); err != nil {
		return store.Run{}, err
	}
	if err := backend.CheckID("conversation", conversationID); err != nil {
		return store.Run{}, err
	}
	now := s.now()
	c, err := backend.CheckCreate(o, now)
	if err != nil {
		return store.Run{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.runs[k]; ok {
		if r.ConversationID != conversationID {
			return store.Run{}, fmt.Errorf("%w: run %q belongs to conversation %q", store.ErrConflict, id, r.ConversationID)
		}
		return cloneRun(r), nil
	}
	if r, ok := s.unfinished(k.tenant, conversationID); ok {
		return store.Run{}, fmt.Errorf("%w: conversation %q has run %q", store.ErrUnfinished, conversationID, r.ID)
	}
	r := store.Run{
		ID: id, ConversationID: conversationID, Status: store.StatusRunning, CreatedAt: now,
		Interactive: o.Interactive, Lane: o.Lane, Priority: o.Priority, StartAt: c.Start, Pool: o.Pool, AgentVersion: o.AgentVersion, User: o.User, Lease: c.Lease,
	}
	if err := backend.CheckTime("the clock's time", r.CreatedAt); err != nil {
		return store.Run{}, err
	}
	s.runs[k] = r
	return r, nil
}

// unfinished returns the run of a conversation that is not final. The caller
// holds s.mu.
func (s *Store) unfinished(tenant tenancy.Tenant, conversationID string) (store.Run, bool) {
	for rk, r := range s.runs {
		if rk.tenant == tenant && r.ConversationID == conversationID && !r.Status.Final() {
			return r, true
		}
	}
	return store.Run{}, false
}

// Get implements [store.RunStore].
func (s *Store) Get(ctx context.Context, id string) (store.Run, error) {
	k, err := scope(ctx, id)
	if err != nil {
		return store.Run{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.run(k)
	return cloneRun(r), err
}

// run returns the run at k. The caller holds s.mu.
func (s *Store) run(k key) (store.Run, error) {
	r, ok := s.runs[k]
	if !ok {
		return store.Run{}, fmt.Errorf("%w: run %q", store.ErrNotFound, k.id)
	}
	return r, nil
}

func cloneRun(r store.Run) store.Run {
	r.Pending = slices.Clone(r.Pending)
	return r
}

// List implements [store.RunStore].
func (s *Store) List(ctx context.Context, conversationID string, p store.Page) (store.RunPage, error) {
	k, err := scope(ctx, conversationID)
	if err != nil {
		return store.RunPage{}, err
	}
	if err := backend.CheckID("conversation", conversationID); err != nil {
		return store.RunPage{}, err
	}
	return s.page(k.tenant, p, func(r store.Run) bool { return r.ConversationID == conversationID })
}

// ListSuspended implements [store.RunStore].
func (s *Store) ListSuspended(ctx context.Context, p store.Page) (store.RunPage, error) {
	k, err := scope(ctx, "")
	if err != nil {
		return store.RunPage{}, err
	}
	return s.page(k.tenant, p, func(r store.Run) bool { return r.Status == store.StatusSuspended })
}

// sorted returns the tenant's runs that match keep, ordered by CreatedAt and
// ID. The caller holds s.mu.
func (s *Store) sorted(tenant tenancy.Tenant, keep func(store.Run) bool) []store.Run {
	var runs []store.Run
	for rk, r := range s.runs {
		if rk.tenant == tenant && keep(r) {
			runs = append(runs, cloneRun(r))
		}
	}
	slices.SortFunc(runs, func(a, b store.Run) int {
		if keyOf(a).Less(keyOf(b)) {
			return -1
		}
		return 1
	})
	return runs
}

func keyOf(r store.Run) backend.RunKey { return backend.RunKey{Created: r.CreatedAt, ID: r.ID} }

// page returns one page of the tenant's runs that match keep.
func (s *Store) page(tenant tenancy.Tenant, p store.Page, keep func(store.Run) bool) (store.RunPage, error) {
	limit, start, err := backend.ParseRunPage(p)
	if err != nil {
		return store.RunPage{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	runs := s.sorted(tenant, keep)
	if p.Order == store.Descending {
		slices.Reverse(runs)
	}
	// after reports whether r comes after the page's start in its order.
	after := func(r store.Run) bool {
		switch {
		case start.HasAfter && p.Order == store.Ascending:
			return start.After.Less(keyOf(r))
		case start.HasAfter:
			return keyOf(r).Less(start.After)
		case start.FromTime.IsZero():
			return true
		case p.Order == store.Ascending:
			return !r.CreatedAt.Before(start.FromTime)
		default:
			return !r.CreatedAt.After(start.FromTime)
		}
	}
	i := slices.IndexFunc(runs, after)
	if i < 0 {
		return store.RunPage{}, nil
	}
	end := min(i+limit, len(runs))
	page := store.RunPage{Runs: runs[i:end]}
	if end < len(runs) {
		page.Next = backend.RunCursor(p.Order, keyOf(runs[end-1]))
	}
	return page, nil
}

// Transition implements [store.RunStore].
func (s *Store) Transition(ctx context.Context, id string, from, to store.Status, d store.Details) (store.Run, error) {
	k, err := scope(ctx, id)
	if err != nil {
		return store.Run{}, err
	}
	pending, invalid := backend.CheckTransition(from, to, d)
	if invalid == nil {
		invalid = backend.CheckTime("next attempt time", d.NextAttemptAt)
	}
	now := s.now()
	if to.Final() {
		if err := backend.CheckTime("the clock's time", now); err != nil {
			return store.Run{}, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.run(k)
	if err != nil {
		return store.Run{}, err
	}
	switch {
	case r.Status.Final():
		return store.Run{}, fmt.Errorf("%w: run %q is %s", store.ErrFinal, id, r.Status)
	case invalid != nil:
		return store.Run{}, invalid
	case r.Status != from:
		return store.Run{}, fmt.Errorf("%w: run %q is %s, not %s", store.ErrConflict, id, r.Status, from)
	}
	r.Status = to
	r.Usage = r.Usage.Add(d.Usage)
	r.Error, r.Retryable, r.NextAttemptAt, r.Pending = d.Error, d.Retryable, d.NextAttemptAt, pending
	if to == store.StatusFailed {
		r.Attempts++
	}
	if to.Final() {
		r.FinishedAt = now
	}
	s.runs[k] = r
	return cloneRun(r), nil
}

// ListResumable implements [store.RunStore].
func (s *Store) ListResumable(ctx context.Context, now time.Time, limit int) ([]store.Run, error) {
	k, err := scope(ctx, "")
	if err != nil {
		return nil, err
	}
	n, err := backend.Limit(store.Page{Limit: limit})
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	runs := s.sorted(k.tenant, func(r store.Run) bool { return backend.Resumable(r, now) })
	return runs[:min(n, len(runs))], nil
}

// Admin returns the cross-tenant view of s, for the runner's recovery loop
// only. It reads the same data as s.
//
// It is a function, not a method, on purpose: *Store has no method that leads
// to other tenants' runs, so code holding s as a [store.RunStore] cannot reach
// them by a type assertion. It would have to assert to *Store, which means
// importing this package; code that serves requests has no reason to.
func Admin(s *Store) store.Admin { return admin{s} }

// admin is the [store.Admin] view of a Store.
type admin struct{ s *Store }

// ListResumableAll implements [store.Admin].
func (a admin) ListResumableAll(ctx context.Context, now time.Time, limit int) ([]store.TenantRun, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	n, err := backend.Limit(store.Page{Limit: limit})
	if err != nil {
		return nil, err
	}
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	var runs []store.TenantRun
	for k, r := range a.s.runs {
		if backend.Resumable(r, now) {
			runs = append(runs, store.TenantRun{Tenant: k.tenant, Run: cloneRun(r)})
		}
	}
	slices.SortFunc(runs, func(x, y store.TenantRun) int {
		return cmp.Or(
			x.Run.CreatedAt.Compare(y.Run.CreatedAt),
			strings.Compare(x.Tenant.ID(), y.Tenant.ID()),
			strings.Compare(x.Run.ID, y.Run.ID),
		)
	})
	return runs[:min(n, len(runs))], nil
}

// ClaimNext implements [store.Admin]: under the store's lock, the first of
// the claimable runs of the lanes in claim order gets owner's lease.
func (a admin) ClaimNext(ctx context.Context, lanes []string, owner string, ttl time.Duration) (store.TenantRun, error) {
	if err := ctx.Err(); err != nil {
		return store.TenantRun{}, err
	}
	if err := backend.CheckClaimNext(lanes, owner, ttl); err != nil {
		return store.TenantRun{}, err
	}
	now := a.s.now()
	if err := backend.CheckTime("lease expiry", now.Add(ttl)); err != nil {
		return store.TenantRun{}, err
	}
	a.s.mu.Lock()
	defer a.s.mu.Unlock()
	var (
		best  key
		found bool
	)
	for k, r := range a.s.runs {
		if !slices.Contains(lanes, r.Lane) || !backend.Claimable(r, now) {
			continue
		}
		if !found || backend.ClaimOrder(k.tenant.ID(), r, best.tenant.ID(), a.s.runs[best]) < 0 {
			best, found = k, true
		}
	}
	if !found {
		return store.TenantRun{}, fmt.Errorf("%w: no run of lanes %q is due", store.ErrNotFound, lanes)
	}
	r := a.s.runs[best]
	r.Lease = store.Lease{Owner: owner, Expires: now.Add(ttl)}
	a.s.runs[best] = r
	return store.TenantRun{Tenant: best.tenant, Run: cloneRun(r)}, nil
}

// Unfinished implements [store.RunStore].
func (s *Store) Unfinished(ctx context.Context, conversationID string) (store.Run, error) {
	k, err := scope(ctx, conversationID)
	if err != nil {
		return store.Run{}, err
	}
	if err := backend.CheckID("conversation", conversationID); err != nil {
		return store.Run{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.unfinished(k.tenant, conversationID)
	if !ok {
		return store.Run{}, fmt.Errorf("%w: conversation %q has no unfinished run", store.ErrNotFound, conversationID)
	}
	return cloneRun(r), nil
}

// Claim implements [store.Leaser].
func (s *Store) Claim(ctx context.Context, runID, owner string, ttl time.Duration) (store.Lease, error) {
	return s.lease(ctx, runID, owner, ttl, true)
}

// Renew implements [store.Leaser].
func (s *Store) Renew(ctx context.Context, runID, owner string, ttl time.Duration) (store.Lease, error) {
	return s.lease(ctx, runID, owner, ttl, false)
}

func (s *Store) lease(ctx context.Context, runID, owner string, ttl time.Duration, claim bool) (store.Lease, error) {
	k, err := scope(ctx, runID)
	if err != nil {
		return store.Lease{}, err
	}
	if err := backend.CheckLease(owner, ttl); err != nil {
		return store.Lease{}, err
	}
	now := s.now()
	if err := backend.CheckTime("lease expiry", now.Add(ttl)); err != nil {
		return store.Lease{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.run(k)
	if err != nil {
		return store.Lease{}, err
	}
	if r.Status.Final() {
		return store.Lease{}, fmt.Errorf("%w: run %q", store.ErrFinal, runID)
	}
	held := backend.Held(r.Lease, now)
	switch {
	case held && r.Lease.Owner != owner:
		return store.Lease{}, fmt.Errorf("%w: run %q", store.ErrLeased, runID)
	case !held && !claim:
		return store.Lease{}, fmt.Errorf("%w: run %q", store.ErrLeaseLost, runID)
	}
	r.Lease = store.Lease{Owner: owner, Expires: now.Add(ttl)}
	s.runs[k] = r
	return r.Lease, nil
}

// Release implements [store.Leaser].
func (s *Store) Release(ctx context.Context, runID, owner string) error {
	k, err := scope(ctx, runID)
	if err != nil {
		return err
	}
	if err := backend.CheckOwner(owner); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.run(k)
	if err != nil {
		return err
	}
	switch {
	case r.Lease.Owner == owner:
		r.Lease = store.Lease{}
		s.runs[k] = r
	case backend.Held(r.Lease, s.now()):
		return fmt.Errorf("%w: run %q", store.ErrLeased, runID)
	}
	return nil
}

// Save implements [flow.Checkpointer].
func (s *Store) Save(ctx context.Context, id string, c flow.Checkpoint) error {
	k, err := scope(ctx, id)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkpoints[k] = c.Clone()
	return nil
}

// Load implements [flow.Checkpointer].
func (s *Store) Load(ctx context.Context, id string) (flow.Checkpoint, error) {
	k, err := scope(ctx, id)
	if err != nil {
		return flow.Checkpoint{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.checkpoints[k]
	if !ok {
		return flow.Checkpoint{}, fmt.Errorf("%w: checkpoint of run %q", flow.ErrNotFound, id)
	}
	return c.Clone(), nil
}
