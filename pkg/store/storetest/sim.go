package storetest

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// SimulateConversations runs one seeded simulation against a fresh store from
// newStore: writers with stale views appending, retrying and conflicting, gaps
// and invalid entries, compactions, context reads, writers bringing a cached
// context up to date from History, history pages read while entries are
// appended between them, and calls with no tenant or a cancelled context, over
// two tenants that use the same conversation ids. Every call's result is
// predicted by an independent model of the log, and the invariants listed on
// convRig are checked at the end. A failure names its seed and step; the same
// seed replays the same calls.
//
// RunConversationStore runs a range of seeds. A backend's fuzz target can call
// it with the fuzzer's seed:
//
//	func FuzzConversationSimulation(f *testing.F) {
//		f.Add(uint64(0))
//		f.Fuzz(func(t *testing.T, seed uint64) {
//			storetest.SimulateConversations(t, newStore, seed)
//		})
//	}
func SimulateConversations(t *testing.T, newStore func(t *testing.T, clk clock.Clock) store.ConversationStore, seed uint64) {
	t.Helper()
	r := newConvRig(t, newStore)
	s := &convSim{
		t:     t,
		r:     r,
		rng:   rand.New(rand.NewPCG(seed, 0x9e3779b97f4a7c15)),
		seed:  seed,
		ctxs:  map[string]context.Context{tenantA: r.ctx(tenantA), tenantB: r.ctx(tenantB)},
		model: map[logKey][]snapshot{},
	}
	for range 3 {
		s.writers = append(s.writers, &simWriter{cache: map[logKey]*cachedContext{}})
	}
	steps := 40 + s.rng.IntN(160)
	for s.step = 0; s.step < steps; s.step++ {
		s.do()
	}
	// Finish the history reads still open.
	for len(s.pagers) > 0 {
		s.pageStep(0)
	}
}

type convSim struct {
	t     *testing.T
	r     *convRig
	rng   *rand.Rand
	seed  uint64
	step  int
	n     int // makes every generated entry unique
	ctxs  map[string]context.Context
	model map[logKey][]snapshot

	writers []*simWriter
	pagers  []*pager
}

// simWriter is a writer such as the runner: it keeps a cached context of each
// conversation and appends after the last entry it knows of, which may be
// stale.
type simWriter struct {
	cache    map[logKey]*cachedContext
	accepted *simAppend // the last entry it appended, retried as is
	lost     *simAppend // the last entry that conflicted
}

type cachedContext struct {
	seq     int64
	entries []store.Entry
}

type simAppend struct {
	k logKey
	e store.Entry
}

// pager reads History one page per step while other steps append.
type pager struct {
	k        logKey
	p        store.Page
	from     int64 // the seek, or 0
	startLen int64 // the log's length at the first page
	lastLen  int64 // the log's length at the latest page
	visited  []int64
	started  bool
}

func (s *convSim) fatalf(format string, args ...any) {
	s.t.Helper()
	s.t.Fatalf("seed %d step %d: %s", s.seed, s.step, fmt.Sprintf(format, args...))
}

func (s *convSim) pickKey() logKey {
	tenant := tenantA
	if s.rng.IntN(2) == 1 {
		tenant = tenantB
	}
	return logKey{tenant, fmt.Sprintf("c%d", s.rng.IntN(2))}
}

func (s *convSim) do() {
	w := s.writers[s.rng.IntN(len(s.writers))]
	k := s.pickKey()
	switch op := s.rng.IntN(100); {
	case op < 35:
		s.appendNext(w, k)
	case op < 42:
		if w.accepted != nil {
			s.expect("retry", w.accepted.k, w.accepted.e, nil)
		}
	case op < 46:
		if w.lost != nil {
			s.expect("retry of a lost entry", w.lost.k, w.lost.e, store.ErrConflict)
		}
	case op < 50:
		e := s.entry(int64(len(s.model[k]))+2+int64(s.rng.IntN(3)), store.KindTurn)
		s.expect("gap", k, e, store.ErrGap)
	case op < 54:
		s.appendInvalid(k)
	case op < 64:
		s.refresh(w, k)
	case op < 70:
		s.checkContext(k)
	case op < 88:
		s.pageStep(s.rng.IntN(len(s.pagers) + 2))
	case op < 95:
		s.r.clk.Advance(time.Duration(s.rng.Int64N(int64(2 * time.Second))))
	case op < 97:
		if err := s.r.append(s.t.Context(), k.conv, s.entry(1, store.KindPrompt)); !isErr(err, tenancy.ErrNoTenant) {
			s.fatalf("Append without a tenant = %v, want ErrNoTenant", err)
		}
		if _, err := s.r.s.Context(s.t.Context(), k.conv); !isErr(err, tenancy.ErrNoTenant) {
			s.fatalf("Context without a tenant = %v, want ErrNoTenant", err)
		}
	default:
		ctx, cancel := context.WithCancel(s.ctxs[k.tenant])
		cancel()
		if err := s.r.append(ctx, k.conv, s.entry(int64(len(s.model[k]))+1, store.KindTurn)); !isErr(err, context.Canceled) {
			s.fatalf("Append with a cancelled context = %v, want context.Canceled", err)
		}
		if _, err := s.r.s.History(ctx, k.conv, store.Page{}); !isErr(err, context.Canceled) {
			s.fatalf("History with a cancelled context = %v, want context.Canceled", err)
		}
	}
}

// appendNext appends after the last entry w knows of. If another writer got
// there first, the position is taken by a different entry and Append conflicts.
func (s *convSim) appendNext(w *simWriter, k logKey) {
	c := w.cachedFor(k)
	seq := c.seq + 1
	kind := store.KindTurn
	switch x := s.rng.IntN(10); {
	case seq == 1 || x < 2:
		kind = store.KindPrompt
	case x < 4:
		kind = store.KindCompaction
	case x < 5:
		kind = store.KindInterrupt
	case x < 6:
		kind = store.KindSteer
	}
	e := s.entry(seq, kind)
	var want error
	if seq <= int64(len(s.model[k])) {
		want = store.ErrConflict
	}
	if !s.expect("append", k, e, want) {
		w.lost = &simAppend{k, e}
		return
	}
	w.accepted = &simAppend{k, e}
	c.add(e)
}

// expect appends e, checks the result against want and updates the model. It
// reports whether the entry is in the log.
func (s *convSim) expect(op string, k logKey, e store.Entry, want error) bool {
	s.t.Helper()
	err := s.r.append(s.ctxs[k.tenant], k.conv, e)
	if !isErr(err, want) {
		s.fatalf("%s of %s/%s entry %d (%s; log has %d) = %v, want %v", op, k.tenant, k.conv, e.Seq, e.Kind, len(s.model[k]), err, want)
	}
	if err == nil && e.Seq == int64(len(s.model[k]))+1 {
		s.model[k] = append(s.model[k], mustSnap(s.t, e))
	}
	return err == nil
}

func (s *convSim) appendInvalid(k logKey) {
	next := int64(len(s.model[k])) + 1
	var e store.Entry
	switch s.rng.IntN(5) {
	case 0:
		e = s.entry(next, store.KindCompaction)
		e.Covers = next + int64(s.rng.IntN(3)) - 3 // anything but next-1
		if e.Covers == next-1 {
			e.Covers = next
		}
	case 1:
		e = s.entry(next, store.KindTurn)
		e.Messages = nil
	case 2:
		e = s.entry(next, store.KindTurn)
		e.Record = json.RawMessage(`{"open":`)
	case 3:
		e = s.entry(next, "summary")
	default:
		e = s.entry(next, store.KindPrompt)
		e.Covers = next
	}
	s.expect("invalid append", k, e, store.ErrInvalid)
}

// refresh brings w's cached context up to date the way the runner does:
// History from the entry after the cached seq, applied to the cache. The
// result must equal a fresh Context.
func (s *convSim) refresh(w *simWriter, k logKey) {
	c := w.cachedFor(k)
	ctx := s.ctxs[k.tenant]
	p := store.Page{FromSeq: c.seq + 1, Limit: s.rng.IntN(5)}
	for _, e := range readAll(ctx, s.t, s.r.s, k.conv, p) {
		if e.Seq != c.seq+1 {
			s.fatalf("refresh of %s/%s after %d read entry %d", k.tenant, k.conv, c.seq, e.Seq)
		}
		c.add(e)
	}
	view, err := s.r.s.Context(ctx, k.conv)
	if err != nil {
		s.fatalf("Context: %v", err)
	}
	if view.Seq != c.seq || !sameSnaps(s.t, view.Entries, c.entries) {
		s.fatalf("%s/%s: cache brought up to date is %v at %d, Context is %v at %d", k.tenant, k.conv, seqs(c.entries), c.seq, seqs(view.Entries), view.Seq)
	}
}

func (s *convSim) checkContext(k logKey) {
	view, err := s.r.s.Context(s.ctxs[k.tenant], k.conv)
	if err != nil {
		s.fatalf("Context: %v", err)
	}
	log := s.model[k]
	from := 0
	for i := len(log) - 1; i >= 0; i-- {
		if log[i].Kind == store.KindCompaction {
			from = i
			break
		}
	}
	if view.Seq != int64(len(log)) || len(view.Entries) != len(log)-from {
		s.fatalf("%s/%s: Context is %v at %d, want entries %d..%d", k.tenant, k.conv, seqs(view.Entries), view.Seq, from+1, len(log))
	}
	for i, e := range view.Entries {
		if e.Seq != int64(from+i+1) || mustSnap(s.t, e) != log[from+i] {
			s.fatalf("%s/%s: Context entry %d is not log entry %d", k.tenant, k.conv, i, from+i+1)
		}
	}
}

// pageStep starts a new history read (i >= len(s.pagers)) or reads the next
// page of pager i. When a read ends, it must have visited each entry exactly
// once: from its start to the end of the log as of its last page in Ascending
// order, from its start down to 1 in Descending order.
func (s *convSim) pageStep(i int) {
	if i >= len(s.pagers) {
		if len(s.pagers) >= 3 {
			return
		}
		pg := &pager{k: s.pickKey(), p: store.Page{Limit: s.rng.IntN(5)}}
		if s.rng.IntN(2) == 1 {
			pg.p.Order = store.Descending
		}
		if s.rng.IntN(3) == 0 {
			pg.from = 1 + int64(s.rng.IntN(len(s.model[pg.k])+2))
			pg.p.FromSeq = pg.from
		}
		s.pagers = append(s.pagers, pg)
		i = len(s.pagers) - 1
	}
	pg := s.pagers[i]
	log := s.model[pg.k]
	pg.lastLen = int64(len(log))
	if !pg.started {
		pg.startLen, pg.started = pg.lastLen, true
	}
	page, err := s.r.s.History(s.ctxs[pg.k.tenant], pg.k.conv, pg.p)
	if err != nil {
		s.fatalf("History %+v: %v", pg.p, err)
	}
	if page.Next != "" && len(page.Entries) != pageLimit(pg.p.Limit) {
		s.fatalf("History page with a Next holds %d entries, want %d", len(page.Entries), pageLimit(pg.p.Limit))
	}
	for _, e := range page.Entries {
		if e.Seq < 1 || e.Seq > int64(len(log)) || mustSnap(s.t, e) != log[e.Seq-1] {
			s.fatalf("History of %s/%s returned entry %d, which is not the log's", pg.k.tenant, pg.k.conv, e.Seq)
		}
		pg.visited = append(pg.visited, e.Seq)
	}
	if page.Next != "" {
		pg.p = store.Page{Cursor: page.Next, Limit: pg.p.Limit, Order: pg.p.Order}
		return
	}
	s.pagers = slices.Delete(s.pagers, i, i+1)
	var want []int64
	if pg.p.Order == store.Ascending {
		for seq := max(pg.from, 1); seq <= pg.lastLen; seq++ {
			want = append(want, seq)
		}
	} else {
		top := pg.startLen
		if pg.from > 0 {
			top = min(pg.from, top)
		}
		want = descendingFrom(top)
	}
	if !slices.Equal(pg.visited, want) {
		s.fatalf("History of %s/%s (order %d, limit %d, from %d) visited %v, want %v", pg.k.tenant, pg.k.conv, pg.p.Order, pg.p.Limit, pg.from, pg.visited, want)
	}
}

func (w *simWriter) cachedFor(k logKey) *cachedContext {
	c, ok := w.cache[k]
	if !ok {
		c = &cachedContext{}
		w.cache[k] = c
	}
	return c
}

// add applies the next entry to a cached context: a compaction replaces it.
func (c *cachedContext) add(e store.Entry) {
	if e.Kind == store.KindCompaction {
		c.entries = nil
	}
	c.entries = append(c.entries, e)
	c.seq = e.Seq
}

func sameSnaps(t *testing.T, a, b []store.Entry) bool {
	t.Helper()
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Seq != b[i].Seq || mustSnap(t, a[i]) != mustSnap(t, b[i]) {
			return false
		}
	}
	return true
}

// entry returns a new, unique entry at seq with random content, including
// inputs a re-encoding store would change.
func (s *convSim) entry(seq int64, kind store.Kind) store.Entry {
	s.n++
	e := store.Entry{Seq: seq, Kind: kind, RunID: fmt.Sprintf("run-%d", s.rng.IntN(3))}
	if kind == store.KindCompaction {
		e.Covers = seq - 1
	}
	switch s.rng.IntN(3) {
	case 0:
		e.Record = record(s.n)
	case 1:
		e.Record = json.RawMessage(`{}`)
	}
	n := 1 + s.rng.IntN(3)
	for m := range n {
		role := llm.RoleUser
		if m%2 == 1 {
			role = llm.RoleAssistant
		}
		msg := llm.Message{Role: role, Content: []llm.Block{llm.Text{Text: fmt.Sprintf("entry %d message %d", s.n, m)}}}
		for range s.rng.IntN(3) {
			msg.Content = append(msg.Content, s.block())
		}
		e.Messages = append(e.Messages, msg)
	}
	return e
}

func (s *convSim) block() llm.Block {
	switch s.rng.IntN(5) {
	case 0:
		return opaque(s.rng.IntN(1000))
	case 1:
		return toolUse(s.rng.IntN(1000))
	case 2:
		return llm.ToolResult{ToolUseID: "toolu_1", Content: "<&>  ", IsError: s.rng.IntN(2) == 1}
	case 3:
		return llm.Text{Text: "bad \xff utf-8 \x00"}
	}
	return llm.Opaque{Provider: "other", Data: json.RawMessage(`[1e2,  "é",{"b":1,"a":2}]`)}
}
