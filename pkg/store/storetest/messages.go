package storetest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// RunMessageLog runs the conformance suite for a [store.MessageLog] and the
// [store.MessageAdmin] over it. newStore must return an empty store that
// reads its times from clk (a fake clock the suite advances), and its
// MessageAdmin. Every test checks, when it ends, that each thread of the
// tenants it used holds its messages at hops 1 to Hops, once each, and that
// ListThreads, paged in both orders, lists every thread once.
func RunMessageLog(t *testing.T, newStore func(t *testing.T, clk clock.Clock) (store.MessageLog, store.MessageAdmin)) {
	t.Helper()
	const ttl = time.Minute

	t.Run("SendNumbersTheThread", func(t *testing.T) {
		t.Parallel()
		r := newMessageRig(t, newStore)
		a := r.ctx(tenantA)
		data := json.RawMessage("{ \"z\": 1.50,  \"a\" : \"<&>é\" }")
		first := r.mustSend(a, store.Message{
			ID: "m1", ThreadID: "t", From: "agent:a", To: "agent:b", FromConversation: "ca", ToConversation: "cb",
			Kind: store.MessageRequest, Text: "hello <&> é ✓", Data: data,
		})
		if first.Hop != 1 || first.State != store.MessageQueued || !first.CreatedAt.Equal(r.clk.Now()) ||
			first.Attempts != 0 || !first.DeliveredAt.IsZero() || !first.ProcessedAt.IsZero() || string(first.Data) != string(data) {
			t.Errorf("sent %+v, want hop 1, queued, now, never delivered, data byte for byte", first)
		}
		r.clk.Advance(time.Second)
		second := r.mustSend(a, reply("m2", "t", "m1", "cb", "ca"))
		r.mustSend(a, request("m3", "t", "ca", "cb"))
		if second.Hop != 2 {
			t.Errorf("second hop %d, want 2", second.Hop)
		}
		th, err := r.s.GetThread(a, "t")
		if err != nil {
			t.Fatal(err)
		}
		if th.Hops != 3 || th.Status != store.ThreadOpen || !th.CreatedAt.Equal(first.CreatedAt) || !th.UpdatedAt.Equal(r.clk.Now()) {
			t.Errorf("thread %+v, want 3 hops, open, created with its first message", th)
		}
		got, err := r.s.GetMessage(a, "m1")
		if err != nil || string(got.Data) != string(data) || got.Text != first.Text || got.FromConversation != "ca" {
			t.Errorf("GetMessage = %+v, %v; want the message as sent", got, err)
		}
		page, err := r.s.ThreadMessages(a, "t", 1, 1)
		if err != nil || len(page) != 1 || page[0].ID != "m2" {
			t.Errorf("ThreadMessages after 1, limit 1 = %v, %v; want [m2]", msgIDs(page), err)
		}
		if none, err := r.s.ThreadMessages(a, "no-thread", 0, 0); err != nil || len(none) != 0 {
			t.Errorf("a thread that does not exist has %v, %v; want none", msgIDs(none), err)
		}
	})

	t.Run("SendIsIdempotent", func(t *testing.T) {
		t.Parallel()
		r := newMessageRig(t, newStore)
		a := r.ctx(tenantA)
		m := request("m1", "t", "ca", "cb")
		first := r.mustSend(a, m)
		r.clk.Advance(time.Second)
		again := r.mustSend(a, m)
		if again.Hop != first.Hop || !again.CreatedAt.Equal(first.CreatedAt) {
			t.Errorf("a retry gave %+v, want the stored %+v", again, first)
		}
		changed := m
		changed.Text = "other"
		if _, err := r.s.SendMessage(a, changed); !errors.Is(err, store.ErrConflict) {
			t.Errorf("same ID, other text: %v, want ErrConflict", err)
		}
		if _, err := r.s.StopThread(a, "t", "hop_limit", "too many hops"); err != nil {
			t.Fatal(err)
		}
		if again, err := r.s.SendMessage(a, m); err != nil || again.Hop != 1 {
			t.Errorf("a retry once the thread stopped: %+v, %v; want the stored one", again, err)
		}
		if th, _ := r.s.GetThread(a, "t"); th.Hops != 1 {
			t.Errorf("retries made %d hops, want 1", th.Hops)
		}
	})

	t.Run("RefusesBadMessages", func(t *testing.T) {
		t.Parallel()
		r := newMessageRig(t, newStore)
		a := r.ctx(tenantA)
		r.mustSend(a, request("in-t", "t", "ca", "cb"))
		r.mustSend(a, request("in-u", "u", "ca", "cb"))
		ok := request("x", "t", "ca", "cb")
		for _, c := range []struct {
			name string
			edit func(*store.Message)
		}{
			{"no id", func(m *store.Message) { m.ID = "" }},
			{"no thread", func(m *store.Message) { m.ThreadID = "" }},
			{"no recipient conversation", func(m *store.Message) { m.ToConversation = "" }},
			{"no sender", func(m *store.Message) { m.From = "" }},
			{"no recipient", func(m *store.Message) { m.To = "" }},
			{"unknown kind", func(m *store.Message) { m.Kind = "shout" }},
			{"text with NUL", func(m *store.Message) { m.Text = "a\x00b" }},
			{"text not UTF-8", func(m *store.Message) { m.Text = "\xff" }},
			{"data not JSON", func(m *store.Message) { m.Data = json.RawMessage("{") }},
			{"a request in reply", func(m *store.Message) { m.InReplyTo = "in-t" }},
			{"a reply to nothing", func(m *store.Message) { m.Kind = store.MessageReply }},
			{"a reply to another thread", func(m *store.Message) { m.Kind, m.InReplyTo = store.MessageReply, "in-u" }},
			{"a reply to no message", func(m *store.Message) { m.Kind, m.InReplyTo = store.MessageReply, "nope" }},
		} {
			m := ok
			c.edit(&m)
			if _, err := r.s.SendMessage(a, m); !errors.Is(err, store.ErrInvalid) {
				t.Errorf("%s: %v, want ErrInvalid", c.name, err)
			}
		}
		if _, err := r.s.SendMessage(t.Context(), ok); !errors.Is(err, tenancy.ErrNoTenant) {
			t.Errorf("no tenant: %v, want ErrNoTenant", err)
		}
		if _, err := r.s.ThreadMessages(a, "t", -1, 0); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("negative after: %v, want ErrInvalid", err)
		}
		if _, err := r.s.ThreadMessages(a, "t", 0, -1); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("negative limit: %v, want ErrInvalid", err)
		}
		if _, err := r.s.ListThreads(a, store.Page{FromSeq: 1}); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("ListThreads seeking by seq: %v, want ErrInvalid", err)
		}
	})

	t.Run("StoppedThreadTakesNoticesOnly", func(t *testing.T) {
		t.Parallel()
		r := newMessageRig(t, newStore)
		a := r.ctx(tenantA)
		r.mustSend(a, request("m1", "t", "ca", "cb"))
		r.clk.Advance(time.Second)
		th, err := r.s.StopThread(a, "t", "hop_limit", "the thread reached 8 hops")
		if err != nil || th.Status != store.ThreadStopped || th.StopCode != "hop_limit" || !th.UpdatedAt.Equal(r.clk.Now()) {
			t.Fatalf("StopThread = %+v, %v; want stopped with its code, now", th, err)
		}
		if _, err := r.s.SendMessage(a, request("m2", "t", "cb", "ca")); !errors.Is(err, store.ErrFinal) {
			t.Errorf("a request to a stopped thread: %v, want ErrFinal", err)
		}
		if _, err := r.s.SendMessage(a, reply("m2", "t", "m1", "cb", "ca")); !errors.Is(err, store.ErrFinal) {
			t.Errorf("a reply to a stopped thread: %v, want ErrFinal", err)
		}
		n := request("n1", "t", "", "cb")
		n.Kind, n.From = store.MessageNotice, "agentkit"
		if got := r.mustSend(a, n); got.Hop != 2 {
			t.Errorf("a notice to a stopped thread got hop %d, want 2", got.Hop)
		}
		if again, err := r.s.StopThread(a, "t", "budget", "other"); err != nil || again.StopCode != "hop_limit" {
			t.Errorf("stopping again = %+v, %v; want the first code kept", again, err)
		}
		if _, err := r.s.StopThread(a, "nope", "x", ""); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("stopping no thread: %v, want ErrNotFound", err)
		}
		if _, err := r.s.StopThread(a, "t", "", "why"); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("stopping with no code: %v, want ErrInvalid", err)
		}
	})

	t.Run("ClaimAndUpdate", func(t *testing.T) {
		t.Parallel()
		r := newMessageRig(t, newStore)
		a := r.ctx(tenantA)
		r.mustSend(a, request("m1", "t", "ca", "cb"))
		r.clk.Advance(time.Second)
		m, err := r.s.ClaimMessage(a, "m1", "p1", ttl)
		if err != nil || m.Attempts != 1 || !m.DeliveredAt.Equal(r.clk.Now()) || m.Lease.Owner != "p1" || !m.Lease.Expires.Equal(r.clk.Now().Add(ttl)) {
			t.Fatalf("ClaimMessage = %+v, %v; want one attempt, delivered now, p1's lease", m, err)
		}
		delivered := m.DeliveredAt
		if _, err := r.s.ClaimMessage(a, "m1", "p2", ttl); !errors.Is(err, store.ErrLeased) {
			t.Errorf("another owner's claim: %v, want ErrLeased", err)
		}
		if _, err := r.s.UpdateMessage(a, "m1", "p2", store.MessageUpdate{State: store.MessageDone}); !errors.Is(err, store.ErrLeased) {
			t.Errorf("another owner's update: %v, want ErrLeased", err)
		}
		r.clk.Advance(ttl) // p1 stopped: its lease expires
		m, err = r.s.ClaimMessage(a, "m1", "p2", ttl)
		if err != nil || m.Attempts != 2 || !m.DeliveredAt.Equal(delivered) {
			t.Fatalf("claim after the lease expired = %+v, %v; want a second attempt, first delivery kept", m, err)
		}
		if _, err := r.s.UpdateMessage(a, "m1", "p1", store.MessageUpdate{State: store.MessageRunning}); !errors.Is(err, store.ErrLeased) {
			t.Errorf("the old owner's update: %v, want ErrLeased", err)
		}
		next := r.clk.Now().Add(time.Hour)
		m, err = r.s.UpdateMessage(a, "m1", "p2", store.MessageUpdate{State: store.MessageRunning, RunID: "run1", NextAttemptAt: next, Tokens: 40})
		if err != nil || m.State != store.MessageRunning || m.RunID != "run1" || !m.ProcessedAt.Equal(r.clk.Now()) ||
			!m.NextAttemptAt.Equal(next) || m.Lease.Owner != "" {
			t.Fatalf("UpdateMessage = %+v, %v; want running by run1, processed now, due in an hour, released", m, err)
		}
		if _, err := r.s.UpdateMessage(a, "m1", "p2", store.MessageUpdate{State: store.MessageDone}); !errors.Is(err, store.ErrLeaseLost) {
			t.Errorf("an update after the release: %v, want ErrLeaseLost", err)
		}
		if _, err := r.s.ClaimMessage(a, "m1", "p3", ttl); err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct {
			name string
			u    store.MessageUpdate
			want error
		}{
			{"back to queued", store.MessageUpdate{State: store.MessageQueued}, store.ErrInvalid},
			{"another run", store.MessageUpdate{State: store.MessageDone, RunID: "run2"}, store.ErrConflict},
			{"no state", store.MessageUpdate{}, store.ErrInvalid},
			{"negative tokens", store.MessageUpdate{State: store.MessageDone, Tokens: -1}, store.ErrInvalid},
		} {
			if _, err := r.s.UpdateMessage(a, "m1", "p3", c.u); !errors.Is(err, c.want) {
				t.Errorf("%s: %v, want %v", c.name, err, c.want)
			}
		}
		processed := r.clk.Now()
		r.clk.Advance(time.Second)
		m, err = r.s.UpdateMessage(a, "m1", "p3", store.MessageUpdate{State: store.MessageDone, RunID: "run1", Tokens: 2})
		if err != nil || m.State != store.MessageDone || m.ProcessedAt.After(processed) {
			t.Fatalf("done = %+v, %v; want done, processed when it left queued", m, err)
		}
		if _, err := r.s.ClaimMessage(a, "m1", "p3", ttl); !errors.Is(err, store.ErrFinal) {
			t.Errorf("claiming a done message: %v, want ErrFinal", err)
		}
		if _, err := r.s.UpdateMessage(a, "m1", "p3", store.MessageUpdate{State: store.MessageDone}); !errors.Is(err, store.ErrFinal) {
			t.Errorf("updating a done message: %v, want ErrFinal", err)
		}
		if th, _ := r.s.GetThread(a, "t"); th.Tokens != 42 {
			t.Errorf("thread tokens %d, want 42", th.Tokens)
		}
		if _, err := r.s.ClaimMessage(a, "nope", "p1", ttl); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("claiming no message: %v, want ErrNotFound", err)
		}
		if _, err := r.s.ClaimMessage(a, "m1", "", ttl); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("claiming with no owner: %v, want ErrInvalid", err)
		}
		if _, err := r.s.ClaimMessage(a, "m1", "p1", 0); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("claiming for no time: %v, want ErrInvalid", err)
		}
	})

	t.Run("DueInMailboxOrder", func(t *testing.T) {
		t.Parallel()
		const hold = time.Hour // outlasts the minutes this test advances
		r := newMessageRig(t, newStore)
		a, b := r.ctx(tenantA), r.ctx(tenantB)
		r.mustSend(a, request("a1", "t", "ca", "cb")) // hop 1 to cb
		r.clk.Advance(time.Second)
		r.mustSend(b, request("b1", "t", "ca", "cb")) // another tenant's thread t
		r.clk.Advance(time.Second)
		r.mustSend(a, request("a2", "t", "ca", "cb")) // hop 2 to cb: waits for a1
		r.mustSend(a, request("a3", "t", "cb", "cc")) // hop 3 to cc: its own mailbox

		claim := func() string {
			t.Helper()
			tm, err := r.admin.ClaimDueMessage(t.Context(), "d", hold)
			if errors.Is(err, store.ErrNotFound) {
				return ""
			}
			if err != nil {
				t.Fatal(err)
			}
			return tm.Tenant.ID() + "/" + tm.Message.ID
		}
		if got := claim(); got != tenantA+"/a1" {
			t.Fatalf("first claim %q, want a1, the oldest", got)
		}
		if got := claim(); got != tenantB+"/b1" {
			t.Fatalf("second claim %q, want b1", got)
		}
		if got := claim(); got != tenantA+"/a3" {
			t.Fatalf("third claim %q, want a3: a2 waits for a1, which is queued", got)
		}
		if got := claim(); got != "" {
			t.Fatalf("fourth claim %q, want none", got)
		}
		// a1 could not be processed (its recipient was busy): due again in
		// a minute, and a2 still waits behind it.
		later := r.clk.Now().Add(time.Minute)
		if _, err := r.s.UpdateMessage(a, "a1", "d", store.MessageUpdate{State: store.MessageQueued, NextAttemptAt: later}); err != nil {
			t.Fatal(err)
		}
		if got := claim(); got != "" {
			t.Fatalf("claim before a1 is due %q, want none", got)
		}
		r.clk.Advance(time.Minute)
		if got := claim(); got != tenantA+"/a1" {
			t.Fatalf("claim once a1 is due %q, want a1", got)
		}
		if _, err := r.s.UpdateMessage(a, "a1", "d", store.MessageUpdate{State: store.MessageRunning, RunID: "r1", NextAttemptAt: r.clk.Now().Add(time.Second)}); err != nil {
			t.Fatal(err)
		}
		if got := claim(); got != tenantA+"/a2" {
			t.Fatalf("claim once a1 left queued %q, want a2", got)
		}
		r.clk.Advance(time.Second)
		if got := claim(); got != tenantA+"/a1" {
			t.Fatalf("claim of the running a1 once due %q, want a1", got)
		}
		r.clk.Advance(hold)
		// The leases of b1, a3 and a2 have expired: they are due again,
		// oldest first.
		var got []string
		for id := claim(); id != ""; id = claim() {
			got = append(got, id)
		}
		if want := []string{tenantB + "/b1", tenantA + "/a2", tenantA + "/a3", tenantA + "/a1"}; !slices.Equal(got, want) {
			t.Errorf("claims once the leases expired %v, want %v", got, want)
		}
		if _, err := r.admin.ClaimDueMessage(t.Context(), "", ttl); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("no owner: %v, want ErrInvalid", err)
		}
	})

	t.Run("ConcurrentClaimsNeverShare", func(t *testing.T) {
		t.Parallel()
		r := newMessageRig(t, newStore)
		a := r.ctx(tenantA)
		const n = 12
		for i := range n {
			r.mustSend(a, request(fmt.Sprintf("m%02d", i), fmt.Sprintf("t%d", i), "ca", "cb"))
		}
		var (
			mu  sync.Mutex
			got = map[string]int{}
			wg  sync.WaitGroup
		)
		for w := range 4 {
			wg.Go(func() {
				for {
					tm, err := r.admin.ClaimDueMessage(t.Context(), fmt.Sprintf("w%d", w), ttl)
					if errors.Is(err, store.ErrNotFound) {
						return
					}
					if err != nil {
						t.Error(err)
						return
					}
					mu.Lock()
					got[tm.Message.ID]++
					mu.Unlock()
				}
			})
		}
		wg.Wait()
		if len(got) != n {
			t.Errorf("claimed %d messages, want %d", len(got), n)
		}
		for id, c := range got {
			if c != 1 {
				t.Errorf("message %s claimed %d times, want once", id, c)
			}
		}
	})

	t.Run("TenantsKeepTheirOwn", func(t *testing.T) {
		t.Parallel()
		r := newMessageRig(t, newStore)
		a, b := r.ctx(tenantA), r.ctx(tenantB)
		r.mustSend(a, request("m", "t", "ca", "cb"))
		if _, err := r.s.GetMessage(b, "m"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("another tenant's message: %v, want ErrNotFound", err)
		}
		if _, err := r.s.GetThread(b, "t"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("another tenant's thread: %v, want ErrNotFound", err)
		}
		if _, err := r.s.ClaimMessage(b, "m", "p", ttl); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("claiming another tenant's message: %v, want ErrNotFound", err)
		}
		other := request("m", "t", "x", "y")
		if got := r.mustSend(b, other); got.Hop != 1 {
			t.Errorf("the same IDs in another tenant: hop %d, want 1", got.Hop)
		}
	})

	t.Run("ListThreadsPages", func(t *testing.T) {
		t.Parallel()
		r := newMessageRig(t, newStore)
		a := r.ctx(tenantA)
		for i := range 5 {
			r.mustSend(a, request(fmt.Sprintf("m%d", i), fmt.Sprintf("t%d", i), "ca", "cb"))
			r.clk.Advance(time.Second)
		}
		if got := r.listAll(a, store.Descending, 2); !slices.Equal(got, []string{"t4", "t3", "t2", "t1", "t0"}) {
			t.Errorf("newest first: %v", got)
		}
		from := r.clk.Now().Add(-3 * time.Second) // t2's
		p, err := r.s.ListThreads(a, store.Page{FromTime: from})
		if err != nil || !slices.Equal(threadIDs(p.Threads), []string{"t2", "t3", "t4"}) {
			t.Errorf("from t2's time: %v, %v", threadIDs(p.Threads), err)
		}
	})
}

// request and reply build messages of thread th from conversation from to
// conversation to.
func request(id, th, from, to string) store.Message {
	return store.Message{ID: id, ThreadID: th, From: "agent:" + from, To: "agent:" + to, FromConversation: from, ToConversation: to, Kind: store.MessageRequest, Text: "text of " + id}
}

func reply(id, th, inReplyTo, from, to string) store.Message {
	m := request(id, th, from, to)
	m.Kind, m.InReplyTo = store.MessageReply, inReplyTo
	return m
}

func msgIDs(ms []store.Message) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}

func threadIDs(ts []store.Thread) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return out
}

type messageRig struct {
	t       *testing.T
	s       store.MessageLog
	admin   store.MessageAdmin
	clk     *clocktest.Fake
	tenants map[string]bool
}

func newMessageRig(t *testing.T, newStore func(t *testing.T, clk clock.Clock) (store.MessageLog, store.MessageAdmin)) *messageRig {
	t.Helper()
	clk := clocktest.NewFake(time.Time{})
	s, admin := newStore(t, clk)
	r := &messageRig{t: t, s: s, admin: admin, clk: clk, tenants: map[string]bool{}}
	t.Cleanup(r.check)
	return r
}

func (r *messageRig) ctx(id string) context.Context {
	r.tenants[id] = true
	return tenantCtx(r.t.Context(), r.t, id)
}

func (r *messageRig) mustSend(ctx context.Context, m store.Message) store.Message {
	r.t.Helper()
	got, err := r.s.SendMessage(ctx, m)
	if err != nil {
		r.t.Fatalf("SendMessage %s: %v", m.ID, err)
	}
	return got
}

func (r *messageRig) listAll(ctx context.Context, o store.Order, limit int) []string {
	r.t.Helper()
	var out []string
	p := store.Page{Order: o, Limit: limit}
	for {
		page, err := r.s.ListThreads(ctx, p)
		if err != nil {
			r.t.Fatalf("ListThreads: %v", err)
		}
		out = append(out, threadIDs(page.Threads)...)
		if page.Next == "" {
			return out
		}
		p.Cursor = page.Next
	}
}

// check is the suite's invariant: every thread of every tenant used holds
// hops 1 to Hops once each, in order, and lists once in both orders.
func (r *messageRig) check() {
	t := r.t
	for id := range r.tenants {
		ctx := tenantCtx(context.Background(), t, id)
		asc := r.listAll(ctx, store.Ascending, 3)
		desc := r.listAll(ctx, store.Descending, 2)
		slices.Reverse(desc)
		if !slices.Equal(asc, desc) {
			t.Errorf("tenant %s: threads ascending %v, descending reversed %v", id, asc, desc)
		}
		seen := map[string]bool{}
		for _, th := range asc {
			if seen[th] {
				t.Errorf("tenant %s: thread %s listed twice", id, th)
			}
			seen[th] = true
			got, err := r.s.GetThread(ctx, th)
			if err != nil {
				t.Errorf("tenant %s: GetThread %s: %v", id, th, err)
				continue
			}
			ms, err := r.s.ThreadMessages(ctx, th, 0, store.MaxPageLimit)
			if err != nil {
				t.Errorf("tenant %s: ThreadMessages %s: %v", id, th, err)
				continue
			}
			if len(ms) != got.Hops {
				t.Errorf("tenant %s: thread %s has %d messages, Hops %d", id, th, len(ms), got.Hops)
			}
			for i, m := range ms {
				if m.Hop != i+1 || m.ThreadID != th {
					t.Errorf("tenant %s: thread %s: message %d is %s at hop %d", id, th, i, m.ID, m.Hop)
				}
			}
		}
	}
}
