package storetest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/store"
)

// RunTriggerStore runs the conformance suite for a [store.TriggerStore] and
// the [store.TriggerAdmin] of the same backend. newStore must return an empty
// store, and its admin, that read times from clk (a fake clock the suite
// advances).
func RunTriggerStore(t *testing.T, newStore func(t *testing.T, clk clock.Clock) (store.TriggerStore, store.TriggerAdmin)) {
	t.Helper()
	start := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	rig := func(t *testing.T) (*clocktest.Fake, store.TriggerStore, store.TriggerAdmin) {
		clk := clocktest.NewFake(start)
		s, a := newStore(t, clk)
		return clk, s, a
	}
	data := json.RawMessage(" { \"z\": 1.50, \"a\" : \"<&>é\" }\n")

	t.Run("CreateGetListDelete", func(t *testing.T) {
		t.Parallel()
		clk, s, _ := rig(t)
		ctx := tenantCtx(t.Context(), t, tenantA)
		next := start.Add(time.Hour)
		for _, id := range []string{"news", "Deploy_2", "a-b"} {
			got := mustPutTrigger(ctx, t, s, store.TriggerRecord{ID: id, Data: data, NextFireAt: next, Version: 7}, 0)
			if got.Version != 1 || !bytes.Equal(got.Data, data) || !got.NextFireAt.Equal(next) || got.Paused || got.HookID != "" {
				t.Errorf("%s: created %+v, want version 1, the data byte for byte and the next firing time", id, got)
			}
			if !got.CreatedAt.Equal(clk.Now()) || !got.UpdatedAt.Equal(clk.Now()) {
				t.Errorf("%s: created %v, updated %v; want the clock's %v", id, got.CreatedAt, got.UpdatedAt, clk.Now())
			}
			clk.Advance(time.Second)
		}
		if got, want := triggerIDs(mustListTriggers(ctx, t, s)), []string{"Deploy_2", "a-b", "news"}; !slices.Equal(got, want) {
			t.Errorf("ListTriggers %v, want %v (sorted by ID)", got, want)
		}
		got, err := s.GetTrigger(ctx, "news")
		if err != nil || !bytes.Equal(got.Data, data) || got.Version != 1 {
			t.Errorf("GetTrigger = %+v, %v", got, err)
		}
		if _, err := s.PutTrigger(ctx, store.TriggerRecord{ID: "news", Data: data}, 0); !errors.Is(err, store.ErrConflict) {
			t.Errorf("creating a trigger that exists: %v, want ErrConflict", err)
		}
		if _, err := s.GetTrigger(ctx, "nope"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("GetTrigger of none: %v, want ErrNotFound", err)
		}
		ctxB := tenantCtx(t.Context(), t, tenantB)
		if got := mustListTriggers(ctxB, t, s); len(got) != 0 {
			t.Errorf("another tenant lists %v", triggerIDs(got))
		}
		if _, err := s.GetTrigger(ctxB, "news"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("another tenant's GetTrigger: %v, want ErrNotFound", err)
		}
		// The same ID in another tenant is another trigger.
		mustPutTrigger(ctxB, t, s, store.TriggerRecord{ID: "news", Data: json.RawMessage(`{}`)}, 0)
		if err := s.DeleteTrigger(ctx, "news", 2); !errors.Is(err, store.ErrConflict) {
			t.Errorf("DeleteTrigger of another version: %v, want ErrConflict", err)
		}
		if err := s.DeleteTrigger(ctx, "news", 1); err != nil {
			t.Fatalf("DeleteTrigger: %v", err)
		}
		if err := s.DeleteTrigger(ctx, "news", 0); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("DeleteTrigger again: %v, want ErrNotFound", err)
		}
		if _, err := s.GetTrigger(ctxB, "news"); err != nil {
			t.Errorf("deleting tenant A's trigger removed tenant B's: %v", err)
		}
		if err := s.DeleteTrigger(ctx, "a-b", 0); err != nil {
			t.Errorf("DeleteTrigger of any version: %v", err)
		}
	})

	t.Run("ReplaceIsCompareAndSwap", func(t *testing.T) {
		t.Parallel()
		clk, s, _ := rig(t)
		ctx := tenantCtx(t.Context(), t, tenantA)
		v1 := mustPutTrigger(ctx, t, s, store.TriggerRecord{ID: "t", Data: json.RawMessage(`{"n":1}`)}, 0)
		clk.Advance(time.Minute)
		next := start.Add(2 * time.Hour)
		v2 := mustPutTrigger(ctx, t, s, store.TriggerRecord{ID: "t", Data: json.RawMessage(`{"n":2}`), Paused: true, NextFireAt: next, HookID: "hk_1"}, 1)
		if v2.Version != 2 || !v2.CreatedAt.Equal(v1.CreatedAt) || !v2.UpdatedAt.Equal(clk.Now()) || !v2.Paused || !v2.NextFireAt.Equal(next) || v2.HookID != "hk_1" {
			t.Errorf("replaced %+v, want version 2, created as first, updated now, paused, the new next time and hook", v2)
		}
		if _, err := s.PutTrigger(ctx, store.TriggerRecord{ID: "t", Data: data}, 1); !errors.Is(err, store.ErrConflict) {
			t.Errorf("replacing an old version: %v, want ErrConflict", err)
		}
		if _, err := s.PutTrigger(ctx, store.TriggerRecord{ID: "none", Data: data}, 1); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("replacing a trigger that does not exist: %v, want ErrNotFound", err)
		}
		got, err := s.GetTrigger(ctx, "t")
		if err != nil || got.Version != 2 || string(got.Data) != `{"n":2}` || !got.Paused {
			t.Errorf("GetTrigger after the failed replaces = %+v, %v", got, err)
		}
	})

	t.Run("HookIDsAreUniqueAcrossTenants", func(t *testing.T) {
		t.Parallel()
		_, s, a := rig(t)
		ctxA, ctxB := tenantCtx(t.Context(), t, tenantA), tenantCtx(t.Context(), t, tenantB)
		mustPutTrigger(ctxA, t, s, store.TriggerRecord{ID: "gh", Data: data, HookID: "hk_secret"}, 0)
		if _, err := s.PutTrigger(ctxB, store.TriggerRecord{ID: "other", Data: data, HookID: "hk_secret"}, 0); !errors.Is(err, store.ErrConflict) {
			t.Errorf("another tenant's trigger with the same hook: %v, want ErrConflict", err)
		}
		if _, err := s.PutTrigger(ctxA, store.TriggerRecord{ID: "other", Data: data, HookID: "hk_secret"}, 0); !errors.Is(err, store.ErrConflict) {
			t.Errorf("another trigger with the same hook: %v, want ErrConflict", err)
		}
		// Replacing a trigger keeps its own hook.
		mustPutTrigger(ctxA, t, s, store.TriggerRecord{ID: "gh", Data: json.RawMessage(`{}`), HookID: "hk_secret"}, 1)
		got, err := a.TriggerByHook(t.Context(), "hk_secret")
		if err != nil || got.Tenant.ID() != tenantA || got.Trigger.ID != "gh" || got.Trigger.Version != 2 {
			t.Errorf("TriggerByHook = %+v, %v; want tenant A's gh at version 2", got, err)
		}
		for _, h := range []string{"", "hk_nope"} {
			if _, err := a.TriggerByHook(t.Context(), h); !errors.Is(err, store.ErrNotFound) {
				t.Errorf("TriggerByHook(%q): %v, want ErrNotFound", h, err)
			}
		}
		// A new hook frees the old one.
		mustPutTrigger(ctxA, t, s, store.TriggerRecord{ID: "gh", Data: data, HookID: "hk_rotated"}, 2)
		if _, err := a.TriggerByHook(t.Context(), "hk_secret"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("TriggerByHook of a replaced hook: %v, want ErrNotFound", err)
		}
		mustPutTrigger(ctxB, t, s, store.TriggerRecord{ID: "other", Data: data, HookID: "hk_secret"}, 0)
		if err := s.DeleteTrigger(ctxA, "gh", 0); err != nil {
			t.Fatal(err)
		}
		if _, err := a.TriggerByHook(t.Context(), "hk_rotated"); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("TriggerByHook of a deleted trigger: %v, want ErrNotFound", err)
		}
	})

	t.Run("RefusesInvalid", func(t *testing.T) {
		t.Parallel()
		_, s, _ := rig(t)
		ctx := tenantCtx(t.Context(), t, tenantA)
		for name, c := range map[string]struct {
			t store.TriggerRecord
			v int64
		}{
			"empty id":      {store.TriggerRecord{Data: data}, 0},
			"id with space": {store.TriggerRecord{ID: "a b", Data: data}, 0},
			"long id":       {store.TriggerRecord{ID: strings.Repeat("a", 65), Data: data}, 0},
			"no data":       {store.TriggerRecord{ID: "t"}, 0},
			"not JSON":      {store.TriggerRecord{ID: "t", Data: json.RawMessage(`{`)}, 0},
			"not UTF-8":     {store.TriggerRecord{ID: "t", Data: json.RawMessage("\"\xff\"")}, 0},
			"too big":       {store.TriggerRecord{ID: "t", Data: json.RawMessage(`"` + strings.Repeat("a", store.MaxTriggerBytes) + `"`)}, 0},
			"bad hook":      {store.TriggerRecord{ID: "t", Data: data, HookID: "hk/1"}, 0},
			"negative":      {store.TriggerRecord{ID: "t", Data: data}, -1},
		} {
			if _, err := s.PutTrigger(ctx, c.t, c.v); !errors.Is(err, store.ErrInvalid) {
				t.Errorf("%s: PutTrigger = %v, want ErrInvalid", name, err)
			}
		}
		if got := mustListTriggers(ctx, t, s); len(got) != 0 {
			t.Errorf("refused puts stored %v", triggerIDs(got))
		}
		if err := s.DeleteTrigger(ctx, "a b", 0); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("DeleteTrigger of a bad id: %v, want ErrInvalid", err)
		}
	})

	t.Run("ScheduleFiresEachDueTimeOnce", func(t *testing.T) {
		t.Parallel()
		clk, s, _ := rig(t)
		ctx := tenantCtx(t.Context(), t, tenantA)
		due := start.Add(time.Hour)
		mustPutTrigger(ctx, t, s, store.TriggerRecord{ID: "hourly", Data: data, NextFireAt: due}, 0)
		clk.Advance(time.Hour)
		next := due.Add(time.Hour)
		if _, err := s.StartFiring(ctx, "hourly", store.Firing{ID: "f1", ScheduledFor: due}, due.Add(time.Minute), next); !errors.Is(err, store.ErrConflict) {
			t.Errorf("firing a due time the trigger is not at: %v, want ErrConflict", err)
		}
		f, err := s.StartFiring(ctx, "hourly", store.Firing{ID: "f1", ScheduledFor: due, State: store.FiringStarted, RunID: "ignored", Seq: 9}, due, next)
		if err != nil {
			t.Fatalf("StartFiring: %v", err)
		}
		if f.ID != "f1" || f.Seq != 1 || f.State != store.FiringPending || !f.At.Equal(clk.Now()) || !f.ScheduledFor.Equal(due) || f.RunID != "" || !f.EndedAt.IsZero() {
			t.Errorf("started %+v, want pending f1 at Seq 1, now, for the due time", f)
		}
		got, err := s.GetTrigger(ctx, "hourly")
		if err != nil || !got.NextFireAt.Equal(next) {
			t.Errorf("after the firing the trigger is due at %v (%v), want %v", got.NextFireAt, err, next)
		}
		// The same due time again, under another ID: another process lost.
		if _, err := s.StartFiring(ctx, "hourly", store.Firing{ID: "f1b", ScheduledFor: due}, due, next); !errors.Is(err, store.ErrConflict) {
			t.Errorf("firing a due time twice: %v, want ErrConflict", err)
		}
		for name, c := range map[string]struct{ due, next time.Time }{
			"next not after due":  {next, next},
			"next with no due":    {time.Time{}, next.Add(time.Hour)},
			"next before the due": {next, due},
		} {
			if _, err := s.StartFiring(ctx, "hourly", store.Firing{ID: "x"}, c.due, c.next); !errors.Is(err, store.ErrInvalid) {
				t.Errorf("%s: StartFiring = %v, want ErrInvalid", name, err)
			}
		}
		// Paused: never due, and a due time is not fired.
		tr := mustPutTrigger(ctx, t, s, store.TriggerRecord{ID: "hourly", Data: data, NextFireAt: next, Paused: true}, got.Version)
		if _, err := s.StartFiring(ctx, "hourly", store.Firing{ID: "f2"}, next, next.Add(time.Hour)); !errors.Is(err, store.ErrConflict) {
			t.Errorf("firing a paused trigger: %v, want ErrConflict", err)
		}
		mustPutTrigger(ctx, t, s, store.TriggerRecord{ID: "hourly", Data: data, NextFireAt: next}, tr.Version)
		// Firing to no next time: the schedule has ended.
		if _, err := s.StartFiring(ctx, "hourly", store.Firing{ID: "f2", ScheduledFor: next}, next, time.Time{}); err != nil {
			t.Fatalf("StartFiring with no next: %v", err)
		}
		if got, _ := s.GetTrigger(ctx, "hourly"); !got.NextFireAt.IsZero() {
			t.Errorf("next firing time %v, want none", got.NextFireAt)
		}
		if _, err := s.StartFiring(ctx, "none", store.Firing{ID: "f"}, time.Time{}, time.Time{}); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("StartFiring of no trigger: %v, want ErrNotFound", err)
		}
		if _, err := s.StartFiring(tenantCtx(t.Context(), t, tenantB), "hourly", store.Firing{ID: "f9"}, time.Time{}, time.Time{}); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("StartFiring of another tenant's trigger: %v, want ErrNotFound", err)
		}
	})

	t.Run("ConcurrentFiringsOfOneDueTimeOneWins", func(t *testing.T) {
		t.Parallel()
		_, s, _ := rig(t)
		ctx := tenantCtx(t.Context(), t, tenantA)
		due := start
		mustPutTrigger(ctx, t, s, store.TriggerRecord{ID: "t", Data: data, NextFireAt: due}, 0)
		const n = 8
		var (
			wg  sync.WaitGroup
			mu  sync.Mutex
			won []string
		)
		for i := range n {
			wg.Go(func() {
				// Half the processes name the firing alike, half differently:
				// either way the due time is fired once.
				id := fmt.Sprintf("f%d", i%2)
				_, err := s.StartFiring(ctx, "t", store.Firing{ID: id, ScheduledFor: due}, due, due.Add(time.Hour))
				switch {
				case err == nil:
					mu.Lock()
					won = append(won, id)
					mu.Unlock()
				case !errors.Is(err, store.ErrConflict):
					t.Errorf("StartFiring: %v, want nil or ErrConflict", err)
				}
			})
		}
		wg.Wait()
		if len(won) != 1 {
			t.Errorf("%d of %d firings of one due time started (%v), want exactly 1", len(won), n, won)
		}
		if fs := mustListFirings(ctx, t, s, "t", 0); len(fs) != 1 {
			t.Errorf("%d firings recorded, want 1", len(fs))
		}
	})

	t.Run("WebhookFiringsAndRetries", func(t *testing.T) {
		t.Parallel()
		clk, s, _ := rig(t)
		ctx := tenantCtx(t.Context(), t, tenantA)
		next := start.Add(time.Hour)
		mustPutTrigger(ctx, t, s, store.TriggerRecord{ID: "gh", Data: data, HookID: "hk", NextFireAt: next}, 0)
		f1, err := s.StartFiring(ctx, "gh", store.Firing{ID: "msg_1"}, time.Time{}, time.Time{})
		if err != nil || f1.Seq != 1 || f1.State != store.FiringPending {
			t.Fatalf("StartFiring = %+v, %v", f1, err)
		}
		if got, _ := s.GetTrigger(ctx, "gh"); !got.NextFireAt.Equal(next) {
			t.Errorf("a webhook firing moved the next firing time to %v", got.NextFireAt)
		}
		// A replay of a pending or ended delivery is refused.
		if _, err := s.StartFiring(ctx, "gh", store.Firing{ID: "msg_1"}, time.Time{}, time.Time{}); !errors.Is(err, store.ErrConflict) {
			t.Errorf("a pending delivery again: %v, want ErrConflict", err)
		}
		clk.Advance(time.Second)
		ended, err := s.EndFiring(ctx, "gh", "msg_1", store.FiringEnd{State: store.FiringStarted, RunID: "run_1"})
		if err != nil || ended.State != store.FiringStarted || ended.RunID != "run_1" || !ended.EndedAt.Equal(clk.Now()) || !ended.At.Equal(f1.At) || ended.Seq != 1 {
			t.Errorf("EndFiring = %+v, %v", ended, err)
		}
		if _, err := s.StartFiring(ctx, "gh", store.Firing{ID: "msg_1"}, time.Time{}, time.Time{}); !errors.Is(err, store.ErrConflict) {
			t.Errorf("a started delivery again: %v, want ErrConflict", err)
		}
		if _, err := s.EndFiring(ctx, "gh", "msg_1", store.FiringEnd{State: store.FiringFailed}); !errors.Is(err, store.ErrFinal) {
			t.Errorf("ending an ended firing: %v, want ErrFinal", err)
		}
		// One that failed may be started again: the sender retries.
		if _, err := s.StartFiring(ctx, "gh", store.Firing{ID: "msg_2"}, time.Time{}, time.Time{}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.EndFiring(ctx, "gh", "msg_2", store.FiringEnd{State: store.FiringFailed, Reason: "too many background runs"}); err != nil {
			t.Fatal(err)
		}
		clk.Advance(time.Second)
		again, err := s.StartFiring(ctx, "gh", store.Firing{ID: "msg_2"}, time.Time{}, time.Time{})
		if err != nil || again.Seq != 3 || again.State != store.FiringPending || again.Reason != "" || !again.At.Equal(clk.Now()) || !again.EndedAt.IsZero() {
			t.Errorf("starting a failed firing again = %+v, %v; want it pending again at Seq 3", again, err)
		}
		fs := mustListFirings(ctx, t, s, "gh", 0)
		if got := firingIDs(fs); !slices.Equal(got, []string{"msg_2", "msg_1"}) {
			t.Errorf("ListFirings %v, want msg_2 then msg_1 (newest first, a retried one once)", got)
		}
		if _, err := s.EndFiring(ctx, "gh", "msg_9", store.FiringEnd{State: store.FiringSkipped}); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("EndFiring of none: %v, want ErrNotFound", err)
		}
		for name, end := range map[string]store.FiringEnd{
			"pending": {State: store.FiringPending},
			"no":      {},
			"other":   {State: "done"},
			"run id":  {State: store.FiringStarted, RunID: "\x00"},
			"reason":  {State: store.FiringSkipped, Reason: "\xff"},
		} {
			if _, err := s.EndFiring(ctx, "gh", "msg_2", end); !errors.Is(err, store.ErrInvalid) {
				t.Errorf("EndFiring to %s: %v, want ErrInvalid", name, err)
			}
		}
		for name, f := range map[string]store.Firing{"no id": {}, "long id": {ID: strings.Repeat("m", 129)}, "nul": {ID: "m\x00"}} {
			if _, err := s.StartFiring(ctx, "gh", f, time.Time{}, time.Time{}); !errors.Is(err, store.ErrInvalid) {
				t.Errorf("StartFiring with %s: %v, want ErrInvalid", name, err)
			}
		}
	})

	// Firings younger than FiringKeep stay beyond MaxFirings: a delivery's
	// ID stays taken while a replay of it could be accepted.
	t.Run("FiringsKeptForReplay", func(t *testing.T) {
		t.Parallel()
		clk, s, _ := rig(t)
		ctx := tenantCtx(t.Context(), t, tenantA)
		mustPutTrigger(ctx, t, s, store.TriggerRecord{ID: "t", Data: data}, 0)
		start := func(id string) error {
			_, err := s.StartFiring(ctx, "t", store.Firing{ID: id}, time.Time{}, time.Time{})
			return err
		}
		for i := range store.MaxFirings + 3 {
			if err := start(fmt.Sprintf("f%03d", i)); err != nil {
				t.Fatal(err)
			}
			clk.Advance(time.Second)
		}
		if err := start("f000"); !errors.Is(err, store.ErrConflict) {
			t.Errorf("replaying the oldest of %d firings started within %v: %v, want ErrConflict", store.MaxFirings+3, store.FiringKeep, err)
		}
		clk.Advance(store.FiringKeep)
		if err := start("next"); err != nil {
			t.Fatal(err)
		}
		if err := start("f000"); err != nil {
			t.Errorf("a firing older than FiringKeep and beyond MaxFirings: %v, want it dropped", err)
		}
	})

	t.Run("ListFirings", func(t *testing.T) {
		t.Parallel()
		clk, s, _ := rig(t)
		ctx := tenantCtx(t.Context(), t, tenantA)
		mustPutTrigger(ctx, t, s, store.TriggerRecord{ID: "t", Data: data}, 0)
		mustPutTrigger(tenantCtx(t.Context(), t, tenantB), t, s, store.TriggerRecord{ID: "t", Data: data}, 0)
		for i := range store.MaxFirings + 3 {
			if _, err := s.StartFiring(ctx, "t", store.Firing{ID: fmt.Sprintf("f%03d", i)}, time.Time{}, time.Time{}); err != nil {
				t.Fatal(err)
			}
			clk.Advance(time.Millisecond)
			if i == 2 {
				clk.Advance(store.FiringKeep) // the first three are old enough to go
			}
		}
		fs := mustListFirings(ctx, t, s, "t", store.MaxPageLimit)
		if len(fs) != store.MaxFirings || fs[0].ID != fmt.Sprintf("f%03d", store.MaxFirings+2) || fs[len(fs)-1].ID != "f003" {
			t.Errorf("%d firings kept, %s to %s; want the newest %d", len(fs), fs[0].ID, fs[len(fs)-1].ID, store.MaxFirings)
		}
		for i := 1; i < len(fs); i++ {
			if fs[i].Seq >= fs[i-1].Seq {
				t.Fatalf("firings not newest first: Seq %d after %d", fs[i].Seq, fs[i-1].Seq)
			}
		}
		if got := mustListFirings(ctx, t, s, "t", 2); len(got) != 2 || got[0].ID != fs[0].ID {
			t.Errorf("ListFirings limit 2 = %v", firingIDs(got))
		}
		// The oldest firing is gone: its ID may fire again.
		if _, err := s.StartFiring(ctx, "t", store.Firing{ID: "f000"}, time.Time{}, time.Time{}); err != nil {
			t.Errorf("a firing ID dropped from the log: %v", err)
		}
		if got := mustListFirings(tenantCtx(t.Context(), t, tenantB), t, s, "t", 0); len(got) != 0 {
			t.Errorf("another tenant's trigger has %d firings", len(got))
		}
		if _, err := s.ListFirings(ctx, "none", 0); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("ListFirings of no trigger: %v, want ErrNotFound", err)
		}
		if _, err := s.ListFirings(ctx, "t", -1); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("ListFirings limit -1: %v, want ErrInvalid", err)
		}
		// Deleting a trigger deletes its firings.
		if err := s.DeleteTrigger(ctx, "t", 0); err != nil {
			t.Fatal(err)
		}
		mustPutTrigger(ctx, t, s, store.TriggerRecord{ID: "t", Data: data}, 0)
		if got := mustListFirings(ctx, t, s, "t", 0); len(got) != 0 {
			t.Errorf("a trigger created again has the old one's %d firings", len(got))
		}
	})

	t.Run("DueTriggers", func(t *testing.T) {
		t.Parallel()
		_, s, a := rig(t)
		ctxA, ctxB := tenantCtx(t.Context(), t, tenantA), tenantCtx(t.Context(), t, tenantB)
		at := func(m int) time.Time { return start.Add(time.Duration(m) * time.Minute) }
		mustPutTrigger(ctxA, t, s, store.TriggerRecord{ID: "b", Data: data, NextFireAt: at(1)}, 0)
		mustPutTrigger(ctxA, t, s, store.TriggerRecord{ID: "a", Data: data, NextFireAt: at(1)}, 0)
		mustPutTrigger(ctxB, t, s, store.TriggerRecord{ID: "a", Data: data, NextFireAt: at(1)}, 0)
		mustPutTrigger(ctxB, t, s, store.TriggerRecord{ID: "early", Data: data, NextFireAt: at(-5)}, 0)
		mustPutTrigger(ctxA, t, s, store.TriggerRecord{ID: "paused", Data: data, NextFireAt: at(-9), Paused: true}, 0)
		mustPutTrigger(ctxA, t, s, store.TriggerRecord{ID: "hook", Data: data, HookID: "hk"}, 0)
		mustPutTrigger(ctxA, t, s, store.TriggerRecord{ID: "later", Data: data, NextFireAt: at(2)}, 0)
		due := func(now time.Time, limit int) []string {
			t.Helper()
			ts, err := a.DueTriggers(t.Context(), now, limit)
			if err != nil {
				t.Fatalf("DueTriggers: %v", err)
			}
			var out []string
			for _, tt := range ts {
				out = append(out, tt.Tenant.ID()+"/"+tt.Trigger.ID)
				if !bytes.Equal(tt.Trigger.Data, data) {
					t.Errorf("%s: data %q", tt.Trigger.ID, tt.Trigger.Data)
				}
			}
			return out
		}
		if got, want := due(at(1), 0), []string{tenantB + "/early", tenantA + "/a", tenantA + "/b", tenantB + "/a"}; !slices.Equal(got, want) {
			t.Errorf("DueTriggers = %v, want %v (by time, tenant, ID; not paused, hooks or later ones)", got, want)
		}
		if got := due(at(1), 2); len(got) != 2 {
			t.Errorf("DueTriggers limit 2 = %v", got)
		}
		if got := due(at(-6), 0); len(got) != 0 {
			t.Errorf("DueTriggers before any is due = %v", got)
		}
		if _, err := a.DueTriggers(t.Context(), at(0), -1); !errors.Is(err, store.ErrInvalid) {
			t.Errorf("DueTriggers limit -1: %v, want ErrInvalid", err)
		}
	})
}

func mustPutTrigger(ctx context.Context, t *testing.T, s store.TriggerStore, tr store.TriggerRecord, ifVersion int64) store.TriggerRecord {
	t.Helper()
	got, err := s.PutTrigger(ctx, tr, ifVersion)
	if err != nil {
		t.Fatalf("PutTrigger(%s, %d): %v", tr.ID, ifVersion, err)
	}
	return got
}

func mustListTriggers(ctx context.Context, t *testing.T, s store.TriggerStore) []store.TriggerRecord {
	t.Helper()
	got, err := s.ListTriggers(ctx)
	if err != nil {
		t.Fatalf("ListTriggers: %v", err)
	}
	return got
}

func mustListFirings(ctx context.Context, t *testing.T, s store.TriggerStore, id string, limit int) []store.Firing {
	t.Helper()
	got, err := s.ListFirings(ctx, id, limit)
	if err != nil {
		t.Fatalf("ListFirings(%s): %v", id, err)
	}
	return got
}

func triggerIDs(ts []store.TriggerRecord) []string {
	out := []string{}
	for _, t := range ts {
		out = append(out, t.ID)
	}
	return out
}

func firingIDs(fs []store.Firing) []string {
	out := []string{}
	for _, f := range fs {
		out = append(out, f.ID)
	}
	return out
}
