package agentapi_test

import (
	"context"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/store"
)

// pauseTask starts a task whose first turn calls the gated tool, pauses it
// while the call runs, and waits until it is paused.
func (r *rig) pauseTask(g *gatedTool) agentapi.Run {
	r.t.Helper()
	task := r.task(`{"input":"report on the data"}`)
	g.inFlight(r.t)
	r.taskCommand(task.ID, "pause", http.StatusAccepted)
	close(g.release)
	if run := r.waitEnded(task.ID); run.Status != agentapi.StatusPaused {
		r.t.Fatalf("task = %s, want paused", jsonOf(run))
	}
	return task
}

// A paused task is listed by its status, as the spec's Run.status says.
func TestPausedTasksAreListedByStatus(t *testing.T) {
	t.Parallel()
	g := newGatedTool(t)
	m := script(t, textAndCalls("Fetching.", call("c1", "fetch", `{}`)))
	r := newRig(t, m, rigConfig{tools: g.reg})
	task := r.pauseTask(g)
	list := r.listRuns(url.Values{"status": {agentapi.StatusPaused}})
	if len(list.Runs) != 1 || list.Runs[0].ID != task.ID || list.Runs[0].Status != agentapi.StatusPaused {
		t.Errorf("GET /v1/runs?status=paused = %s, want the paused task", jsonOf(list))
	}
}

// Limits: a paused task counts against the tenant's background runs again
// however it is resumed: POST /v1/runs/{id}/resume and an empty message to
// its conversation answer 429 as POST /v1/tasks/{id}/resume does, and once a
// place is free it resumes.
func TestResumingAPausedTaskCountsAgainstTheLimit(t *testing.T) {
	t.Parallel()
	g := newGatedTool(t)
	m := script(t, textAndCalls("Fetching.", call("c1", "fetch", `{}`)), llmtest.Text("Other done."), llmtest.Text("Done."))
	m.gate = make(chan struct{}, 1)
	m.gate <- struct{}{} // the paused task's first model call
	r := newRig(t, m, rigConfig{tools: g.reg, api: []agentapi.Option{agentapi.WithMaxBackgroundRuns(1)}})
	paused := r.pauseTask(g)
	other := r.task(`{"input":"take the place"}`) // waits for its model call

	for name, req := range map[string][2]string{
		"task resume":   {"/v1/tasks/" + paused.ID + "/resume", ""},
		"run resume":    {"/v1/runs/" + paused.ID + "/resume", ""},
		"empty message": {"/v1/runs", `{"message":"","conversation_id":"` + paused.ConversationID + `"}`},
	} {
		resp, body := r.do(http.MethodPost, req[0], req[1], "Accept", "application/json")
		wantError(t, name+" past the limit", resp, body, http.StatusTooManyRequests, agentapi.CodeTooManyBackgroundRuns)
	}
	if run := r.getRun(paused.ID); run.Status != agentapi.StatusPaused {
		t.Fatalf("after the refused resumes the task is %s, want paused", run.Status)
	}
	m.gate <- struct{}{}
	if run := r.waitTask(other.ID); run.Status != agentapi.StatusDone {
		t.Fatalf("the other task = %s, want done", jsonOf(run))
	}
	m.gate <- struct{}{}
	r.do(http.MethodPost, "/v1/runs/"+paused.ID+"/resume", "", "Accept", "application/json")
	if run := r.waitTask(paused.ID); run.Status != agentapi.StatusDone || run.Answer != "Done." {
		t.Fatalf("resumed with a place free, the task = %s, want done", jsonOf(run))
	}
}

// leaseStore holds the lease of run id for another owner at the first Get
// of it once armed, and lets it go at the second: a task that has just
// paused, whose process lets its lease go a moment after.
type leaseStore struct {
	agentapi.Store
	mu    sync.Mutex
	id    string
	calls int
}

func (s *leaseStore) arm(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.id = id
}

func (s *leaseStore) Get(ctx context.Context, id string) (store.Run, error) {
	s.mu.Lock()
	n := 0
	if s.id != "" && id == s.id {
		s.calls++
		n = s.calls
	}
	s.mu.Unlock()
	switch n {
	case 1:
		if _, err := s.Claim(ctx, id, "pausing", time.Minute); err != nil {
			return store.Run{}, err
		}
	case 2:
		if err := s.Release(ctx, id, "pausing"); err != nil {
			return store.Run{}, err
		}
	}
	return s.Store.Get(ctx, id)
}

// A resume that finds the task paused but its lease not let go yet waits
// for it, then resumes the task: it does not take the lease it saw before
// the wait for one held.
func TestResumeWaitsForThePausedTaskToLetGo(t *testing.T) {
	t.Parallel()
	g := newGatedTool(t)
	m := script(t, textAndCalls("Fetching.", call("c1", "fetch", `{}`)), llmtest.Text("Done."))
	clk := clocktest.NewFake(time.Time{})
	base, tenant := testStore(t, clk)
	st := &leaseStore{Store: base}
	r := newRig(t, m, rigConfig{tools: g.reg, clock: clk, store: st, api: tenant})
	task := r.pauseTask(g)
	st.arm(task.ID)
	r.taskCommand(task.ID, "resume", http.StatusAccepted)
	if run := r.waitTask(task.ID); run.Status != agentapi.StatusDone {
		t.Fatalf("task = %s, want done", jsonOf(run))
	}
}

// A key that may not use every toolset of a task's agent can neither steer
// the task nor resume it (both run the agent's tools): 404, as for no task,
// before anything is sent. It may still pause it, as it may stop it.
func TestSteeringNeedsTheAgentsToolsets(t *testing.T) {
	t.Parallel()
	r, _ := accessRig(t)
	both := r.task(`{"input":"hi","agent":"both"}`, as(keyA)...)
	solo := r.task(`{"input":"hi","agent":"solo"}`, as(keyA)...)
	r.waitTask(both.ID)
	r.waitTask(solo.ID)
	for _, verb := range []string{"messages", "resume"} {
		resp, body := r.do(http.MethodPost, "/v1/tasks/"+both.ID+"/"+verb, `{"text":"use b"}`, asJSON(keyOnlyA)...)
		wantError(t, verb+" to a task of a hidden agent", resp, body, http.StatusNotFound, agentapi.CodeNotFound)
		// The same key reaches a task of an agent it may use (ended: 409).
		resp, body = r.do(http.MethodPost, "/v1/tasks/"+solo.ID+"/"+verb, `{"text":"use a"}`, asJSON(keyOnlyA)...)
		wantError(t, verb+" to an ended task of a visible agent", resp, body, http.StatusConflict, agentapi.CodeRunFinal)
	}
	resp, body := r.do(http.MethodPost, "/v1/tasks/"+both.ID+"/pause", "", asJSON(keyOnlyA)...)
	wantError(t, "pausing an ended task of a hidden agent", resp, body, http.StatusConflict, agentapi.CodeRunFinal)
}
