package runner_test

import (
	"bufio"
	"context"
	"fmt"
	"iter"
	"os/exec"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store/memory"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// shout is a model that answers with the user's last message in capitals.
type shout struct{}

func (shout) Generate(_ context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	text := strings.ToUpper(req.Messages[len(req.Messages)-1].Text())
	return func(yield func(llm.Event, error) bool) {
		_ = yield(llm.Event{Kind: llm.EventBlockDone, Block: llm.Text{Text: text}}, nil) &&
			yield(llm.Event{Kind: llm.EventMessageDone, Response: &llm.Response{
				Message: llm.Assistant(llm.Text{Text: text}), Stop: llm.StopReason{Kind: llm.StopEndTurn},
			}}, nil)
	}
}

func Example() {
	s := memory.New()
	tools, _ := tool.NewRegistry(nil)
	agents := func(context.Context, runner.Placement) (runner.Agent, error) {
		return harness.New(shout{}, tools)
	}
	r, err := runner.New(s, agents)
	if err != nil {
		panic(err)
	}
	acme, _ := tenancy.NewTenant("acme")
	ctx := tenancy.With(context.Background(), acme)

	for _, msg := range []string{"hi", "again"} {
		x, err := r.Run(ctx, "conversation-1", harness.UserText(msg))
		if err != nil {
			panic(err)
		}
		for ev, err := range x.Events() {
			if err != nil {
				panic(err)
			}
			switch {
			case ev.Kind == runner.EventHarness && ev.Seq > 0:
				fmt.Printf("entry %d: %s\n", ev.Seq, ev.Harness.Kind)
			case ev.Kind == runner.EventDone:
				fmt.Println(ev.Run.Status, ev.Result.Final.Text())
			}
		}
	}
	tr, seq, _ := r.Context(ctx, "conversation-1")
	fmt.Println(len(tr), "messages up to entry", seq)
	// Output:
	// entry 1: run_start
	// entry 2: turn_done
	// completed HI
	// entry 3: run_start
	// entry 4: turn_done
	// completed AGAIN
	// 4 messages up to entry 4
}

// The runner is handed its store; it never picks a backend itself, so it can
// never reach a backend's cross-tenant view (store.Admin) on its own.
func TestRunnerDoesNotImportABackend(t *testing.T) {
	t.Parallel()
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		if p := strings.TrimPrefix(sc.Text(), "github.com/bsamanvith/agentkit/"); strings.HasPrefix(p, "pkg/store/") {
			t.Errorf("the runner depends on %s", sc.Text())
		}
	}
}
