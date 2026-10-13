package agentapi_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// listRuns reads GET /v1/runs with the query.
func (r *rig) listRuns(q url.Values) agentapi.RunList {
	r.t.Helper()
	var list agentapi.RunList
	if code := r.get("/v1/runs?"+q.Encode(), &list); code != http.StatusOK {
		r.t.Fatalf("GET /v1/runs?%s = %d", q.Encode(), code)
	}
	return list
}

func runIDs(runs []agentapi.Run) []string {
	out := make([]string, len(runs))
	for i, run := range runs {
		out[i] = run.ID
	}
	return out
}

// TestListRuns: GET /v1/runs lists a conversation's runs newest first, a page
// at a time, and filters them by status; a bad query is 400, an unknown
// conversation 404.
func TestListRuns(t *testing.T) {
	t.Parallel()
	m := echo(t)
	r := newRig(t, m, rigConfig{})
	first := r.run("one", "")
	conv := first.conversationID()
	second := r.run("two", conv)
	m.gate = make(chan struct{}, 1)
	third := r.start(t.Context(), "three", conv)
	third.until(agentapi.TypeRunStart, 0)
	if resp, body := r.do(http.MethodPost, "/v1/runs/"+third.runID()+"/stop", ""); resp.StatusCode != http.StatusOK {
		t.Fatalf("stop = %d %s", resp.StatusCode, body)
	}
	third.all()

	all := r.listRuns(url.Values{"conversation_id": {conv}})
	if want := []string{third.runID(), second.runID(), first.runID()}; !slices.Equal(runIDs(all.Runs), want) || all.NextCursor != "" {
		t.Errorf("runs %v (cursor %q), want %v, newest first, in one page", runIDs(all.Runs), all.NextCursor, want)
	}
	if run := all.Runs[0]; run.Status != agentapi.StatusInterrupted || run.Error == nil || run.Error.Code != agentapi.CodeRunInterrupted || run.Agent == "" {
		t.Errorf("listed stopped run %s, want interrupted, with its agent", jsonOf(run))
	}
	var paged []string
	q := url.Values{"conversation_id": {conv}, "limit": {"1"}}
	for range 4 {
		page := r.listRuns(q)
		paged = append(paged, runIDs(page.Runs)...)
		if page.NextCursor == "" {
			break
		}
		q.Set("cursor", page.NextCursor)
	}
	if !slices.Equal(paged, runIDs(all.Runs)) {
		t.Errorf("pages of one %v, want %v", paged, runIDs(all.Runs))
	}
	done := r.listRuns(url.Values{"conversation_id": {conv}, "status": {agentapi.StatusDone}})
	if want := []string{second.runID(), first.runID()}; !slices.Equal(runIDs(done.Runs), want) {
		t.Errorf("done runs %v, want %v", runIDs(done.Runs), want)
	}

	for query, want := range map[string]struct {
		status int
		code   string
	}{
		"":                                      {http.StatusBadRequest, agentapi.CodeInvalidRequest},
		"conversation_id=" + conv + "&status=x": {http.StatusBadRequest, agentapi.CodeInvalidRequest},
		"conversation_id=" + conv + "&limit=0":  {http.StatusBadRequest, agentapi.CodeInvalidRequest},
		"conversation_id=" + conv + "&cursor=x": {http.StatusBadRequest, agentapi.CodeInvalidRequest},
		"conversation_id=conv_nope":             {http.StatusNotFound, agentapi.CodeNotFound},
	} {
		resp, body := r.do(http.MethodGet, "/v1/runs?"+query, "")
		wantError(t, "GET /v1/runs?"+query, resp, body, want.status, want.code)
	}
}

// TestCancelRun: POST /v1/runs/{id}/cancel ends a run for good: its stream
// ends with run_error cancelled, canceling or stopping it again is a no-op,
// and it cannot be resumed (409 run_final, naming the run).
func TestCancelRun(t *testing.T) {
	t.Parallel()
	m := echo(t)
	m.gate = make(chan struct{}, 1)
	r := newRig(t, m, rigConfig{})
	s := r.start(t.Context(), "hi", "")
	s.until(agentapi.TypeRunStart, 0)
	id := s.runID()
	for range 2 {
		var run agentapi.Run
		resp, body := r.do(http.MethodPost, "/v1/runs/"+id+"/cancel", "")
		if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &run) != nil || run.Status != agentapi.StatusCanceled {
			t.Fatalf("cancel = %d %s, want 200 and the run canceled", resp.StatusCode, body)
		}
	}
	if e := s.all(); e[len(e)-1].Error == nil || e[len(e)-1].Error.Code != agentapi.CodeCancelled {
		t.Errorf("stream ended with %+v, want run_error cancelled", e[len(e)-1])
	}
	resp, body := r.do(http.MethodPost, "/v1/runs/"+id+"/resume", "")
	wantError(t, "resume a canceled run", resp, body, http.StatusConflict, agentapi.CodeRunFinal)
	if e := apiError(t, body); e.RunID != id {
		t.Errorf("resume: run_id %q, want %q", e.RunID, id)
	}
	var run agentapi.Run
	if resp, body := r.do(http.MethodPost, "/v1/runs/"+id+"/stop", ""); resp.StatusCode != http.StatusOK || json.Unmarshal(body, &run) != nil || run.Status != agentapi.StatusCanceled {
		t.Errorf("stop a canceled run = %d %s, want 200 and the run as it was", resp.StatusCode, body)
	}
	for _, verb := range []string{"cancel", "stop", "resume"} {
		resp, body := r.do(http.MethodPost, "/v1/runs/run_nope/"+verb, "")
		wantError(t, verb+" an unknown run", resp, body, http.StatusNotFound, agentapi.CodeNotFound)
	}
	if n := len(m.requests()); n > 1 {
		t.Errorf("%d model calls, want at most 1", n)
	}
}

// TestRunLosesItsLease: a run whose lease the store cannot renew in time
// stops, interrupted, and can be resumed.
func TestRunLosesItsLease(t *testing.T) {
	t.Parallel()
	m := echo(t)
	m.gate = make(chan struct{}, 1)
	r := newRig(t, m, rigConfig{})
	s := r.start(t.Context(), "hi", "")
	s.until(agentapi.TypeRunStart, 0)
	if err := r.clock.WaitForTimers(t.Context(), 1); err != nil { // the lease's heartbeat
		t.Fatal(err)
	}
	r.clock.Advance(time.Minute)
	if e := s.all(); e[len(e)-1].Error == nil || e[len(e)-1].Error.Code != agentapi.CodeRunInterrupted {
		t.Errorf("stream ended with %+v, want run_error interrupted", e[len(e)-1])
	}
	if run := r.getRun(s.runID()); run.Status != agentapi.StatusInterrupted {
		t.Errorf("run = %s, want interrupted", run.Status)
	}
	m.gate <- struct{}{}
	resumed := r.open(r.postPath(t.Context(), "/v1/runs/"+s.runID()+"/resume", ""))
	if resumed.all(); resumed.last().Answer != "echo: hi" {
		t.Errorf("resumed stream %s, want the answer", jsonOf(resumed.events))
	}
}

// TestEventsOfAnEvictedRun: once the server has let a finished run's events
// go, GET /v1/runs/{id}/events replays the run from the store, and the run
// reads as it did.
func TestEventsOfAnEvictedRun(t *testing.T) {
	t.Parallel()
	sets := demoTools(t)
	m := script(t, llmtest.ToolCalls(call("c1", "echo", `{"text":"x"}`)), llmtest.Text("Done."), llmtest.Text("Next."))
	r := newRig(t, m, rigConfig{tools: sets["a"], api: []agentapi.Option{agentapi.WithRetention(1, 1, time.Hour)}})
	first := r.run("echo x", "")
	before := r.getRun(first.runID())
	r.run("next", first.conversationID()) // evicts the first run's events
	got := r.events(first.runID(), "")
	if !slices.Equal(types(got), types(first.events)) || got[len(got)-1].Answer != "Done." {
		t.Errorf("replayed %v, want %v, answered", types(got), types(first.events))
	}
	if after := r.getRun(first.runID()); !jsonEqual(after, before) {
		t.Errorf("evicted run reads\n%s\nnot\n%s", jsonOf(after), jsonOf(before))
	}
	if tail := r.events(first.runID(), "2"); len(tail) != len(got)-3 || tail[0].Seq != 3 {
		t.Errorf("from Last-Event-ID 2: %s", jsonOf(tail))
	}
}

// TestDecisions: a flow run that waits at an approval is listed as
// suspended; POST /v1/runs/{id}/decisions approves it, and it goes on to
// done, or rejects it, and it ends rejected. A decision for another step, or
// for a run that is final, is 409.
func TestDecisions(t *testing.T) {
	t.Parallel()
	steps := func(name string) flow.Step {
		return flow.Func(name, func(_ context.Context, st *flow.State) error { return flow.Set(st, name, true) })
	}
	flows := runner.WithFlows(func(context.Context, string) (flow.Step, error) {
		return flow.Sequence("f", steps("draft"), flow.Approve("ok"), steps("send")), nil
	})
	clk := clocktest.NewFake(time.Time{})
	// The flow runs in the default tenant, which the rig's own store
	// selection would replace.
	r := newRig(t, echo(t), rigConfig{clock: clk, store: scoped.Memory(clk), api: []agentapi.Option{agentapi.WithRunnerOptions(flows)}})
	tenant, err := tenancy.NewTenant(agentapi.DefaultTenant)
	if err != nil {
		t.Fatal(err)
	}
	ctx := tenancy.With(t.Context(), tenant)
	suspend := func() string {
		t.Helper()
		conv := r.create(`{}`)
		x, err := r.srv.Runner().RunFlow(ctx, conv.ID, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := x.Wait(); err != nil {
			t.Fatal(err)
		}
		return x.Run.ID
	}
	approved, rejected := suspend(), suspend()
	list := r.listRuns(url.Values{"status": {agentapi.StatusSuspended}})
	if !slices.Contains(runIDs(list.Runs), approved) || !slices.Contains(runIDs(list.Runs), rejected) {
		t.Fatalf("suspended runs %v, want %s and %s", runIDs(list.Runs), approved, rejected)
	}

	decide := func(id string, d agentapi.DecisionRequest) (*http.Response, []byte) {
		b, _ := json.Marshal(d)
		return r.do(http.MethodPost, "/v1/runs/"+id+"/decisions", string(b))
	}
	resp, body := decide(approved, agentapi.DecisionRequest{Path: "f/other", Approved: true})
	wantError(t, "a decision for another step", resp, body, http.StatusConflict, agentapi.CodeNotPending)
	resp, body = r.do(http.MethodPost, "/v1/runs/"+approved+"/resume", "")
	wantError(t, "resume a suspended run", resp, body, http.StatusConflict, agentapi.CodeAwaitingDecision)
	resp, body = r.do(http.MethodPost, "/v1/runs/"+approved+"/decisions", `{"approved":true}`)
	wantError(t, "a decision without a path", resp, body, http.StatusBadRequest, agentapi.CodeInvalidRequest)

	if resp, body := decide(approved, agentapi.DecisionRequest{Path: "f/ok", Approved: true}); resp.StatusCode != http.StatusOK {
		t.Fatalf("approve = %d %s", resp.StatusCode, body)
	}
	if run := r.waitEnded(approved); run.Status != agentapi.StatusDone {
		t.Errorf("approved run %s, want done", jsonOf(run))
	}
	if resp, body := decide(rejected, agentapi.DecisionRequest{Path: "f/ok", Reason: "not now"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("reject = %d %s", resp.StatusCode, body)
	}
	if run := r.waitEnded(rejected); run.Status != agentapi.StatusRejected || run.Error == nil || run.Error.Code != agentapi.CodeRejected {
		t.Errorf("rejected run %s, want rejected", jsonOf(run))
	}
	resp, body = decide(approved, agentapi.DecisionRequest{Path: "f/ok", Approved: true})
	wantError(t, "a decision for a finished run", resp, body, http.StatusConflict, agentapi.CodeRunFinal)
	if list := r.listRuns(url.Values{"status": {agentapi.StatusSuspended}}); len(list.Runs) != 0 {
		t.Errorf("suspended runs after the decisions: %v", runIDs(list.Runs))
	}
}

// holdLog is a logger that holds the first record of a message until the
// test releases it, so a test can act in the moment the server logs it.
type holdLog struct {
	slog.Handler
	msg     string
	reached chan struct{}
	release chan struct{}
	once    sync.Once
}

func newHoldLog(msg string) *holdLog {
	return &holdLog{Handler: slog.DiscardHandler, msg: msg, reached: make(chan struct{}), release: make(chan struct{})}
}

func (h *holdLog) Enabled(context.Context, slog.Level) bool { return true }

func (h *holdLog) Handle(_ context.Context, r slog.Record) error {
	if r.Message == h.msg {
		h.once.Do(func() { close(h.reached); <-h.release })
	}
	return nil
}

// TestSlotFreeBeforeRunDone: a run's slot is free by the time its client
// sees run_done, so a client that starts its next run at once is not
// refused over capacity. The server is held after the run's end, before it
// logs it.
func TestSlotFreeBeforeRunDone(t *testing.T) {
	t.Parallel()
	hold := newHoldLog("run done")
	r := newRig(t, echo(t), rigConfig{api: []agentapi.Option{agentapi.WithMaxConcurrentRuns(1), agentapi.WithLogger(slog.New(hold))}})
	defer close(hold.release)
	first := r.run("one", "")
	<-hold.reached
	if next := r.run("two", first.conversationID()); next.last().Type != agentapi.TypeRunDone {
		t.Errorf("the next run ended %+v, want run_done", next.last())
	}
}

// TestRunViewBeforeItsEnding: between the store's end of a run and its
// execution's last event, GET /v1/runs/{id} reads the run as stored, error
// and all. The server is held as it logs the lost lease, before the ending.
func TestRunViewBeforeItsEnding(t *testing.T) {
	t.Parallel()
	m := echo(t)
	m.gate = make(chan struct{}, 1)
	hold := newHoldLog("run lost its lease")
	r := newRig(t, m, rigConfig{api: []agentapi.Option{agentapi.WithLogger(slog.New(hold))}})
	s := r.start(t.Context(), "hi", "")
	s.until(agentapi.TypeRunStart, 0)
	if err := r.clock.WaitForTimers(t.Context(), 1); err != nil { // the lease's heartbeat
		t.Fatal(err)
	}
	r.clock.Advance(time.Minute)
	<-hold.reached
	run := r.getRun(s.runID())
	close(hold.release)
	if run.Status != agentapi.StatusInterrupted || run.Error == nil || run.Error.Code != agentapi.CodeRunInterrupted {
		t.Errorf("run = %+v, want interrupted, with its error", run)
	}
	s.all()
}
