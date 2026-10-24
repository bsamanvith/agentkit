package runner_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/runner"
)

// A run's tool choice is stored with its prompt, and a resume after a crash
// keeps it: the toolset it left out stays out.
func TestToolChoiceKeptOnResume(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	w.hopts = []harness.Option{harness.WithToolsets(func(name string) string {
		if name == "echo" {
			return "util"
		}
		return ""
	})}
	prompt := w.model.on("p", 2)
	dead := w.process()
	dead.store.crashAt(crashAtNth("Append", 3, false)) // after its first turn
	x, err := dead.r.Run(w.ctx, "c", harness.UserText(prompt), runner.Tools(harness.ToolChoice{Toolsets: []string{}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dead.drain(x, nil); !errors.Is(err, errCrashed) {
		t.Fatalf("crashed run ended with %v", err)
	}
	w.expire()
	evs, err := w.process().resume(x.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	lastDone(t, evs)
	res := toolResults(w, prompt)
	if len(res) != 2 {
		t.Fatalf("results %+v, want one per turn", res)
	}
	for _, r := range res {
		if !r.IsError || !strings.Contains(r.Content, "not available in this chat") {
			t.Errorf("a tool left out ran: %+v", r)
		}
	}
	rec, err := runner.DecodeRecord(w.log("c")[0])
	if err != nil || rec.Prompt == nil || rec.Prompt.Tools == nil || rec.Prompt.Tools.Toolsets == nil {
		t.Fatalf("prompt record %+v, %v: want the run's choice", rec.Prompt, err)
	}
}

func TestToolChoiceInvalidMode(t *testing.T) {
	t.Parallel()
	w := newWorld(t)
	_, err := w.process().r.Run(w.ctx, "c", harness.UserText("hi"), runner.Tools(harness.ToolChoice{Search: "lazy"}))
	if !errors.Is(err, runner.ErrInvalidConfig) {
		t.Fatalf("err = %v, want ErrInvalidConfig", err)
	}
}
