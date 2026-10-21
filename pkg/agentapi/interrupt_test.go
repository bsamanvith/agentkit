package agentapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/policy"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// interruptRig is a rig whose agent is interactive, with interruptTools.
func interruptRig(t *testing.T, m *model, notes *atomic.Int64) *rig {
	t.Helper()
	return newRig(t, m, rigConfig{tools: interruptTools(t, notes), harness: interactive()})
}

// interactive are the options of an interactive harness whose policy holds
// every call of note for an approval.
func interactive() []harness.Option {
	return []harness.Option{
		harness.WithInteraction(harness.Interactive),
		harness.WithToolPolicy(policy.Policy{Name: "agent", Rules: []policy.Rule{
			{Name: "hold-notes", Match: policy.Match{Tools: []string{"note"}}, Action: policy.Approve, Reason: "notes need a yes"},
		}, Default: policy.Allow}, nil),
	}
}

// interruptTools are ask, which asks "which color?" (red or blue), and note,
// a write that counts its calls in notes.
func interruptTools(t *testing.T, notes *atomic.Int64) tool.Registry {
	t.Helper()
	ask, err := tool.Func("ask", "Ask the user.", func(context.Context, struct{}) (string, error) {
		return "", tool.Interrupt("which color?", "red", "blue")
	}, tool.WithMetadata(tool.Metadata{Kind: tool.KindRead, Risk: tool.RiskLow, Interrupts: true}))
	if err != nil {
		t.Fatal(err)
	}
	note, err := tool.Func("note", "Write a note.", func(_ context.Context, in struct {
		S string `json:"s"`
	},
	) (string, error) {
		notes.Add(1)
		return "noted " + in.S, nil
	}, tool.WithMetadata(tool.Metadata{Kind: tool.KindWrite, Risk: tool.RiskLow}))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := tool.NewRegistry([]tool.Tool{ask, note})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

// input posts answers to POST /v1/runs/{id}/input and returns the response
// status and body.
func (r *rig) input(id, body string, header ...string) (*http.Response, []byte) {
	r.t.Helper()
	return r.do(http.MethodPost, "/v1/runs/"+id+"/input", body, header...)
}

// A question and an approval suspend the run; the run shows what it waits
// for, takes the answers one by one (the same answer twice is fine, a
// different one is a conflict), and goes on once all are in: the answer is
// the question's result, the approved call runs with its input.
func TestInputAnswersInterrupts(t *testing.T) {
	t.Parallel()
	m := script(t,
		llmtest.ToolCalls(
			llm.ToolUse{ID: "c1", Name: "ask", Input: json.RawMessage(`{}`)},
			llm.ToolUse{ID: "c2", Name: "note", Input: json.RawMessage(`{"s": "hi"}`)},
		),
		llmtest.Text("done"),
	)
	var notes atomic.Int64
	r := interruptRig(t, m, &notes)

	first := r.run("go", "")
	end := first.last()
	if end.Type != agentapi.TypeRunError || end.Error.Code != agentapi.CodeSuspended || !strings.Contains(end.Error.Message, "/input") {
		t.Fatalf("the run ended %+v, want suspended for input", end)
	}
	waiting := map[string]agentapi.Event{}
	for _, e := range first.events {
		if e.Type == agentapi.TypeInterrupt {
			waiting[e.ToolUseID] = e
		}
	}
	if q := waiting["c1"]; q.Turn != 1 || q.Name != "ask" || q.Interrupt.Kind != "question" || q.Interrupt.Prompt != "which color?" || !slices.Equal(q.Interrupt.Choices, []string{"red", "blue"}) {
		t.Errorf("question event %+v", q)
	}
	if a := waiting["c2"]; a.Turn != 1 || a.Interrupt.Kind != "approval" || a.Interrupt.Prompt != "notes need a yes" || string(a.Interrupt.Input) != `{"s":"hi"}` || a.Interrupt.Policy != "agent" || a.Interrupt.Rule != "hold-notes" {
		t.Errorf("approval event %+v %+v: want the call input", a, *a.Interrupt)
	}
	id := first.runID()
	run := r.getRun(id)
	if run.Status != agentapi.StatusSuspended || len(run.Interrupts) != 2 || run.Error == nil || run.Error.Code != agentapi.CodeSuspended {
		t.Fatalf("run %+v, want suspended on two interrupts", run)
	}

	// A resume is not how it goes on.
	resp, _ := r.do(http.MethodPost, "/v1/runs/"+id+"/resume", "")
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("resume of a suspended run = %d", resp.StatusCode)
	}

	// One answer: still suspended, the answer kept.
	resp, body := r.input(id, `{"answers":[{"id":"c1","text":"red","by":"mallory"}]}`)
	var got agentapi.Run
	if err := json.Unmarshal(body, &got); err != nil || resp.StatusCode != http.StatusOK || got.Status != agentapi.StatusSuspended {
		t.Fatalf("first answer = %d %s", resp.StatusCode, body)
	}
	i := slices.IndexFunc(got.Interrupts, func(in agentapi.Interrupt) bool { return in.ID == "c1" })
	if i < 0 || got.Interrupts[i].Answer == nil || got.Interrupts[i].Answer.Text != "red" || got.Interrupts[i].Answer.By != "" {
		t.Fatalf("interrupts after an answer: %+v (by is the server's to say)", got.Interrupts)
	}
	if resp, body := r.input(id, `{"answers":[{"id":"c1","text":"red"}]}`); resp.StatusCode != http.StatusOK {
		t.Errorf("the same answer again = %d %s", resp.StatusCode, body)
	}
	for name, tc := range map[string]struct {
		body string
		code int
		err  string
	}{
		"a different answer":     {`{"answers":[{"id":"c1","text":"blue"}]}`, http.StatusConflict, agentapi.CodeNotPending},
		"no such interrupt":      {`{"answers":[{"id":"c9","text":"red"}]}`, http.StatusConflict, agentapi.CodeNotPending},
		"not one of the choices": {`{"answers":[{"id":"c2","text":"green"}]}`, http.StatusBadRequest, agentapi.CodeInvalidRequest},
		"no answers":             {`{"answers":[]}`, http.StatusBadRequest, agentapi.CodeInvalidRequest},
		"no id":                  {`{"answers":[{"text":"red"}]}`, http.StatusBadRequest, agentapi.CodeInvalidRequest},
		"unknown field":          {`{"answers":[{"id":"c2","approve":true}]}`, http.StatusBadRequest, agentapi.CodeInvalidRequest},
		"twice in one request":   {`{"answers":[{"id":"c2","approved":true},{"id":"c2","approved":true}]}`, http.StatusBadRequest, agentapi.CodeInvalidRequest},
	} {
		resp, body := r.input(id, tc.body)
		var e agentapi.ErrorResponse
		_ = json.Unmarshal(body, &e)
		if resp.StatusCode != tc.code || e.Error == nil || e.Error.Code != tc.err {
			t.Errorf("%s: %d %s, want %d %s", name, resp.StatusCode, body, tc.code, tc.err)
		}
	}
	if notes.Load() != 0 {
		t.Fatal("the held note ran before its approval")
	}

	// The last answer, following the run's events.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, r.url+"/v1/runs/"+id+"/input", strings.NewReader(`{"answers":[{"id":"c2","approved":true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err = r.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	second := r.open(resp)
	evs := second.all()
	last := evs[len(evs)-1]
	if last.Type != agentapi.TypeRunDone || last.Answer != "done" || last.Turns != 2 {
		t.Fatalf("the answered run ended %+v", last)
	}
	if notes.Load() != 1 {
		t.Fatalf("the approved note ran %d times", notes.Load())
	}
	results := map[string]agentapi.Event{}
	for _, e := range evs {
		if e.Type == agentapi.TypeToolResult {
			results[e.ToolUseID] = e
		}
		if e.Type != agentapi.TypeRunDone && e.Turn == 0 {
			t.Errorf("event %+v has no turn", e)
		}
	}
	if results["c1"].Content != "red" || results["c1"].Turn != 1 || results["c2"].Content != `"noted hi"` {
		t.Errorf("results %+v", results)
	}
	reqs := m.requests()
	if len(reqs) != 2 {
		t.Fatalf("%d model requests, want 2", len(reqs))
	}
	if run := r.getRun(id); run.Status != agentapi.StatusDone || len(run.Interrupts) != 0 || run.Turns != 2 || len(run.Record.Turns) != 2 {
		t.Errorf("run %+v", run)
	}
	resp, _ = r.input(id, `{"answers":[{"id":"c2","approved":true}]}`)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("an answer to a done run = %d", resp.StatusCode)
	}
}

// A denied call never runs; the model is told who denied it and why.
func TestInputDenies(t *testing.T) {
	t.Parallel()
	m := script(t, llmtest.ToolCalls(llm.ToolUse{ID: "c1", Name: "note", Input: json.RawMessage(`{"s":"x"}`)}), llmtest.Text("fine"))
	var notes atomic.Int64
	r := interruptRig(t, m, &notes)
	first := r.run("go", "")
	id := first.runID()
	resp, body := r.input(id, `{"answers":[{"id":"c1","approved":false,"reason":"not now"}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("denial = %d %s", resp.StatusCode, body)
	}
	if run := r.waitEnded(id); run.Status != agentapi.StatusDone {
		t.Fatalf("run %+v", run)
	}
	if notes.Load() != 0 {
		t.Fatal("the denied note ran")
	}
	reqs := m.requests()
	res := reqs[len(reqs)-1].Messages[2].Content[0].(llm.ToolResult)
	if !res.IsError || res.Content != "denied by a person: not now" {
		t.Fatalf("the model was shown %+v", res)
	}
	// The whole stream, from the log, is one run with two turns.
	evs := r.events(id, "")
	if last := evs[len(evs)-1]; last.Type != agentapi.TypeRunDone || last.Turns != 2 {
		t.Fatalf("stored events end %+v", last)
	}
}

// An autonomous run is not offered ask_user-like tools, and a call that
// wants an approval is refused, saying why; the mode is the run request's.
func TestRunRequestInteraction(t *testing.T) {
	t.Parallel()
	m := script(t, llmtest.ToolCalls(llm.ToolUse{ID: "c1", Name: "note", Input: json.RawMessage(`{}`)}), llmtest.Text("ok"))
	var notes atomic.Int64
	r := interruptRig(t, m, &notes)
	s := r.open(r.post(t.Context(), `{"message":"go","interaction":"autonomous"}`, ""))
	evs := s.all()
	if last := evs[len(evs)-1]; last.Type != agentapi.TypeRunDone {
		t.Fatalf("the autonomous run ended %+v", last)
	}
	for _, d := range m.requests()[0].Tools {
		if d.Name == "ask" {
			t.Fatal("an autonomous run was offered ask")
		}
	}
	for _, e := range evs {
		if e.Type == agentapi.TypeToolResult && (!e.IsError || !strings.Contains(e.Content, "autonomous")) {
			t.Errorf("held call's result %+v", e)
		}
		if e.Type == agentapi.TypeInterrupt {
			t.Errorf("an autonomous run was interrupted: %+v", e)
		}
	}
	if notes.Load() != 0 {
		t.Fatal("the held note ran")
	}
	for name, body := range map[string]string{
		"unknown mode": `{"message":"go","interaction":"chatty"}`,
		"on a resume":  `{"message":"","conversation_id":"` + s.conversationID() + `","interaction":"autonomous"}`,
	} {
		code, e := errorOf(t, r.post(t.Context(), body, ""))
		if code != http.StatusBadRequest || e.Code != agentapi.CodeInvalidRequest {
			t.Errorf("%s: %d %+v, want 400 invalid_request", name, code, e)
		}
	}
}
