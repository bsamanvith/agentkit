package sqlite_test

import (
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/sqlite"
)

// Times never go backwards along a log, even when its writers' clocks
// disagree: two processes on one file, the second an hour behind. An entry
// written by the slow one takes the previous entry's time, so seeks by time
// still find every entry in order.
func TestEntryTimesNeverGoBackwardsAcrossWriters(t *testing.T) {
	t.Parallel()
	ctx := tenantCtx(t, "acme")
	path := filepath.Join(t.TempDir(), "agentkit.db")
	fast := clocktest.NewFake(time.Time{})
	slow := clocktest.NewFake(fast.Now().Add(-time.Hour))
	a := openPath(t, path, sqlite.WithClock(fast))
	b := openPath(t, path, sqlite.WithClock(slow))

	for i, s := range []*sqlite.Store{a, b, b, a} {
		if err := s.Append(ctx, "c", prompt(int64(i+1), "x")); err != nil {
			t.Fatalf("Append %d: %v", i+1, err)
		}
		fast.Advance(time.Second)
		slow.Advance(time.Second)
	}
	page, err := a.History(ctx, "c", store.Page{})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	t0 := page.Entries[0].CreatedAt
	want := []time.Time{t0, t0, t0, t0.Add(3 * time.Second)}
	for i, e := range page.Entries {
		if !e.CreatedAt.Equal(want[i]) {
			t.Errorf("entry %d created at %v, want %v", e.Seq, e.CreatedAt, want[i])
		}
	}
	page, err = b.History(ctx, "c", store.Page{FromTime: t0, Order: store.Descending})
	if err != nil || seqs(page.Entries) == nil || !slices.Equal(seqs(page.Entries), []int64{3, 2, 1}) {
		t.Errorf("History down from the first entry's time = %v, %v; want 3 2 1", seqs(page.Entries), err)
	}
}

func seqs(entries []store.Entry) []int64 {
	var out []int64
	for _, e := range entries {
		out = append(out, e.Seq)
	}
	return out
}

// A failed run marked retryable with no next attempt time is due at once:
// NextAttemptAt, the zero time, is not after any now (backend.Resumable).
func TestRetryableFailureWithNoNextAttemptIsResumable(t *testing.T) {
	t.Parallel()
	clk := clocktest.NewFake(time.Time{})
	s := open(t, clk)
	ctx := tenantCtx(t, "acme")
	if _, err := s.Create(ctx, "r", "c"); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.Claim(ctx, "r", "w", time.Hour); err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if _, err := s.Transition(ctx, "r", store.StatusRunning, store.StatusFailed, store.Details{Retryable: true}); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	runs, err := s.ListResumable(ctx, clk.Now(), 0)
	if err != nil || len(runs) != 1 || runs[0].ID != "r" {
		t.Errorf("ListResumable = %+v, %v; want the failed run", runs, err)
	}
	all, err := sqlite.Admin(s).ListResumableAll(ctx, clk.Now(), 0)
	if err != nil || len(all) != 1 || all[0].Run.ID != "r" {
		t.Errorf("ListResumableAll = %+v, %v; want the failed run", all, err)
	}
}

// Times are stored as Unix nanoseconds, which hold 1678 to 2262. A time the
// caller asks the store to keep outside that range is ErrInvalid and changes
// nothing; a time a read only compares with (now, a seek) may be anything.
func TestTimesOutsideTheStoredRange(t *testing.T) {
	t.Parallel()
	clk := clocktest.NewFake(time.Time{})
	s := open(t, clk)
	ctx := tenantCtx(t, "acme")
	far := time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC)
	early := time.Date(1, 1, 2, 0, 0, 0, 0, time.UTC)
	for _, id := range []string{"r", "f"} {
		if _, err := s.Create(ctx, id, "c-"+id); err != nil {
			t.Fatalf("Create: %v", err)
		}
	}
	if err := s.Append(ctx, "c", prompt(1, "x")); err != nil {
		t.Fatalf("Append: %v", err)
	}

	if _, err := s.Transition(ctx, "f", store.StatusRunning, store.StatusFailed,
		store.Details{Retryable: true, NextAttemptAt: far}); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("Transition with a next attempt in %d = %v, want ErrInvalid", far.Year(), err)
	}
	if _, err := s.Claim(ctx, "r", "w", far.Sub(clk.Now())); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("Claim until %d = %v, want ErrInvalid", far.Year(), err)
	}
	if r, err := s.Get(ctx, "r"); err != nil || r.Lease != (store.Lease{}) {
		t.Errorf("after the refused claim the run is %+v, %v; want it unclaimed", r, err)
	}

	if _, err := s.Transition(ctx, "f", store.StatusRunning, store.StatusFailed,
		store.Details{Retryable: true, NextAttemptAt: clk.Now()}); err != nil {
		t.Fatalf("Transition: %v", err)
	}
	for _, c := range []struct {
		now  time.Time
		want []string
	}{
		{far, []string{"f", "r"}}, // created at one instant: by id
		{early, []string{"r"}},    // unclaimed; f is not due yet
	} {
		runs, err := s.ListResumable(ctx, c.now, 0)
		var ids []string
		for _, r := range runs {
			ids = append(ids, r.ID)
		}
		if err != nil || !slices.Equal(ids, c.want) {
			t.Errorf("ListResumable at year %d = %v, %v; want %v", c.now.Year(), ids, err, c.want)
		}
	}
	for _, c := range []struct {
		p    store.Page
		want []int64
	}{
		{store.Page{FromTime: far}, nil},
		{store.Page{FromTime: early}, []int64{1}},
		{store.Page{FromTime: far, Order: store.Descending}, []int64{1}},
		{store.Page{FromTime: early, Order: store.Descending}, nil},
	} {
		page, err := s.History(ctx, "c", c.p)
		if err != nil || !slices.Equal(seqs(page.Entries), c.want) {
			t.Errorf("History %+v = %v, %v; want %v", c.p, seqs(page.Entries), err, c.want)
		}
	}
}
