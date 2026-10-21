package runner_test

import (
	"testing"

	"github.com/bsamanvith/agentkit/pkg/runner"
)

// The factory learns where each chat run comes from: interactive or
// background, started (no run yet) or resumed (with its ID), by a user or by
// the recovery loop. Pools route on it (T104).
func TestFactoryGetsPlacement(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	p := w.process()

	chat := w.interrupted(p, "chat", w.model.on("q", 2), 1)
	if _, err := p.resume(chat); err != nil {
		t.Fatalf("continuing the chat: %v", err)
	}
	if _, err := p.run("job", w.model.on("j", 1)); err != nil {
		t.Fatalf("background run: %v", err)
	}
	recovered := w.crashRun(w.ctx, "crash", w.model.on("r", 2), killPoints[1])
	w.expire()
	if rep, err := w.recovery(w.process()).Recover(w.ctx); err != nil || rep.Resumed != 1 {
		t.Fatalf("Recover = %+v, %v; want the crashed run resumed", rep, err)
	}

	want := []runner.Placement{
		{ConversationID: "chat", Interactive: true},
		{ConversationID: "chat", RunID: chat, Interactive: true, Resume: true},
		{ConversationID: "job"},
		{ConversationID: "crash"},
		{ConversationID: "crash", RunID: recovered, Resume: true},
	}
	w.mu.Lock()
	got := w.placements
	w.mu.Unlock()
	if len(got) != len(want) {
		t.Fatalf("placements = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("placement %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
