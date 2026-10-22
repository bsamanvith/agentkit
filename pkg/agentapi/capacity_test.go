package agentapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/runner/pool"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// tenantQueue stands in for the dispatcher's store.Admin over one tenant:
// the agent API's tests never hold a store backend, the only way to a real
// Admin (archtest). It claims the resumable runs of the lanes, oldest first,
// as ClaimNext does when they share a priority.
type tenantQueue struct {
	st     agentapi.Store
	tenant tenancy.Tenant
	clk    *clocktest.Fake
}

func (q tenantQueue) ListResumableAll(context.Context, time.Time, int) ([]store.TenantRun, error) {
	return nil, store.ErrInvalid // the dispatcher only claims
}

func (q tenantQueue) ClaimNotification(context.Context, string, time.Duration) (store.TenantNotification, error) {
	return store.TenantNotification{}, store.ErrInvalid // no webhooks here
}

func (q tenantQueue) RecordDelivery(context.Context, tenancy.Tenant, string, string, store.Delivery) (store.Notification, error) {
	return store.Notification{}, store.ErrInvalid
}

func (q tenantQueue) ClaimNext(ctx context.Context, lanes []string, owner string, ttl time.Duration) (store.TenantRun, error) {
	ctx = tenancy.With(ctx, q.tenant)
	runs, err := q.st.ListResumable(ctx, q.clk.Now(), 0)
	if err != nil {
		return store.TenantRun{}, err
	}
	for _, r := range runs {
		if !slices.Contains(lanes, r.Lane) {
			continue
		}
		if _, err := q.st.Claim(ctx, r.ID, owner, ttl); err != nil {
			continue
		}
		claimed, err := q.st.Get(ctx, r.ID)
		if err != nil {
			return store.TenantRun{}, err
		}
		return store.TenantRun{Tenant: q.tenant, Run: claimed}, nil
	}
	return store.TenantRun{}, store.ErrNotFound
}

// poolsRig is a server with a chat pool and a background pool of one slot
// each, chats routed to the first and everything else to the second; the
// agent "worker" answers when the test lets its model go, "talk" at once.
type poolsRig struct {
	*rig
	work  *model
	mem   agentapi.Store
	ctx   context.Context // the rig's tenant
	queue *runner.Recovery
	rt    *pool.Router
}

func newPoolsRig(t *testing.T, api ...agentapi.Option) *poolsRig {
	t.Helper()
	work, talk := echo(t), echo(t)
	work.gate = make(chan struct{}, 8)
	rt, err := pool.NewRouter([]pool.Pool{pool.NewProcess("chat"), pool.NewProcess("background")},
		[]pool.Rule{{Pool: "chat", When: pool.Chats()}}, "background",
		pool.WithLimit("chat", 1), pool.WithLimit("background", 1))
	if err != nil {
		t.Fatal(err)
	}
	clk := clocktest.NewFake(time.Time{})
	mem := scoped.Memory(clk)
	tools := registry(t)
	r := newRig(t, talk, rigConfig{
		clock: clk, store: mem,
		agents: []agentapi.Agent{
			{Name: "worker", Harness: agentHarness(t, work, tools, harness.WithClock(clk))},
			{Name: "talk", Harness: agentHarness(t, talk, tools, harness.WithClock(clk))},
		},
		api: append([]agentapi.Option{agentapi.WithRouter(rt), agentapi.WithTenant(func(*http.Request) string { return "acme" })}, api...),
	})
	acme, err := tenancy.NewTenant("acme")
	if err != nil {
		t.Fatal(err)
	}
	queue, err := r.srv.Dispatcher(tenantQueue{st: mem, tenant: acme, clk: clk}, "background")
	if err != nil {
		t.Fatal(err)
	}
	return &poolsRig{rig: r, work: work, mem: mem, ctx: tenancy.With(t.Context(), acme), queue: queue, rt: rt}
}

// enqueue queues a background run of the worker on a new conversation.
func (p *poolsRig) enqueue(prompt string) store.Run {
	p.t.Helper()
	conv := p.create(`{"agent":"worker"}`)
	run, err := p.srv.Enqueue(p.ctx, conv.ID, harness.UserText(prompt))
	if err != nil {
		p.t.Fatalf("Enqueue: %v", err)
	}
	return run
}

func (p *poolsRig) stored(id string) store.Run {
	p.t.Helper()
	run, err := p.mem.Get(p.ctx, id)
	if err != nil {
		p.t.Fatal(err)
	}
	return run
}

// eventually waits for cond, for up to ten seconds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !cond(); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
	}
}

// T105: a background pool that is full never delays a chat. The worker's
// two queued runs: the background pool's dispatcher starts one, which takes
// the pool's one slot, and the other waits in the store, unclaimed. A chat
// meanwhile starts and finishes in the chat pool; the waiting run starts
// only when the first ends.
func TestFullBackgroundPoolNeverDelaysAChat(t *testing.T) {
	t.Parallel()
	p := newPoolsRig(t)
	first, second := p.enqueue("job 1"), p.enqueue("job 2")
	passed := make(chan runner.Report, 1)
	go func() {
		rep, err := p.queue.Recover(p.ctx)
		if err != nil {
			t.Error(err)
		}
		passed <- rep
	}()
	eventually(t, "the first job at its model", func() bool { return len(p.work.requests()) == 1 })
	if got := p.stored(second.ID); got.Lease != (store.Lease{}) || got.Status != store.StatusRunning {
		t.Fatalf("with the background pool full, the second job is %+v; want it waiting in the store, unclaimed", got)
	}

	chat := p.startAgent(t.Context(), "hello", "", "talk").all()
	if end := chat[len(chat)-1]; end.Type != agentapi.TypeRunDone || end.Answer != "echo: hello" {
		t.Fatalf("the chat ended with %+v while the background pool was full, want it done", end)
	}
	if run := p.getRun(chat[0].RunID); run.Pool != "chat" {
		t.Errorf("the chat ran in pool %q, want chat", run.Pool)
	}
	if got := p.stored(second.ID); got.Lease != (store.Lease{}) || len(p.work.requests()) != 1 {
		t.Errorf("the chat let the second job start (%+v, %d model requests)", got, len(p.work.requests()))
	}

	p.work.gate <- struct{}{}
	p.work.gate <- struct{}{}
	if rep := <-passed; rep.Resumed != 2 {
		t.Fatalf("the dispatcher's pass = %+v, want both jobs run", rep)
	}
	for _, run := range []store.Run{first, second} {
		if got := p.stored(run.ID); got.Status != store.StatusCompleted || got.Pool != "background" || got.Lane != "background" {
			t.Errorf("job %s = %+v, want completed in the background pool and its lane", run.ID, got)
		}
	}
}

// T105: a chat whose pool is full is refused at the request, 429 with
// Retry-After, whether it starts a run or resumes one; it leaves nothing
// behind, and the server's other slots are not taken.
func TestFullChatPoolRefusesAChat(t *testing.T) {
	t.Parallel()
	p := newPoolsRig(t)
	p.work.gate <- struct{}{} // the worker's first answer
	conv := p.startAgent(t.Context(), "first", "", "worker").all()[0].ConversationID
	held := p.startAgent(t.Context(), "hold on", conv, "worker")
	held.until(agentapi.TypeRunStart, 0)

	resp, body := p.do("POST", "/v1/runs", jsonRun("hello", "", "talk"), "Accept", "application/json")
	wantError(t, "a chat with its pool full", resp, body, http.StatusTooManyRequests, agentapi.CodeOverCapacity)
	if resp.Header.Get("Retry-After") == "" {
		t.Error("the 429 has no Retry-After")
	}
	page, err := p.mem.ListConversations(p.ctx, "talk", store.Page{})
	if err != nil || len(page.Conversations) != 0 {
		t.Errorf("the refused chat left conversations %+v (%v), want none", page.Conversations, err)
	}

	p.work.gate <- struct{}{}
	held.all()
	// The slot is back: the next chat runs.
	if end := p.startAgent(t.Context(), "hello", "", "talk").all(); end[len(end)-1].Type != agentapi.TypeRunDone {
		t.Errorf("a chat once the pool has a slot again ended with %+v", end[len(end)-1])
	}
}

// T105: a run's pool is stored with it and GET /v1/runs/{id} and the list
// return it: a chat's, and a queued background run's.
func TestRunPoolIsStoredAndReturned(t *testing.T) {
	t.Parallel()
	p := newPoolsRig(t)
	chat := p.startAgent(t.Context(), "hello", "", "talk").all()
	id := chat[0].RunID
	if got := p.stored(id); got.Pool != "chat" {
		t.Errorf("the chat's stored pool is %q, want chat", got.Pool)
	}
	if got := p.getRun(id); got.Pool != "chat" {
		t.Errorf("GET /v1/runs/%s gives pool %q, want chat", id, got.Pool)
	}
	var list agentapi.RunList
	if code := p.get(fmt.Sprintf("/v1/runs?conversation_id=%s", chat[0].ConversationID), &list); code != http.StatusOK || len(list.Runs) != 1 || list.Runs[0].Pool != "chat" {
		t.Errorf("GET /v1/runs = %d %+v, want the run in pool chat", code, list)
	}
	queued := p.enqueue("job")
	if queued.Pool != "background" || queued.Lane != "background" {
		t.Errorf("a queued run's pool %q, lane %q; want background, background", queued.Pool, queued.Lane)
	}
	if got := p.getRun(queued.ID); got.Pool != "background" {
		t.Errorf("GET /v1/runs/%s gives pool %q, want background", queued.ID, got.Pool)
	}
}

// T105: a flow run is in no pool, so resuming one never waits on, or is
// refused by, the pool its conversation's agent would be routed to: an
// interrupted flow run resumes while the background pool is full.
func TestResumedFlowRunTakesNoPoolSlot(t *testing.T) {
	t.Parallel()
	var first atomic.Bool
	wait := flow.Func("wait", func(ctx context.Context, st *flow.State) error {
		if first.CompareAndSwap(false, true) {
			<-ctx.Done()
			return ctx.Err()
		}
		return flow.Set(st, "wait", true)
	})
	p := newPoolsRig(t, agentapi.WithRunnerOptions(runner.WithFlows(func(context.Context, string) (flow.Step, error) {
		return flow.Sequence("f", wait), nil
	})))
	conv := p.create(`{"agent":"worker"}`)
	x, err := p.srv.Runner().RunFlow(p.ctx, conv.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	stopped := make(chan struct{})
	go func() { defer close(stopped); _, _ = x.Wait() }()
	eventually(t, "the flow at its step", first.Load)
	if _, err := p.srv.Runner().Interrupt(p.ctx, x.Run.ID); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	<-stopped

	// The background pool full, as a background run holds its slot.
	bg := p.rt.Slots("background")
	if !bg.TryAcquire() {
		t.Fatal("the background pool is full already")
	}
	defer bg.Release()

	resp, body := p.do("POST", "/v1/runs/"+x.Run.ID+"/resume", "", "Accept", "application/json")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resuming the flow run with the background pool full = %d %s, want 200", resp.StatusCode, body)
	}
	eventually(t, "the flow run completed", func() bool { return p.stored(x.Run.ID).Status == store.StatusCompleted })
	if n := bg.InUse(); n != 1 {
		t.Errorf("the background pool has %d slots taken after the flow run, want 1 (the test's)", n)
	}
}

// T105: an answer that lets a suspended chat go on takes a slot of the
// chat's pool, as a resume does: with the pool full it is 429 and the run
// keeps waiting for its answer; with a slot free it goes on, and gives the
// slot back when it ends.
func TestAnsweredRunTakesItsPoolsSlot(t *testing.T) {
	t.Parallel()
	m := script(t, llmtest.ToolCalls(llm.ToolUse{ID: "c1", Name: "ask", Input: json.RawMessage(`{}`)}), llmtest.Text("done"))
	rt, err := pool.NewRouter([]pool.Pool{pool.NewProcess("chat"), pool.NewProcess("background")},
		[]pool.Rule{{Pool: "chat", When: pool.Chats()}}, "background", pool.WithLimit("chat", 1))
	if err != nil {
		t.Fatal(err)
	}
	var notes atomic.Int64
	r := newRig(t, m, rigConfig{tools: interruptTools(t, &notes), harness: interactive(), api: []agentapi.Option{agentapi.WithRouter(rt)}})
	first := r.run("go", "")
	if end := first.last(); end.Error == nil || end.Error.Code != agentapi.CodeSuspended {
		t.Fatalf("the run ended %+v, want suspended for input", end)
	}
	id := first.runID()
	chat := rt.Slots("chat")
	eventually(t, "the suspended run's slot back", func() bool { return chat.InUse() == 0 })

	if !chat.TryAcquire() {
		t.Fatal("the chat pool is full")
	}
	answer := `{"answers":[{"id":"c1","text":"red"}]}`
	resp, body := r.input(id, answer, "Accept", "application/json")
	wantError(t, "an answer with the chat pool full", resp, body, http.StatusTooManyRequests, agentapi.CodeOverCapacity)
	if run := r.getRun(id); run.Status != agentapi.StatusSuspended {
		t.Errorf("after the refused answer the run is %s, want suspended", run.Status)
	}
	chat.Release()

	resp, body = r.input(id, answer, "Accept", "application/json")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the answer with a slot free = %d %s", resp.StatusCode, body)
	}
	eventually(t, "the run done and its slot back", func() bool {
		return r.getRun(id).Status == agentapi.StatusDone && chat.InUse() == 0
	})
}
