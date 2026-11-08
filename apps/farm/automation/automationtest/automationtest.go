// Package automationtest is the conformance suite every automation.Store
// passes.
package automationtest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/automation"
)

var t0 = time.Date(2026, 11, 9, 0, 30, 0, 0, time.UTC)

func auto(id string) automation.Automation {
	return automation.Automation{
		ID: id, Name: "Weekly " + id, Enabled: true,
		Trigger:   automation.Trigger{Kind: automation.OnSchedule, Schedule: "@weekly"},
		Action:    automation.Action{Kind: automation.DoRule, Rule: "weather_advice"},
		CreatedAt: t0, UpdatedAt: t0,
	}
}

// RunStore runs the suite on fresh stores from open.
func RunStore(t *testing.T, open func(t *testing.T) automation.Store) {
	t.Helper()
	ctx := context.Background()

	t.Run("automations", func(t *testing.T) {
		s := open(t)
		a, b := auto("a1"), auto("a2")
		b.CreatedAt = t0.Add(time.Second)
		for _, x := range []automation.Automation{b, a} {
			if err := s.CreateAutomation(ctx, x); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.CreateAutomation(ctx, a); !errors.Is(err, automation.ErrInvalid) {
			t.Errorf("a taken ID: %v, want ErrInvalid", err)
		}
		all, err := s.ListAutomations(ctx)
		if err != nil || len(all) != 2 || all[0].ID != "a1" || all[1].ID != "a2" {
			t.Fatalf("List = %+v, %v; want a1 then a2", all, err)
		}
		a.Enabled = false
		a.Trigger.Weather = &automation.Condition{Metric: automation.MetricRainDay, Op: ">=", Value: 50}
		if err := s.UpdateAutomation(ctx, a); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetAutomation(ctx, "a1")
		if err != nil || got.Enabled || got.Trigger.Weather == nil || got.Trigger.Weather.Value != 50 || !got.CreatedAt.Equal(t0) {
			t.Errorf("Get after update = %+v, %v", got, err)
		}
		due := t0.Add(time.Hour)
		if err := s.SetLastDue(ctx, "a1", due); err != nil {
			t.Fatal(err)
		}
		if got, _ := s.GetAutomation(ctx, "a1"); !got.LastDue.Equal(due) || got.Enabled {
			t.Errorf("SetLastDue changed more than LastDue, or not it: %+v", got)
		}
		if err := s.DeleteAutomation(ctx, "a1"); err != nil {
			t.Fatal(err)
		}
		for name, err := range map[string]error{
			"get":      func() error { _, err := s.GetAutomation(ctx, "a1"); return err }(),
			"update":   s.UpdateAutomation(ctx, a),
			"delete":   s.DeleteAutomation(ctx, "a1"),
			"last due": s.SetLastDue(ctx, "a1", due),
		} {
			if !errors.Is(err, automation.ErrNotFound) {
				t.Errorf("%s of a deleted automation: %v, want ErrNotFound", name, err)
			}
		}
	})

	t.Run("a firing is claimed once, even at once", func(t *testing.T) {
		s := open(t)
		due := t0.Add(24 * time.Hour)
		var wg sync.WaitGroup
		var mu sync.Mutex
		won := 0
		for range 8 {
			wg.Go(func() {
				ok, err := s.ClaimFiring(ctx, "a1", due)
				if err != nil {
					t.Error(err)
				}
				if ok {
					mu.Lock()
					won++
					mu.Unlock()
				}
			})
		}
		wg.Wait()
		if won != 1 {
			t.Errorf("%d claims won, want 1", won)
		}
		if ok, _ := s.ClaimFiring(ctx, "a2", due); !ok {
			t.Error("another automation's claim of the same time lost")
		}
		if ok, _ := s.ClaimFiring(ctx, "a1", due.Add(time.Minute)); !ok {
			t.Error("the next time's claim lost")
		}
	})

	t.Run("runs", func(t *testing.T) {
		s := open(t)
		for i := range 5 {
			r := automation.Run{
				ID: fmt.Sprintf("run%d", i), Automation: []string{"a1", "a2"}[i%2], Farm: "farm_x", Status: automation.RunQuiet,
				Started: t0.Add(time.Duration(i) * time.Minute), Finished: t0.Add(time.Duration(i)*time.Minute + time.Second),
			}
			if i == 4 {
				r.Farm = "farm_y"
			}
			if err := s.AddRun(ctx, r); err != nil {
				t.Fatal(err)
			}
		}
		rs, err := s.ListRuns(ctx, automation.RunFilter{Automation: "a1"})
		if err != nil || len(rs) != 3 || rs[0].ID != "run4" || rs[2].ID != "run0" {
			t.Errorf("a1's runs = %+v, %v; want run4, run2, run0", rs, err)
		}
		rs, _ = s.ListRuns(ctx, automation.RunFilter{Farm: "farm_x", Limit: 2})
		if len(rs) != 2 || rs[0].ID != "run3" {
			t.Errorf("farm_x's two newest = %+v; want run3, run2", rs)
		}
	})

	t.Run("notices: dedupe, filters, read", func(t *testing.T) {
		s := open(t)
		n := func(id, farm, key string, at time.Time) automation.Notice {
			return automation.Notice{ID: id, Farm: farm, Key: key, Title: "Heavy rain", Severity: automation.Warning, CreatedAt: at}
		}
		add := func(x automation.Notice, since time.Time) bool {
			ok, err := s.AddNotice(ctx, x, since)
			if err != nil {
				t.Fatal(err)
			}
			return ok
		}
		if !add(n("n1", "f1", "rain:f1:2026-11-12", t0), t0.Add(-time.Hour)) {
			t.Fatal("first notice not added")
		}
		if add(n("n2", "f1", "rain:f1:2026-11-12", t0.Add(time.Hour)), t0.Add(-time.Hour)) {
			t.Error("same farm and key within the window added again")
		}
		if !add(n("n3", "f1", "rain:f1:2026-11-12", t0.Add(7*24*time.Hour)), t0.Add(time.Hour)) {
			t.Error("same key after the window not added")
		}
		if !add(n("n4", "f2", "rain:f1:2026-11-12", t0.Add(time.Hour)), t0.Add(-time.Hour)) {
			t.Error("same key on another farm not added")
		}
		all, _ := s.ListNotices(ctx, automation.NoticeFilter{})
		if len(all) != 3 || all[0].ID != "n3" {
			t.Fatalf("notices = %+v; want n3, n4, n1", all)
		}
		got, _ := s.ListNotices(ctx, automation.NoticeFilter{Farm: "f1"})
		if len(got) != 2 {
			t.Errorf("f1's notices = %d, want 2", len(got))
		}
		at := t0.Add(8 * 24 * time.Hour)
		if k, err := s.MarkRead(ctx, "", []string{"n1"}, at); err != nil || k != 1 {
			t.Errorf("MarkRead(n1) = %d, %v", k, err)
		}
		got, _ = s.ListNotices(ctx, automation.NoticeFilter{Farm: "f1", Unread: true})
		if len(got) != 1 || got[0].ID != "n3" {
			t.Errorf("f1 unread = %+v, want n3", got)
		}
		all, _ = s.ListNotices(ctx, automation.NoticeFilter{Farm: "f1"})
		if all[1].ReadAt == nil || !all[1].ReadAt.Equal(at) {
			t.Errorf("n1 read at %v, want %v", all[1].ReadAt, at)
		}
		if k, _ := s.MarkRead(ctx, "f1", nil, at); k != 1 {
			t.Errorf("mark all of f1 marked %d, want 1", k)
		}
		if k, _ := s.MarkRead(ctx, "", nil, at); k != 1 {
			t.Errorf("mark all marked %d, want 1 (f2's)", k)
		}
	})

	t.Run("prune", func(t *testing.T) {
		s := open(t)
		_ = s.AddRun(ctx, automation.Run{ID: "old", Automation: "a", Farm: "f", Started: t0})
		_ = s.AddRun(ctx, automation.Run{ID: "new", Automation: "a", Farm: "f", Started: t0.Add(48 * time.Hour)})
		_, _ = s.AddNotice(ctx, automation.Notice{ID: "n", Farm: "f", Key: "k", CreatedAt: t0}, t0)
		_, _ = s.ClaimFiring(ctx, "a", t0)
		if err := s.Prune(ctx, t0.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		rs, _ := s.ListRuns(ctx, automation.RunFilter{})
		ns, _ := s.ListNotices(ctx, automation.NoticeFilter{})
		if len(rs) != 1 || rs[0].ID != "new" || len(ns) != 0 {
			t.Errorf("after prune: runs %+v, notices %+v", rs, ns)
		}
	})
}
