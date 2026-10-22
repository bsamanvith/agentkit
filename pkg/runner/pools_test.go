package runner_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/runner/pool"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/memory"
)

// watchedSlots are a pool's slots that say when the dispatcher starts to
// wait for one.
type watchedSlots struct {
	*pool.Slots
	waiting chan struct{}
}

func (s watchedSlots) Acquire(ctx context.Context) error {
	select {
	case s.waiting <- struct{}{}:
	default:
	}
	return s.Slots.Acquire(ctx)
}

// T105: a background pool's dispatcher claims a run only while the pool has
// a free slot. With the pool's one slot taken (by a chat), an enqueued run
// that is due waits in the store, unclaimed, however long; once the slot is
// free the dispatcher starts it, holding the slot while it runs, in the
// pool's placement, and gives the slot back when it ends.
func TestBackgroundRunWaitsForItsPoolsSlot(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	p := w.process()
	slots := watchedSlots{Slots: pool.NewSlots(1), waiting: make(chan struct{}, 1)}
	if !slots.TryAcquire() {
		t.Fatal("a pool of one slot has none free")
	}
	prompt := w.model.on("bg", 1)
	run, err := p.r.Enqueue(w.ctx, "c", harness.UserText(prompt), runner.Lane("bg"), runner.InPool("bg"))
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if run.Lane != "bg" || run.Pool != "bg" {
		t.Fatalf("Enqueue = lane %q, pool %q; want both bg", run.Lane, run.Pool)
	}

	heldWhileRunning := make(chan bool, 1)
	execute := func(_ context.Context, x *runner.Execution) {
		free := slots.TryAcquire()
		if free {
			slots.Release()
		}
		heldWhileRunning <- !free
		if _, err := x.Wait(); err != nil {
			t.Error(err)
		}
	}
	rc := w.recovery(p, runner.WithLanes("bg"), runner.WithSlots(slots), runner.WithInterval(30*time.Second), runner.WithExecute(execute))
	ctx, cancel := context.WithCancel(w.ctx)
	reports := make(chan runner.Report, 8)
	done := make(chan error, 1)
	go func() {
		done <- rc.Run(ctx, func(rep runner.Report, err error) {
			if err != nil {
				t.Error(err)
			}
			reports <- rep
		})
	}()
	select {
	case <-slots.waiting:
	case <-time.After(10 * time.Second):
		t.Fatal("the dispatcher never waited for a slot of its pool")
	}
	w.clk.Advance(time.Hour) // a long wait changes nothing
	if got := w.get(run.ID); got.Lease != (store.Lease{}) || got.Status != store.StatusRunning {
		t.Fatalf("with its pool full the run is %+v, want running and unclaimed", got)
	}
	if n := len(w.model.requestsFor(prompt)); n != 0 {
		t.Fatalf("with its pool full the run made %d model requests, want none", n)
	}
	select {
	case rep := <-reports:
		t.Fatalf("a pass ended (%+v) while the pool was full", rep)
	default:
	}

	slots.Release() // the chat ends
	if rep := <-reports; rep.Resumed != 1 {
		t.Fatalf("the pass once a slot is free = %+v, want the run started", rep)
	}
	if !<-heldWhileRunning {
		t.Error("the run ran without holding its pool's slot")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
	if got := w.get(run.ID); got.Status != store.StatusCompleted || got.Pool != "bg" {
		t.Errorf("run = %+v, want completed in pool bg", got)
	}
	if slots.InUse() != 0 {
		t.Errorf("%d slots in use after the run, want none", slots.InUse())
	}
	w.mu.Lock()
	placements := slices.Clone(w.placements)
	w.mu.Unlock()
	want := runner.Placement{ConversationID: "c", RunID: run.ID, Resume: true, Pool: "bg"}
	if len(placements) != 1 || placements[0] != want {
		t.Errorf("placements = %+v, want %+v", placements, want)
	}
}

// InPool stores the run's pool and gives it to the factory on the start and
// on every resume; a pool name the store cannot keep is refused.
func TestRunKeepsItsPool(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	p := w.process()
	chat := w.interrupted(p, "chat", w.model.on("q", 2), 1, runner.InPool("chats"))
	if got := w.get(chat); got.Pool != "chats" {
		t.Errorf("the run's stored pool is %q, want chats", got.Pool)
	}
	if _, err := p.resume(chat); err != nil {
		t.Fatalf("resume: %v", err)
	}
	want := []runner.Placement{
		{ConversationID: "chat", Interactive: true, Pool: "chats"},
		{ConversationID: "chat", RunID: chat, Interactive: true, Resume: true, Pool: "chats"},
	}
	w.mu.Lock()
	got := slices.Clone(w.placements)
	w.mu.Unlock()
	if !slices.Equal(got, want) {
		t.Errorf("placements = %+v, want %+v", got, want)
	}
	if _, err := p.r.Run(w.ctx, "bad", harness.UserText(w.model.on("b", 0)), runner.InPool("a\x00b")); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("Run in pool %q = %v, want store.ErrInvalid", "a\x00b", err)
	}
}

// AgentVersion stores the version of its agent the run runs and gives it to
// the factory on the start and on every resume (T075), so a run keeps the
// version it started on; a negative version is refused.
func TestRunKeepsItsAgentVersion(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	p := w.process()
	chat := w.interrupted(p, "chat", w.model.on("q", 2), 1, runner.AgentVersion(3))
	if got := w.get(chat); got.AgentVersion != 3 {
		t.Errorf("the run's stored agent version is %d, want 3", got.AgentVersion)
	}
	if _, err := p.resume(chat); err != nil {
		t.Fatalf("resume: %v", err)
	}
	want := []runner.Placement{
		{ConversationID: "chat", Interactive: true, AgentVersion: 3},
		{ConversationID: "chat", RunID: chat, Interactive: true, Resume: true, AgentVersion: 3},
	}
	w.mu.Lock()
	got := slices.Clone(w.placements)
	w.mu.Unlock()
	if !slices.Equal(got, want) {
		t.Errorf("placements = %+v, want %+v", got, want)
	}
	if _, err := p.r.Run(w.ctx, "bad", harness.UserText(w.model.on("b", 0)), runner.AgentVersion(-1)); !errors.Is(err, store.ErrInvalid) {
		t.Errorf("Run with agent version -1 = %v, want store.ErrInvalid", err)
	}
}

// WithSlots refuses nil slots.
func TestWithSlotsRefusesNil(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	if _, err := runner.NewRecovery(w.process().r, memory.Admin(w.mem), runner.WithSlots(nil)); !errors.Is(err, runner.ErrInvalidConfig) {
		t.Errorf("WithSlots(nil) = %v, want ErrInvalidConfig", err)
	}
}
