package agentapi_test

import (
	"context"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// gatedTool is a tool "fetch" whose calls each say when they start and wait
// until the test lets them finish: a tool call in flight.
type gatedTool struct {
	entered chan struct{}
	release chan struct{}
	reg     tool.Registry
}

func newGatedTool(t *testing.T) *gatedTool {
	t.Helper()
	g := &gatedTool{entered: make(chan struct{}, 4), release: make(chan struct{})}
	fetch, err := tool.Func("fetch", "Fetch the data.", func(ctx context.Context, _ struct{}) (string, error) {
		g.entered <- struct{}{}
		select {
		case <-g.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		return "fetched", nil
	}, tool.WithMetadata(tool.Metadata{Kind: tool.KindRead, Risk: tool.RiskLow}))
	if err != nil {
		t.Fatal(err)
	}
	g.reg = registry(t, fetch)
	return g
}

// inFlight waits until a call has started.
func (g *gatedTool) inFlight(t *testing.T) {
	t.Helper()
	select {
	case <-g.entered:
	case <-time.After(30 * time.Second):
		t.Fatal("the tool was never called")
	}
}

// steer posts a steering message to the task and wants 202.
func (r *rig) steer(id, text string) agentapi.TaskMessage {
	r.t.Helper()
	resp, body := r.do(http.MethodPost, "/v1/tasks/"+id+"/messages", string(jsonOf(agentapi.SteerRequest{Text: text})))
	if resp.StatusCode != http.StatusAccepted {
		r.t.Fatalf("POST /v1/tasks/%s/messages = %d %s, want 202", id, resp.StatusCode, body)
	}
	return decodeAs[agentapi.TaskMessage](r.t, body)
}

// taskCommand posts pause or resume to the task, wants status, and returns
// the task.
func (r *rig) taskCommand(id, verb string, status int) agentapi.Run {
	r.t.Helper()
	resp, body := r.do(http.MethodPost, "/v1/tasks/"+id+"/"+verb, "")
	if resp.StatusCode != status {
		r.t.Fatalf("POST /v1/tasks/%s/%s = %d %s, want %d", id, verb, resp.StatusCode, body, status)
	}
	return decodeAs[agentapi.Run](r.t, body)
}

func indexOfType(evs []agentapi.Event, typ string, turn int) int {
	return slices.IndexFunc(evs, func(e agentapi.Event) bool { return e.Type == typ && e.Turn == turn })
}

// Delivery and ordering: two messages sent while a tool call runs reach the
// model together, in order, after that call's result and before the next
// model call (tool_result, turn_done, steered, then turn 2); the
// conversation shows them in place, marked; once the task is done, a
// message is refused.
func TestTaskSteering(t *testing.T) {
	t.Parallel()
	g := newGatedTool(t)
	m := script(t, textAndCalls("Fetching.", call("c1", "fetch", `{}`)), llmtest.Text("Done, for 2026, without the PDF."), llmtest.Text("Hello."))
	r := newRig(t, m, rigConfig{tools: g.reg})
	task := r.task(`{"input":"report on the data"}`)
	w := r.follow(task.ID, "")
	g.inFlight(t)
	first, second := r.steer(task.ID, "focus on 2026 data"), r.steer(task.ID, "skip the PDF")
	if first.RunID != task.ID || first.Seq != 1 || second.Seq != 2 || second.Text != "skip the PDF" {
		t.Errorf("queued %+v and %+v, want seqs 1 and 2 of the task", first, second)
	}
	if n := len(m.requests()); n != 1 {
		t.Fatalf("%d model calls while the tool runs, want 1", n)
	}
	close(g.release)
	if run := r.waitTask(task.ID); run.Status != agentapi.StatusDone || run.Answer != "Done, for 2026, without the PDF." {
		t.Fatalf("task = %s, want done", jsonOf(run))
	}

	evs := w.all()
	result, done1, steered, done2 := indexOfType(evs, agentapi.TypeToolResult, 1), indexOfType(evs, agentapi.TypeTurnDone, 1),
		indexOfType(evs, agentapi.TypeSteered, 0), indexOfType(evs, agentapi.TypeTurnDone, 2)
	if result < 0 || done1 < result || steered < done1 || done2 < steered {
		t.Fatalf("tool_result at %d, turn_done 1 at %d, steered at %d, turn_done 2 at %d: want that order\n%s", result, done1, steered, done2, jsonOf(evs))
	}
	if got := evs[steered].Steering; !slices.Equal(got, []string{"focus on 2026 data", "skip the PDF"}) {
		t.Errorf("steered %q, want both messages in order", got)
	}
	if n := len(slices.DeleteFunc(slices.Clone(evs), func(e agentapi.Event) bool { return e.Type != agentapi.TypeSteered })); n != 1 {
		t.Errorf("%d steered events, want 1", n)
	}
	reqs := m.requests()
	last := reqs[1].Messages[len(reqs[1].Messages)-1]
	if len(last.Content) != 3 || last.Content[1].(llm.Text).Text != "focus on 2026 data" || last.Content[2].(llm.Text).Text != "skip the PDF" {
		t.Errorf("the second model call ends with %s, want the result then both messages", jsonOf(last))
	}
	c := r.getConversation(task.ConversationID)
	var marked []string
	for _, msg := range c.Messages {
		if msg.Steer {
			marked = append(marked, msg.Message.Text())
		}
	}
	if !slices.Equal(marked, []string{"focus on 2026 data", "skip the PDF"}) || len(c.Messages) != 6 || !c.Messages[3].Steer {
		t.Errorf("the conversation holds %s, want the messages marked in place after the results", jsonOf(c.Messages))
	}

	resp, body := r.do(http.MethodPost, "/v1/tasks/"+task.ID+"/messages", `{"text":"too late"}`)
	if e := decodeAs[agentapi.ErrorResponse](t, body); resp.StatusCode != http.StatusConflict || e.Error.Code != agentapi.CodeRunFinal {
		t.Errorf("a message after the end = %d %s, want 409 run_final", resp.StatusCode, body)
	}
	// A chat is no task.
	chat := r.run("hello", "").runID()
	if resp, body := r.do(http.MethodPost, "/v1/tasks/"+chat+"/messages", `{"text":"hi"}`); resp.StatusCode != http.StatusNotFound {
		t.Errorf("steering a chat = %d %s, want 404", resp.StatusCode, body)
	}
}

// Pause and resume, across replicas: steering and a pause sent through
// another server while a tool call runs reach the task at its next
// boundary; the call finishes, the steering is stored, and the task pauses
// (its stream ends with run_error paused). Its process stops; a restart's
// dispatcher leaves it paused; a resume through a third server continues it
// to its end, the model reading the steering first.
func TestTaskPauseAndResume(t *testing.T) {
	t.Parallel()
	g := newGatedTool(t)
	m := script(t, textAndCalls("Fetching.", call("c1", "fetch", `{}`)))
	a, b := replicasWith(t, m, replicaConfig{tools: g.reg})
	task := a.task(`{"input":"report on the data"}`)
	w := a.follow(task.ID, "")
	g.inFlight(t)
	b.steer(task.ID, "skip the PDF")
	if run := b.taskCommand(task.ID, "pause", http.StatusAccepted); run.Status != agentapi.StatusRunning {
		t.Errorf("asked to pause, the task is %s, want running until its boundary", run.Status)
	}
	close(g.release)
	if run := a.waitEnded(task.ID); run.Status != agentapi.StatusPaused {
		t.Fatalf("task = %s, want paused", jsonOf(run))
	}
	evs := w.all()
	end := evs[len(evs)-1]
	if end.Type != agentapi.TypeRunError || end.Error == nil || end.Error.Code != agentapi.CodePaused {
		t.Errorf("the stream ended with %s, want run_error paused", jsonOf(end))
	}
	if i := indexOfType(evs, agentapi.TypeToolResult, 1); i < 0 || evs[i].IsError {
		t.Errorf("the call in flight did not finish: %s", jsonOf(evs))
	}
	if i := indexOfType(evs, agentapi.TypeSteered, 0); i < 0 || i < indexOfType(evs, agentapi.TypeTurnDone, 1) {
		t.Errorf("no steered event after turn 1: %s", jsonOf(evs))
	}
	if n := len(m.requests()); n != 1 {
		t.Errorf("%d model calls before the pause took, want 1", n)
	}
	b.taskCommand(task.ID, "pause", http.StatusOK) // paused already

	// The process stops; another starts, and its dispatcher passes.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := a.srv.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown with the task paused: %v", err)
	}
	a.shared.clock.Advance(time.Hour) // every lease expired
	c := a.replica(t, script(t, llmtest.Text("Done, without the PDF.")))
	acme := mustTenant(t, agentapi.DefaultTenant)
	tctx := tenancy.With(t.Context(), acme)
	stored, err := a.shared.store.Get(tctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := runner.NewRecovery(c.srv.Runner(), tenantQueue{st: a.shared.store, tenant: acme, clk: a.shared.clock},
		runner.WithLanes(stored.Lane), runner.WithExecute(c.srv.RunRecovered))
	if err != nil {
		t.Fatal(err)
	}
	if rep, err := queue.Recover(tctx); err != nil || rep.Resumed != 0 {
		t.Fatalf("the restart's dispatcher = %+v, %v; want nothing resumed", rep, err)
	}
	if run := c.getRun(task.ID); run.Status != agentapi.StatusPaused {
		t.Fatalf("after a restart the task is %s, want paused", run.Status)
	}

	// The model answers at once: the task may be done by the time the
	// resume answers.
	if run := c.taskCommand(task.ID, "resume", http.StatusAccepted); run.Status != agentapi.StatusRunning && run.Status != agentapi.StatusDone {
		t.Errorf("resumed, the task is %s, want running (or done already)", run.Status)
	}
	if run := c.waitTask(task.ID); run.Status != agentapi.StatusDone || run.Answer != "Done, without the PDF." || run.Turns != 2 {
		t.Fatalf("task = %s, want done in 2 turns", jsonOf(run))
	}
	reqs := c.model.requests()
	if len(reqs) != 1 {
		t.Fatalf("%d model calls after the resume, want 1", len(reqs))
	}
	last := reqs[0].Messages[len(reqs[0].Messages)-1]
	if txt, ok := last.Content[len(last.Content)-1].(llm.Text); !ok || txt.Text != "skip the PDF" {
		t.Errorf("the model call after the resume ends with %s, want the steering", jsonOf(last))
	}
	c.taskCommand(task.ID, "resume", http.StatusConflict)
}
