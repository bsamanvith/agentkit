package runner_test

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// A deferred tool that a turn loaded is in the turn's stored record, and a
// run resumed after a crash has it from its first request: the stored
// transcript holds what loaded it. Here write is deferred, and each echo
// call loads it.
func TestDeferredToolKeptOnResume(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.hopts = []harness.Option{
		harness.WithDeferLoading(func(name string) bool { return name == "write" }),
		harness.WithToolLoader("echo", func(json.RawMessage) []string { return []string{"write"} }),
	}
	prompt := w.model.onCalls("d", []string{"echo"}, []string{"write"})
	dead := w.process()
	dead.store.crashAt(crashAtNth("Append", 3, false)) // after its first turn
	x, err := dead.r.Run(w.ctx, "c", harness.UserText(prompt))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dead.drain(x, nil); !errors.Is(err, errCrashed) {
		t.Fatalf("crashed run ended with %v", err)
	}
	turn, err := runner.HarnessTurn(w.log("c")[1])
	if err != nil || !slices.Equal(turn.Loaded, []string{"write"}) {
		t.Fatalf("stored first turn loaded %v (%v), want write", turn.Loaded, err)
	}
	w.expire()
	evs, err := w.process().resume(x.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	lastDone(t, evs)
	reqs := w.model.requestsFor(prompt)
	last := reqs[len(reqs)-1]
	if !slices.ContainsFunc(last.Tools, func(d tool.Definition) bool { return d.Name == "write" }) {
		t.Errorf("the resumed run's request has tools %v, want write loaded", last.Tools)
	}
	if res := toolResults(w, prompt); len(res) != 2 || res[1].IsError {
		t.Errorf("results %+v, want write to have run", res)
	}
}
