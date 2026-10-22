package agentapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/runner/pool"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// task posts POST /v1/tasks with body, wants 202, and returns the run.
func (r *rig) task(body string, header ...string) agentapi.Run {
	r.t.Helper()
	resp, b := r.do(http.MethodPost, "/v1/tasks", body, header...)
	var run agentapi.Run
	if resp.StatusCode != http.StatusAccepted || json.Unmarshal(b, &run) != nil {
		r.t.Fatalf("POST /v1/tasks %s = %d %s, want 202 and the run", body, resp.StatusCode, b)
	}
	if run.Status != agentapi.StatusRunning || !run.Background || resp.Header.Get("Location") != "/v1/runs/"+run.ID ||
		resp.Header.Get("Agentkit-Run-Id") != run.ID || resp.Header.Get("Agentkit-Conversation-Id") != run.ConversationID {
		r.t.Errorf("POST /v1/tasks = %s (Location %q), want a running background run and its headers", b, resp.Header.Get("Location"))
	}
	return run
}

// waitTask waits until the task has ended, its last event included (the
// store says so a moment before), and returns it.
func (r *rig) waitTask(id string) agentapi.Run {
	r.t.Helper()
	var run agentapi.Run
	testkit.Eventually(r.t, 30*time.Second, func() bool {
		run = r.getRun(id)
		return run.Status != agentapi.StatusRunning && run.Record != nil && !run.Record.FinishedAt.IsZero()
	})
	return run
}

// checkWhole fails unless evs are the run's events in order, every one once:
// IDs rising, and the stored events those of the run's log once it ended. A
// watcher that joined after a turn's text has the text in its turn_done
// instead of text_delta events.
func checkWhole(t *testing.T, who string, evs, full []agentapi.Event) {
	t.Helper()
	for i := 1; i < len(evs); i++ {
		if wire.CompareIDs(evs[i-1].ID, evs[i].ID) >= 0 {
			t.Errorf("%s: event %s after %s: out of order, or twice", who, evs[i].ID, evs[i-1].ID)
		}
	}
	if got, want := storedOnly(evs), storedOnly(full); !jsonEqual(got, want) {
		t.Errorf("%s saw\n%s\nwant the run's events\n%s", who, jsonOf(got), jsonOf(want))
	}
}

func storedOnly(evs []agentapi.Event) []agentapi.Event {
	return slices.DeleteFunc(slices.Clone(evs), func(e agentapi.Event) bool { return e.Type == agentapi.TypeTextDelta })
}

// Detached: a task goes on while nobody watches it (no reconnect grace) and
// finishes; two watchers attached at different times, one of which leaves
// mid-stream and comes back with Last-Event-ID, each see every event once,
// in order.
func TestTaskRunsDetached(t *testing.T) {
	t.Parallel()
	sets := demoTools(t)
	m := script(t, textAndCalls("Checking.", call("c1", "echo", `{"text":"one"}`)), llmtest.Text("Done: one."))
	m.gate = make(chan struct{})
	r, armed := graceRig(t, m, rigConfig{tools: sets["a"], api: []agentapi.Option{agentapi.WithReconnectGrace(0)}})
	task := r.task(`{"input":"echo one"}`)

	ctxA, leave := context.WithCancel(t.Context())
	a := r.followCtx(ctxA, task.ID, "")
	a.until(agentapi.TypeRunStart, 0)
	m.gate <- struct{}{} // turn 1
	mid := a.until(agentapi.TypeTurnDone, 1)
	seen := slices.Clone(a.events)
	a.abandon(leave) // nobody watches now

	b := r.follow(task.ID, "") // from the start, after A left
	b.until(agentapi.TypeTurnDone, 1)
	back := r.follow(task.ID, mid.ID) // A again, from where it was
	m.gate <- struct{}{}              // turn 2, the answer
	rest, whole := back.all(), b.all()

	if run := r.waitTask(task.ID); run.Status != agentapi.StatusDone || run.Answer != "Done: one." || !run.Background {
		t.Fatalf("task = %s, want done with the answer, background", jsonOf(run))
	}
	full := r.events(task.ID, "")
	checkWhole(t, "watcher A, with its resume", append(seen, rest...), full)
	checkWhole(t, "watcher B", whole, full)
	select {
	case id := <-armed:
		t.Errorf("a reconnect grace started for %s: a task has none", id)
	default:
	}
}

// Detached, with nobody ever watching: the task finishes.
func TestTaskFinishesUnwatched(t *testing.T) {
	t.Parallel()
	r := newRig(t, echo(t), rigConfig{})
	task := r.task(`{"input":"hi"}`)
	if run := r.waitTask(task.ID); run.Status != agentapi.StatusDone || run.Answer != "echo: hi" {
		t.Errorf("task = %s, want done", jsonOf(run))
	}
	c := r.getConversation(task.ConversationID)
	if len(c.Messages) != 2 || c.Messages[0].Message.Text() != "hi" {
		t.Errorf("the task's conversation = %s, want its input and answer", jsonOf(c))
	}
}

// Restart: the process running a task stops mid-run; a watcher waiting on
// another server, and one that comes later with Last-Event-ID, go on with
// the run once a third server takes it up (what the recovery loop does),
// with no gap or duplicate, to its end.
func TestTaskWatchedAcrossARestart(t *testing.T) {
	t.Parallel()
	sets := demoTools(t)
	m := script(t, textAndCalls("Checking.", call("c1", "echo", `{"text":"one"}`)), llmtest.Text("never sent"))
	m.gate = make(chan struct{})
	a, b := replicasWith(t, m, replicaConfig{tools: sets["a"]})
	task := a.task(`{"input":"echo one"}`)
	early := b.follow(task.ID, "")
	// A watcher may miss the text_delta events of a turn the new process
	// streams before the watcher follows it (the turn_done has the text):
	// checkWhole checks these two, not the rig's text-exact check.
	early.done = true
	early.until(agentapi.TypeRunStart, 0)
	m.gate <- struct{}{} // turn 1; the answer waits for ever
	mid := early.until(agentapi.TypeTurnDone, 1)

	// a stops: the task stays running, held by nobody.
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := a.srv.Shutdown(ctx); !errors.Is(err, agentapi.ErrRunsCutOff) {
		t.Fatalf("a's shutdown: %v, want the task cut off", err)
	}
	if run := b.getRun(task.ID); run.Status != agentapi.StatusRunning {
		t.Fatalf("after the stop the task is %s, want running, to be resumed", run.Status)
	}
	late := b.follow(task.ID, mid.ID) // waits for the run to be taken up
	late.done = true

	c := a.replica(t, script(t, llmtest.Text("Done: one.")))
	c.models = nil // its request starts the conversation's turn 2, which no stream of c's announced
	tctx := tenancy.With(t.Context(), mustTenant(t, agentapi.DefaultTenant))
	x, err := c.srv.Runner().Resume(tctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	c.srv.RunRecovered(tctx, x)

	whole, tail := early.all(), late.all()
	full := b.events(task.ID, "")
	if end := full[len(full)-1]; end.Type != agentapi.TypeRunDone || end.Answer != "Done: one." {
		t.Fatalf("the task ended with %s, want run_done", jsonOf(end))
	}
	checkWhole(t, "the early watcher", whole, full)
	checkWhole(t, "the late watcher", append(slices.Clone(full[:indexOf(full, mid.ID)+1]), tail...), full)
}

func mustTenant(t *testing.T, id string) tenancy.Tenant {
	t.Helper()
	tn, err := tenancy.NewTenant(id)
	if err != nil {
		t.Fatal(err)
	}
	return tn
}

// Cancel: a task is canceled (its watcher's stream ends with run_error
// cancelled), canceling it again is a no-op, and it cannot be resumed. A
// chat run a client leaves is interrupted, not canceled: TestClientDisconnect.
func TestTaskCancel(t *testing.T) {
	t.Parallel()
	m := echo(t)
	m.gate = make(chan struct{})
	r := newRig(t, m, rigConfig{})
	task := r.task(`{"input":"hi"}`)
	w := r.follow(task.ID, "")
	w.until(agentapi.TypeRunStart, 0)
	for i := range 2 {
		resp, body := r.do(http.MethodPost, "/v1/runs/"+task.ID+"/cancel", "")
		var run agentapi.Run
		if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &run) != nil || run.Status != agentapi.StatusCanceled {
			t.Fatalf("cancel %d = %d %s, want 200 and the run canceled", i+1, resp.StatusCode, body)
		}
	}
	if end := w.all()[len(w.events)-1]; end.Type != agentapi.TypeRunError || end.Error.Code != agentapi.CodeCancelled {
		t.Errorf("the watcher's stream ended with %s, want run_error cancelled", jsonOf(end))
	}
	resp, body := r.do(http.MethodPost, "/v1/runs/"+task.ID+"/resume", "")
	wantError(t, "resume of a canceled task", resp, body, http.StatusConflict, agentapi.CodeRunFinal)
}

// Limits: a tenant gets 429 too_many_background_runs, with Retry-After, past
// its background runs going on, even when many ask at once; another tenant
// does not; and a place frees up when a task ends.
func TestTaskLimit(t *testing.T) {
	t.Parallel()
	const limit = 3
	m := echo(t)
	m.gate = make(chan struct{})
	r := newRig(t, m, rigConfig{api: []agentapi.Option{agentapi.WithMaxBackgroundRuns(limit)}})

	var mu sync.Mutex
	codes := map[int]int{}
	var wg sync.WaitGroup
	for i := range 3 * limit {
		wg.Go(func() {
			resp, body := r.do(http.MethodPost, "/v1/tasks", fmt.Sprintf(`{"input":"task %d"}`, i))
			if resp.StatusCode == http.StatusTooManyRequests {
				if apiCode(body) != agentapi.CodeTooManyBackgroundRuns || resp.Header.Get("Retry-After") == "" {
					t.Errorf("429 = %s (Retry-After %q), want too_many_background_runs with Retry-After", body, resp.Header.Get("Retry-After"))
				}
			}
			mu.Lock()
			codes[resp.StatusCode]++
			mu.Unlock()
		})
	}
	wg.Wait()
	if codes[http.StatusAccepted] != limit || codes[http.StatusTooManyRequests] != 2*limit {
		t.Fatalf("%d tasks at once with a limit of %d: %v, want %d accepted and the rest 429", 3*limit, limit, codes, limit)
	}
	running := func() int {
		return len(r.listRuns(url.Values{"background": {"true"}, "status": {agentapi.StatusRunning}}).Runs)
	}
	if n := running(); n != limit {
		t.Fatalf("%d background runs going on, want %d", n, limit)
	}
	m.gate <- struct{}{} // one of them ends
	testkit.Eventually(t, 30*time.Second, func() bool { return running() == limit-1 })
	r.task(`{"input":"in the freed place"}`)
	resp, body := r.do(http.MethodPost, "/v1/tasks", `{"input":"over again"}`)
	wantError(t, "a task past the limit again", resp, body, http.StatusTooManyRequests, agentapi.CodeTooManyBackgroundRuns)
	other := r.task(`{"input":"another tenant's"}`, "Test-Tenant", "b")
	for range limit + 1 { // the rest, and the other tenant's
		m.gate <- struct{}{}
	}
	testkit.Eventually(t, 30*time.Second, func() bool { return running() == 0 })
	testkit.Eventually(t, 30*time.Second, func() bool {
		_, b := r.do(http.MethodGet, "/v1/runs/"+other.ID, "", "Test-Tenant", "b")
		var run agentapi.Run
		return json.Unmarshal(b, &run) == nil && run.Status == agentapi.StatusDone
	})
}

// racingStore holds the first count of a tenant's background runs, once
// read, until a second count has read too, or for a while: a server that lets two requests
// count at once lets both read the same count.
type racingStore struct {
	agentapi.Store
	mu            sync.Mutex
	counts        int
	first, second chan struct{}
}

func (s *racingStore) ListRuns(ctx context.Context, f store.RunFilter, p store.Page) (store.RunPage, error) {
	page, err := s.Store.ListRuns(ctx, f, p)
	if f.Kind == store.BackgroundRuns && p.Cursor == "" {
		s.mu.Lock()
		s.counts++
		n := s.counts
		s.mu.Unlock()
		switch n {
		case 1:
			close(s.first)
			// Waiting for what must not happen: a correct server only
			// waits here, a broken one may get past it.
			t := time.NewTimer(500 * time.Millisecond)
			select {
			case <-s.second:
			case <-t.C:
			}
			t.Stop()
		case 2:
			close(s.second)
		}
	}
	return page, err
}

// Limits: counting a tenant's tasks and starting one are one step. While
// one request counts, another cannot count too, so of two requests for the
// last place one gets it and the other 429.
func TestTaskLimitCountAndStartAreOneStep(t *testing.T) {
	t.Parallel()
	m := echo(t)
	m.gate = make(chan struct{})
	clk := clocktest.NewFake(time.Time{})
	base, tenant := testStore(t, clk)
	st := &racingStore{Store: base, first: make(chan struct{}), second: make(chan struct{})}
	r := newRig(t, m, rigConfig{clock: clk, store: st, api: append(tenant, agentapi.WithMaxBackgroundRuns(1))})
	codes := make(chan int, 2)
	post := func(input string) {
		resp, _ := r.do(http.MethodPost, "/v1/tasks", `{"input":"`+input+`"}`)
		codes <- resp.StatusCode
	}
	go post("one")
	<-st.first // the first request is counting
	go post("two")
	got := map[int]int{<-codes: 1}
	got[<-codes]++
	if got[http.StatusAccepted] != 1 || got[http.StatusTooManyRequests] != 1 {
		t.Fatalf("two tasks for the last place = %v, want one 202 and one 429", got)
	}
	m.gate <- struct{}{}
}

// A task runs in the pool that takes background runs (T105), holding one of
// its slots until it ends, and waits in that pool's lane, whose dispatcher
// is the one that resumes it after a crash.
func TestTaskTakesAPoolSlot(t *testing.T) {
	t.Parallel()
	m := echo(t)
	m.gate = make(chan struct{})
	rt, err := pool.NewRouter([]pool.Pool{pool.NewProcess("chat"), pool.NewProcess("work")},
		[]pool.Rule{{Pool: "work", When: pool.Background()}}, "chat", pool.WithLimit("work", 1))
	if err != nil {
		t.Fatal(err)
	}
	clk := clocktest.NewFake(time.Time{})
	mem := scoped.Memory(clk)
	r := newRig(t, m, rigConfig{clock: clk, store: mem, api: []agentapi.Option{agentapi.WithRouter(rt)}})
	task := r.task(`{"input":"one"}`)
	if task.Pool != "work" {
		t.Errorf("the task's pool = %q, want work", task.Pool)
	}
	tenant, err := tenancy.NewTenant(agentapi.DefaultTenant)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := mem.Get(tenancy.With(t.Context(), tenant), task.ID)
	if err != nil || stored.Lane != "work" || stored.Pool != "work" {
		t.Errorf("stored task = lane %q, pool %q (%v), want both work", stored.Lane, stored.Pool, err)
	}
	resp, body := r.do(http.MethodPost, "/v1/tasks", `{"input":"two"}`)
	wantError(t, "a task with its pool full", resp, body, http.StatusTooManyRequests, agentapi.CodeOverCapacity)
	m.gate <- struct{}{}
	r.waitTask(task.ID)
	testkit.Eventually(t, 30*time.Second, func() bool {
		if !rt.Slots("work").TryAcquire() {
			return false
		}
		rt.Slots("work").Release()
		return true
	})
	again := r.task(`{"input":"in the freed slot"}`)
	m.gate <- struct{}{}
	r.waitTask(again.ID)
}

// Limits: the background limit counts tasks only; a chat run going on takes
// no task's place.
func TestTaskLimitCountsTasksOnly(t *testing.T) {
	t.Parallel()
	m := echo(t)
	m.gate = make(chan struct{})
	r := newRig(t, m, rigConfig{api: []agentapi.Option{agentapi.WithMaxBackgroundRuns(1)}})
	r.models = nil // a task's conversation is no chat stream's
	chat := r.start(t.Context(), "a chat", "")
	chat.until(agentapi.TypeRunStart, 0)
	task := r.task(`{"input":"one"}`)
	resp, body := r.do(http.MethodPost, "/v1/tasks", `{"input":"two"}`)
	wantError(t, "a second task", resp, body, http.StatusTooManyRequests, agentapi.CodeTooManyBackgroundRuns)
	m.gate <- struct{}{}
	m.gate <- struct{}{}
	chat.all()
	if run := r.waitTask(task.ID); run.Status != agentapi.StatusDone {
		t.Errorf("task = %s, want done", jsonOf(run))
	}
}

// Limits: a task still going on at its wall-clock limit fails, not
// retryable, with wall_clock_limit, in its stream and in the run.
func TestTaskWallClockLimit(t *testing.T) {
	t.Parallel()
	const limit = 10 * time.Second // within the runner's lease, which the fake clock also moves
	m := echo(t)
	m.gate = make(chan struct{})
	r := newRig(t, m, rigConfig{api: []agentapi.Option{agentapi.WithTaskTimeLimit(limit)}})
	task := r.task(`{"input":"take your time"}`)
	w := r.follow(task.ID, "")
	w.until(agentapi.TypeRunStart, 0)
	r.clock.Advance(limit)
	end := w.all()[len(w.events)-1]
	if end.Type != agentapi.TypeRunError || end.Error.Code != agentapi.CodeWallClockLimit || end.Error.Retryable {
		t.Fatalf("the task's stream ended with %s, want run_error wall_clock_limit, not retryable", jsonOf(end))
	}
	run := r.waitTask(task.ID)
	if run.Status != agentapi.StatusFailed || run.Error == nil || run.Error.Code != agentapi.CodeWallClockLimit || run.Error.Retryable {
		t.Errorf("task = %s, want failed with wall_clock_limit", jsonOf(run))
	}
	listed := r.listRuns(url.Values{"background": {"true"}, "status": {agentapi.StatusFailed}}).Runs
	if len(listed) != 1 || listed[0].Error == nil || listed[0].Error.Code != agentapi.CodeWallClockLimit {
		t.Errorf("failed tasks listed %s, want the task with wall_clock_limit", jsonOf(listed))
	}
}

// A chat run has no wall-clock limit: only the run timeout, which a task
// does not have.
func TestTaskLimitsLeaveChats(t *testing.T) {
	t.Parallel()
	const limit = 10 * time.Second
	m := echo(t)
	m.gate = make(chan struct{})
	r := newRig(t, m, rigConfig{api: []agentapi.Option{agentapi.WithTaskTimeLimit(limit), agentapi.WithRunTimeout(limit / 2)}})
	task := r.task(`{"input":"slow"}`)
	w := r.follow(task.ID, "")
	w.until(agentapi.TypeRunStart, 0)
	s := r.start(t.Context(), "chat", "")
	s.until(agentapi.TypeRunStart, 0)
	r.clock.Advance(limit / 2) // the chat's run timeout, not the task's limit
	if end := s.all()[len(s.events)-1]; end.Type != agentapi.TypeRunError || end.Error.Code != agentapi.CodeTimeout {
		t.Errorf("the chat ended with %s, want its run timeout", jsonOf(end))
	}
	if run := r.getRun(task.ID); run.Status != agentapi.StatusRunning {
		t.Errorf("past the chat's run timeout the task is %s, want running", run.Status)
	}
	m.gate <- struct{}{}
	w.all()
	if run := r.waitTask(task.ID); run.Status != agentapi.StatusDone {
		t.Errorf("task = %s, want done", jsonOf(run))
	}
}

// GET /v1/runs lists the tenant's runs, newest first, filtered by status,
// background and agent, a page at a time.
func TestListTasks(t *testing.T) {
	t.Parallel()
	agents := []agentapi.Agent{
		{Name: agentapi.DefaultAgent, Harness: agentHarness(t, echo(t), registry(t))},
		{Name: "other", Harness: agentHarness(t, echo(t), registry(t))},
	}
	r := newRig(t, echo(t), rigConfig{agents: agents})
	r.models = nil
	chat := r.runJSON("a chat", "")
	t1 := r.task(`{"input":"one"}`)
	t2 := r.task(`{"input":"two","agent":"other"}`)
	r.waitTask(t1.ID)
	r.waitTask(t2.ID)

	for q, want := range map[string][]string{
		"":                              {t2.ID, t1.ID, chat.ID},
		"background=true":               {t2.ID, t1.ID},
		"background=false":              {chat.ID},
		"agent=other":                   {t2.ID},
		"agent=default&background=true": {t1.ID},
		"agent=nobody":                  {},
		"status=done&background=true":   {t2.ID, t1.ID},
		"status=running":                {},
		"conversation_id=" + t1.ConversationID + "&background=false": {},
	} {
		v, _ := url.ParseQuery(q)
		list := r.listRuns(v)
		if got := runIDs(list.Runs); !slices.Equal(got, want) {
			t.Errorf("GET /v1/runs?%s = %v, want %v", q, got, want)
		}
		for _, run := range list.Runs {
			if run.Background != (run.ID != chat.ID) {
				t.Errorf("listed %s with background %v", run.ID, run.Background)
			}
		}
	}
	var paged []string
	q := url.Values{"limit": {"1"}}
	for range 5 {
		page := r.listRuns(q)
		paged = append(paged, runIDs(page.Runs)...)
		if page.NextCursor == "" {
			break
		}
		q.Set("cursor", page.NextCursor)
	}
	if want := []string{t2.ID, t1.ID, chat.ID}; !slices.Equal(paged, want) {
		t.Errorf("pages of one %v, want %v", paged, want)
	}
}

// A task's webhook: the request's notify, or else its agent's default, is
// stored with the run and shown pending; a URL the server must not post to,
// or one on a server without webhooks, is 400. The payload is the run as it
// ended; the secret is the tenant's, for admins.
func TestTaskNotify(t *testing.T) {
	t.Parallel()
	tenant := fmt.Sprintf("hooks-%d", time.Now().UnixNano())
	hooks := agentapi.WithWebhooks(agentapi.Webhooks{
		Secret: func(tn tenancy.Tenant) (string, error) { return "whsec_" + tn.ID(), nil },
		CheckURL: func(raw string) error {
			if strings.Contains(raw, "10.0.0.1") {
				return errors.New("a private address")
			}
			return nil
		},
	})
	agents := []agentapi.Agent{
		{Name: agentapi.DefaultAgent, Harness: agentHarness(t, echo(t), registry(t)), Notify: "https://hooks.example/default"},
		{Name: "quiet", Harness: agentHarness(t, echo(t), registry(t))},
	}
	r := newRig(t, echo(t), rigConfig{agents: agents, api: []agentapi.Option{hooks, agentapi.WithTenant(func(*http.Request) string { return tenant })}})
	r.models = nil

	own := r.task(`{"input":"hi","notify":{"url":"https://hooks.example/mine"}}`)
	byAgent := r.task(`{"input":"hi"}`)
	none := r.task(`{"input":"hi","agent":"quiet"}`)
	for run, want := range map[string]string{own.ID: "https://hooks.example/mine", byAgent.ID: "https://hooks.example/default", none.ID: ""} {
		got := r.waitTask(run)
		switch {
		case want == "" && got.Notify != nil:
			t.Errorf("task %s has a webhook %s, want none", run, jsonOf(got.Notify))
		case want != "" && (got.Notify == nil || got.Notify.URL != want || got.Notify.State != wire.NotificationPending || len(got.Notify.Attempts) != 0):
			t.Errorf("task %s has webhook %s, want %s pending", run, jsonOf(got.Notify), want)
		}
	}
	for _, body := range []string{
		`{"input":"hi","notify":{"url":"https://10.0.0.1/hook"}}`,
		`{"input":"hi","notify":{"url":"ftp://hooks.example/x"}}`,
		`{"input":"hi","notify":{}}`,
		`{"input":"hi","interaction":"supervised"}`,
		`{"input":"hi","policy":{"rules":"no"}}`,
		`{"input":"hi","stray":1}`,
	} {
		resp, b := r.do(http.MethodPost, "/v1/tasks", body)
		wantError(t, "POST /v1/tasks "+body, resp, b, http.StatusBadRequest, agentapi.CodeInvalidRequest)
	}

	payload, err := r.srv.WebhookPayload(tenancy.With(t.Context(), mustTenant(t, tenant)), own.ID)
	if err != nil {
		t.Fatal(err)
	}
	var ev wire.WebhookEvent
	if err := json.Unmarshal(payload, &ev); err != nil || ev.V != 1 || ev.Type != wire.TypeRunEnded ||
		ev.Run.ID != own.ID || ev.Run.Status != agentapi.StatusDone || ev.Run.Answer != "echo: hi" || ev.Run.Record != nil || ev.Run.Notify != nil {
		t.Errorf("payload %s (%v), want run.ended with the run done, no record or webhook", payload, err)
	}
	var secret wire.WebhookSecret
	if code := r.get("/v1/webhooks/secret", &secret); code != http.StatusOK || secret.Secret != "whsec_"+tenant {
		t.Errorf("GET /v1/webhooks/secret = %d %+v, want the tenant's", code, secret)
	}

	plain := newRig(t, echo(t), rigConfig{})
	resp, b := plain.do(http.MethodPost, "/v1/tasks", `{"input":"hi","notify":{"url":"https://hooks.example/x"}}`)
	wantError(t, "a webhook on a server without them", resp, b, http.StatusBadRequest, agentapi.CodeInvalidRequest)
	if _, err := agentapi.NewAgents(agents, agentapi.WithStore(scoped.Memory(nil))); !errors.Is(err, agentapi.ErrInvalidConfig) {
		t.Errorf("an agent's default webhook without WithWebhooks: %v, want ErrInvalidConfig", err)
	}
}
