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

// CommandStore is what [RunCommandLog] tests: a command log and the run store
// it is part of, on one value, as every backend has them.
type CommandStore interface {
	store.RunStore
	store.CommandLog
}

// RunCommandLog runs the conformance suite for a [store.CommandLog]. newStore
// must return an empty store that reads times from clk (a fake clock the
// suite advances).
//
// Every test goes through one helper that checks the invariants listed on
// commandRig when the test ends.
func RunCommandLog(t *testing.T, newStore func(t *testing.T, clk clock.Clock) CommandStore) {
	t.Helper()

	t.Run("SendThenPending", func(t *testing.T) {
		t.Parallel()
		r := newCommandRig(t, newStore)
		ctx := r.ctx(tenantA)
		r.mustCreate(ctx, "r")
		var sent []store.Command
		for i := range 3 {
			sent = append(sent, r.mustSend(ctx, "r", commandN(i)))
			r.clk.Advance(time.Second)
		}
		for i, c := range sent {
			if c.Seq != int64(i+1) {
				t.Errorf("command %d was stored at seq %d, want %d", i, c.Seq, i+1)
			}
		}
		got := r.pending(ctx, "r", 0)
		if !sameCommands(got, sent) {
			t.Errorf("pending = %v, want the sent commands %v", commandSeqs(got), commandSeqs(sent))
		}
		// Data with whitespace and no data both read back as sent.
		odd := store.Command{Key: "odd", Type: "steer", Data: json.RawMessage(" { \"b\":1.50,  \"a\" : \"<&>é\" } ")}
		none := store.Command{Key: "none", Type: "cancel", Data: json.RawMessage{}}
		r.mustSend(ctx, "r", odd)
		if c := r.mustSend(ctx, "r", none); c.Data != nil {
			t.Errorf("a command sent with empty data is returned with data %q, want nil", c.Data)
		}
		got = r.pending(ctx, "r", 0)
		if string(got[3].Data) != string(odd.Data) || got[4].Data != nil {
			t.Errorf("pending data = %q, %q; want %q and nil", got[3].Data, got[4].Data, odd.Data)
		}
	})

	t.Run("SendRules", func(t *testing.T) {
		t.Parallel()
		r := newCommandRig(t, newStore)
		ctx := r.ctx(tenantA)
		r.mustCreate(ctx, "r")
		r.mustSend(ctx, "r", commandN(0))
		with := func(f func(*store.Command)) store.Command { c := commandN(9); f(&c); return c }
		cases := []struct {
			name string
			run  string
			c    store.Command
			want error
		}{
			{"no key", "r", with(func(c *store.Command) { c.Key = "" }), store.ErrInvalid},
			{"key not UTF-8", "r", with(func(c *store.Command) { c.Key = "k\xff" }), store.ErrInvalid},
			{"key with a NUL", "r", with(func(c *store.Command) { c.Key = "k\x00" }), store.ErrInvalid},
			{"no type", "r", with(func(c *store.Command) { c.Type = "" }), store.ErrInvalid},
			{"type with a NUL", "r", with(func(c *store.Command) { c.Type = "a\x00b" }), store.ErrInvalid},
			{"data not JSON", "r", with(func(c *store.Command) { c.Data = json.RawMessage(`{"a":`) }), store.ErrInvalid},
			{"data not UTF-8", "r", with(func(c *store.Command) { c.Data = json.RawMessage("\"\xff\"") }), store.ErrInvalid},
			{"empty run id", "", commandN(9), store.ErrInvalid},
			{"run id with a NUL", "r\x00", commandN(9), store.ErrInvalid},
			{"no such run", "other", commandN(9), store.ErrNotFound},
			{"same key, other type", "r", with(func(c *store.Command) { c.Key = commandN(0).Key }), store.ErrConflict},
			{"same key, other data", "r", func() store.Command { c := commandN(0); c.Data = append(slices.Clone(c.Data), ' '); return c }(), store.ErrConflict},
		}
		for _, c := range cases {
			if _, err := r.s.SendCommand(ctx, c.run, c.c); !errors.Is(err, c.want) {
				t.Errorf("%s: SendCommand = %v, want %v", c.name, err, c.want)
			}
		}
		// Nothing was stored by any of them (the rig checks the whole log).
		if got := r.pending(ctx, "r", 0); len(got) != 1 {
			t.Errorf("after refused sends pending = %v, want only command 1", commandSeqs(got))
		}
	})

	t.Run("RetryReturnsTheStoredCommand", func(t *testing.T) {
		t.Parallel()
		r := newCommandRig(t, newStore)
		ctx := r.ctx(tenantA)
		r.mustCreate(ctx, "r")
		first := r.mustSend(ctx, "r", commandN(0))
		r.mustSend(ctx, "r", commandN(1))
		r.clk.Advance(time.Hour)
		again := r.mustSend(ctx, "r", commandN(0))
		if again.Seq != first.Seq || !again.CreatedAt.Equal(first.CreatedAt) {
			t.Errorf("retry returned seq %d at %v, want the stored seq %d at %v", again.Seq, again.CreatedAt, first.Seq, first.CreatedAt)
		}
		// Empty and nil data are the same command.
		empty := commandN(2)
		empty.Data = nil
		r.mustSend(ctx, "r", empty)
		empty.Data = json.RawMessage{}
		r.mustSend(ctx, "r", empty)
		if got := r.pending(ctx, "r", 0); len(got) != 3 {
			t.Errorf("after retries pending = %v, want 1-3", commandSeqs(got))
		}
		// A retry still succeeds once the run is final; a new command does not.
		r.transition(ctx, "r", store.StatusRunning, store.StatusCanceled, store.Details{Error: "stop"})
		if again := r.mustSend(ctx, "r", commandN(1)); again.Seq != 2 {
			t.Errorf("retry on a final run returned seq %d, want 2", again.Seq)
		}
		if _, err := r.s.SendCommand(ctx, "r", commandN(5)); !errors.Is(err, store.ErrFinal) {
			t.Errorf("new command to a final run = %v, want ErrFinal", err)
		}
	})

	t.Run("TimesNeverGoBackwards", func(t *testing.T) {
		t.Parallel()
		r := newCommandRig(t, newStore)
		ctx := r.ctx(tenantA)
		r.mustCreate(ctx, "r")
		a := r.mustSend(ctx, "r", commandN(0))
		if want := r.clk.Now(); !a.CreatedAt.Equal(want) {
			t.Errorf("CreatedAt = %v, want the clock's %v", a.CreatedAt, want)
		}
		r.clk.skew(-time.Hour) // a second sender whose clock is behind
		b := r.mustSend(ctx, "r", commandN(1))
		if b.CreatedAt.Before(a.CreatedAt) {
			t.Errorf("command 2 at %v is before command 1 at %v", b.CreatedAt, a.CreatedAt)
		}
	})

	t.Run("ConsumeRules", func(t *testing.T) {
		t.Parallel()
		r := newCommandRig(t, newStore)
		ctx := r.ctx(tenantA)
		r.mustCreate(ctx, "r")
		for i := range 3 {
			r.mustSend(ctx, "r", commandN(i))
		}
		cases := []struct {
			name    string
			run     string
			owner   string
			through int64
			want    error
		}{
			{"nobody holds the lease", "r", "me", 1, store.ErrLeaseLost},
			{"no such run", "other", "me", 1, store.ErrNotFound},
			{"empty owner", "r", "", 1, store.ErrInvalid},
			{"through zero", "r", "me", 0, store.ErrInvalid},
			{"empty run id", "", "me", 1, store.ErrInvalid},
		}
		for _, c := range cases {
			if err := r.s.ConsumeCommands(ctx, c.run, c.owner, c.through); !errors.Is(err, c.want) {
				t.Errorf("%s: ConsumeCommands = %v, want %v", c.name, err, c.want)
			}
		}
		r.claim(ctx, "r", "other", time.Minute)
		if err := r.s.ConsumeCommands(ctx, "r", "me", 1); !errors.Is(err, store.ErrLeased) {
			t.Errorf("consume while another owner holds the lease = %v, want ErrLeased", err)
		}
		r.clk.Advance(time.Minute) // other's lease expires
		if err := r.s.ConsumeCommands(ctx, "r", "other", 1); !errors.Is(err, store.ErrLeaseLost) {
			t.Errorf("consume with an expired lease = %v, want ErrLeaseLost", err)
		}
		r.claim(ctx, "r", "me", time.Minute)
		if err := r.s.ConsumeCommands(ctx, "r", "me", 4); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("consume past the last command = %v, want ErrInvalid", err)
		}
		if got := r.pending(ctx, "r", 0); len(got) != 3 {
			t.Errorf("after refused consumes pending = %v, want 1-3", commandSeqs(got))
		}
	})

	t.Run("ConsumeMarksAPrefix", func(t *testing.T) {
		t.Parallel()
		r := newCommandRig(t, newStore)
		ctx := r.ctx(tenantA)
		r.mustCreate(ctx, "r")
		for i := range 4 {
			r.mustSend(ctx, "r", commandN(i))
		}
		r.claim(ctx, "r", "me", time.Minute)
		r.mustConsume(ctx, "r", "me", 2)
		if got := commandSeqs(r.pending(ctx, "r", 0)); !slices.Equal(got, []int64{3, 4}) {
			t.Errorf("after consuming through 2 pending = %v, want [3 4]", got)
		}
		r.mustConsume(ctx, "r", "me", 2) // a retry
		r.mustConsume(ctx, "r", "me", 1) // behind what is consumed: nothing comes back
		if got := commandSeqs(r.pending(ctx, "r", 0)); !slices.Equal(got, []int64{3, 4}) {
			t.Errorf("after consuming again pending = %v, want [3 4]", got)
		}
		// Commands sent after a consume are pending, after the ones left.
		r.mustSend(ctx, "r", commandN(9))
		if got := commandSeqs(r.pending(ctx, "r", 0)); !slices.Equal(got, []int64{3, 4, 5}) {
			t.Errorf("after a new command pending = %v, want [3 4 5]", got)
		}
		r.mustConsume(ctx, "r", "me", 5)
		if got := r.pending(ctx, "r", 0); len(got) != 0 {
			t.Errorf("after consuming all pending = %v, want none", commandSeqs(got))
		}
		// A final run's commands can no longer be consumed.
		r.mustSend(ctx, "r", commandN(10))
		r.transition(ctx, "r", store.StatusRunning, store.StatusCompleted, store.Details{})
		if err := r.s.ConsumeCommands(ctx, "r", "me", 6); !errors.Is(err, store.ErrFinal) {
			t.Errorf("consume on a final run = %v, want ErrFinal", err)
		}
	})

	t.Run("PendingLimit", func(t *testing.T) {
		t.Parallel()
		r := newCommandRig(t, newStore)
		ctx := r.ctx(tenantA)
		r.mustCreate(ctx, "r")
		for i := range 5 {
			r.mustSend(ctx, "r", commandN(i))
		}
		r.claim(ctx, "r", "me", time.Minute)
		r.mustConsume(ctx, "r", "me", 1)
		if got := commandSeqs(r.pending(ctx, "r", 2)); !slices.Equal(got, []int64{2, 3}) {
			t.Errorf("pending with limit 2 = %v, want [2 3]", got)
		}
		if _, err := r.s.PendingCommands(ctx, "r", -1); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("negative limit = %v, want ErrInvalid", err)
		}
		if got := r.pending(ctx, "nothing-here", 0); len(got) != 0 {
			t.Errorf("pending of a run that does not exist = %v, want none", commandSeqs(got))
		}
	})

	t.Run("TenantsAreSeparate", func(t *testing.T) {
		t.Parallel()
		r := newCommandRig(t, newStore)
		a, b := r.ctx(tenantA), r.ctx(tenantB)
		r.mustCreate(a, "r")
		r.mustSend(a, "r", commandN(0))
		if _, err := r.s.SendCommand(b, "r", commandN(1)); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("tenant B sending to tenant A's run = %v, want ErrNotFound", err)
		}
		// The same run id and key in another tenant is another command.
		r.mustCreate(b, "r")
		if c := r.mustSend(b, "r", commandN(0)); c.Seq != 1 {
			t.Errorf("tenant B's first command is at seq %d, want 1", c.Seq)
		}
		r.claim(a, "r", "me", time.Minute)
		if err := r.s.ConsumeCommands(b, "r", "me", 1); !errors.Is(err, store.ErrLeaseLost) {
			t.Errorf("consume through tenant B with tenant A's lease = %v, want ErrLeaseLost", err)
		}
	})

	t.Run("ConcurrentSendsAreDense", func(t *testing.T) {
		t.Parallel()
		r := newCommandRig(t, newStore)
		ctx := r.ctx(tenantA)
		r.mustCreate(ctx, "r")
		const n = 8
		var wg sync.WaitGroup
		seqs := make([]int64, 2*n)
		for i := range 2 * n {
			wg.Go(func() {
				// Two senders per key: one stores it, the other gets it back.
				c, err := r.send(ctx, "r", commandN(i%n))
				if err != nil {
					t.Errorf("concurrent SendCommand %d: %v", i, err)
					return
				}
				seqs[i] = c.Seq
			})
		}
		wg.Wait()
		for i := range n {
			if seqs[i] != seqs[i+n] {
				t.Errorf("two sends of key %d got seqs %d and %d, want the same", i, seqs[i], seqs[i+n])
			}
		}
		got := commandSeqs(r.pending(ctx, "r", 0))
		if want := []int64{1, 2, 3, 4, 5, 6, 7, 8}; !slices.Equal(got, want) {
			t.Errorf("after concurrent sends pending = %v, want %v", got, want)
		}
	})
}

// commandN is a command whose key, type and data are unique to i.
func commandN(i int) store.Command {
	return store.Command{
		Key:  fmt.Sprintf("key-%d", i),
		Type: []string{"steer", "answer", "cancel"}[i%3],
		Data: json.RawMessage(fmt.Sprintf("{\"z\": %d,  \"text\" : \"<&>é\" }", i)),
	}
}

func commandSeqs(cs []store.Command) []int64 {
	out := make([]int64, len(cs))
	for i, c := range cs {
		out[i] = c.Seq
	}
	return out
}

// sameCommands compares everything a command reads back with: Seq, Key,
// Type, Data bytes and CreatedAt.
func sameCommands(a, b []store.Command) bool {
	return slices.EqualFunc(a, b, func(x, y store.Command) bool {
		return x.Seq == y.Seq && x.Key == y.Key && x.Type == y.Type && string(x.Data) == string(y.Data) && x.CreatedAt.Equal(y.CreatedAt)
	})
}

// commandRig is the one helper every command log test goes through. It
// records every command the store accepted and how far each run's commands
// were consumed and, when the test ends, checks on every run the test
// touched:
//
//  1. Dense: each run's accepted commands are numbered 1..n, one per key.
//  2. Pending: PendingCommands is exactly the accepted commands after the
//     highest consumed through, in order, reading back as accepted (Data
//     byte for byte, CreatedAt included).
//  3. Times: CreatedAt is set and never decreases along a run's commands.
//  4. Tenants: every run is read through every tenant the test used plus one
//     that never wrote; each sees only its own commands.
type commandRig struct {
	t   *testing.T
	s   CommandStore
	clk *skewedClock

	mu       sync.Mutex
	accepted map[logKey]map[int64]store.Command // logKey.conv is the run id
	consumed map[logKey]int64
	runs     map[string]bool
	tenants  map[string]bool
}

func newCommandRig(t *testing.T, newStore func(t *testing.T, clk clock.Clock) CommandStore) *commandRig {
	t.Helper()
	clk := &skewedClock{Fake: clocktest.NewFake(time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC))}
	r := &commandRig{
		t:        t,
		s:        newStore(t, clk),
		clk:      clk,
		accepted: map[logKey]map[int64]store.Command{},
		consumed: map[logKey]int64{},
		runs:     map[string]bool{},
		tenants:  map[string]bool{unusedTenant: true},
	}
	t.Cleanup(r.check)
	return r
}

func (r *commandRig) ctx(tenant string) context.Context {
	r.mu.Lock()
	r.tenants[tenant] = true
	r.mu.Unlock()
	return tenantCtx(r.t.Context(), r.t, tenant)
}

func (r *commandRig) mustCreate(ctx context.Context, runID string) {
	r.t.Helper()
	if _, err := r.s.Create(ctx, runID, "conv-"+runID); err != nil {
		r.t.Fatalf("Create(%q): %v", runID, err)
	}
	r.mu.Lock()
	r.runs[runID] = true
	r.mu.Unlock()
}

func (r *commandRig) claim(ctx context.Context, runID, owner string, ttl time.Duration) {
	r.t.Helper()
	if _, err := r.s.Claim(ctx, runID, owner, ttl); err != nil {
		r.t.Fatalf("Claim(%q, %q): %v", runID, owner, err)
	}
}

func (r *commandRig) transition(ctx context.Context, runID string, from, to store.Status, d store.Details) {
	r.t.Helper()
	if _, err := r.s.Transition(ctx, runID, from, to, d); err != nil {
		r.t.Fatalf("Transition(%q, %s → %s): %v", runID, from, to, err)
	}
}

// send sends c and records it if the store accepted it.
func (r *commandRig) send(ctx context.Context, runID string, c store.Command) (store.Command, error) {
	got, err := r.s.SendCommand(ctx, runID, c)
	if err != nil {
		return got, err
	}
	k := r.key(ctx, runID)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.accepted[k] == nil {
		r.accepted[k] = map[int64]store.Command{}
	}
	if prev, ok := r.accepted[k][got.Seq]; ok {
		if !sameCommands([]store.Command{prev}, []store.Command{got}) {
			r.t.Errorf("run %q: seq %d was returned as %+v and as %+v", runID, got.Seq, prev, got)
		}
		return got, nil
	}
	r.accepted[k][got.Seq] = got
	return got, nil
}

func (r *commandRig) mustSend(ctx context.Context, runID string, c store.Command) store.Command {
	r.t.Helper()
	got, err := r.send(ctx, runID, c)
	if err != nil {
		r.t.Fatalf("SendCommand(%q, %q): %v", runID, c.Key, err)
	}
	if got.Key != c.Key || got.Type != c.Type || (len(c.Data) > 0 && string(got.Data) != string(c.Data)) {
		r.t.Errorf("SendCommand(%q, %q) returned %+v, want the command sent", runID, c.Key, got)
	}
	return got
}

func (r *commandRig) mustConsume(ctx context.Context, runID, owner string, through int64) {
	r.t.Helper()
	if err := r.s.ConsumeCommands(ctx, runID, owner, through); err != nil {
		r.t.Fatalf("ConsumeCommands(%q, %q, %d): %v", runID, owner, through, err)
	}
	k := r.key(ctx, runID)
	r.mu.Lock()
	r.consumed[k] = max(r.consumed[k], through)
	r.mu.Unlock()
}

func (r *commandRig) pending(ctx context.Context, runID string, limit int) []store.Command {
	r.t.Helper()
	got, err := r.s.PendingCommands(ctx, runID, limit)
	if err != nil {
		r.t.Fatalf("PendingCommands(%q): %v", runID, err)
	}
	return got
}

func (r *commandRig) key(ctx context.Context, runID string) logKey {
	tn, err := tenancy.Require(ctx)
	if err != nil {
		r.t.Fatalf("command rig: %v", err)
	}
	return logKey{tn.ID(), runID}
}

func (r *commandRig) check() {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx := context.Background() // the test's context is done by now
	for tenant := range r.tenants {
		tctx := tenantCtx(ctx, r.t, tenant)
		for run := range r.runs {
			k := logKey{tenant, run}
			acc := r.accepted[k]
			var want []store.Command
			for seq := int64(1); seq <= int64(len(acc)); seq++ {
				c, ok := acc[seq]
				if !ok {
					r.t.Errorf("invariant: run %q of %s: commands are %v, not numbered densely from 1", run, tenant, mapKeys(acc))
					break
				}
				if seq > 1 && c.CreatedAt.Before(acc[seq-1].CreatedAt) {
					r.t.Errorf("invariant: run %q of %s: command %d at %v is before command %d", run, tenant, seq, c.CreatedAt, seq-1)
				}
				if c.CreatedAt.IsZero() {
					r.t.Errorf("invariant: run %q of %s: command %d has no CreatedAt", run, tenant, seq)
				}
				if seq > r.consumed[k] {
					want = append(want, c)
				}
			}
			keys := map[string]bool{}
			for _, c := range acc {
				if keys[c.Key] {
					r.t.Errorf("invariant: run %q of %s: key %q is stored twice", run, tenant, c.Key)
				}
				keys[c.Key] = true
			}
			got, err := r.s.PendingCommands(tctx, run, store.MaxPageLimit)
			if err != nil {
				r.t.Errorf("invariant: PendingCommands(%q) of %s: %v", run, tenant, err)
				continue
			}
			if !sameCommands(got, want) {
				r.t.Errorf("invariant: run %q of %s: pending = %v, want %v", run, tenant, commandSeqs(got), commandSeqs(want))
			}
		}
	}
}

func mapKeys[V any](m map[int64]V) []int64 {
	out := make([]int64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}
