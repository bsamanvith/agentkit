package agentapi_test

import (
	"context"
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/runner/pool"
)

// recording is a pool that runs each request's built agent, wrapped in an
// agent of its own that it must close (as a sandbox pool's would be), and
// notes the requests it ran.
type recording struct {
	name  string
	owned pool.Owned

	mu     sync.Mutex
	ran    []pool.Request
	made   []*closing
	closed bool
}

func (p *recording) Name() string              { return p.name }
func (p *recording) Isolation() pool.Isolation { return pool.Process }

func (p *recording) Agent(_ context.Context, r pool.Request) (runner.Agent, error) {
	a := &closing{Agent: r.Built}
	if _, err := p.owned.Add(a); err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ran = append(p.ran, r)
	p.made = append(p.made, a)
	return a, nil
}

func (p *recording) Close() error {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	return p.owned.Close()
}

func (p *recording) requests() []pool.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.ran)
}

// closing is an agent with something to close.
type closing struct {
	runner.Agent
	mu     sync.Mutex
	closes int
}

func (a *closing) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closes++
	return nil
}

// TestRunsRouteThroughPools: the server's router places each chat run (T104):
// a chat goes to the chat pool with its placement, and an agent that needs
// more isolation than any pool gives does not run at all.
func TestRunsRouteThroughPools(t *testing.T) {
	t.Parallel()
	plain, locked := echo(t), echo(t)
	chats := &recording{name: "chat"}
	rt, err := pool.NewRouter([]pool.Pool{pool.NewProcess("default"), chats}, []pool.Rule{{Pool: "chat", When: pool.Chats()}}, "default")
	if err != nil {
		t.Fatal(err)
	}
	sets := demoTools(t)
	r := newRig(t, plain, rigConfig{
		agents: []agentapi.Agent{
			{Name: "plain", Harness: agentHarness(t, plain, sets["a"])},
			{Name: "locked", Harness: agentHarness(t, locked, sets["a"]), Isolation: pool.VM},
		},
		api: []agentapi.Option{agentapi.WithRouter(rt)},
	})

	r.startAgent(t.Context(), "hello", "", "plain").all()
	got := chats.requests()
	if len(got) != 1 || got[0].Agent != "plain" || !got[0].Interactive || got[0].Resume || got[0].ConversationID == "" || len(plain.requests()) != 1 {
		t.Errorf("the chat pool ran %+v and plain's model got %d requests; want plain's chat run there", got, len(plain.requests()))
	}

	resp, body := r.do("POST", "/v1/runs", jsonRun("hello", "", "locked"), "Accept", "application/json")
	wantError(t, "a run of an agent no pool may run", resp, body, http.StatusConflict, agentapi.CodeInvalidRequest)
	if n := len(locked.requests()); n != 0 {
		t.Errorf("the agent that needs a VM ran in process: its model got %d requests", n)
	}
}

// TestShutdownClosesPools: the server owns its router; Shutdown closes it
// once the runs have stopped, and so every agent its pools made (review
// item D). The orchestrator of WithRuns gets the routed factory too.
func TestShutdownClosesPools(t *testing.T) {
	t.Parallel()
	only := &recording{name: "only"}
	rt, err := pool.NewRouter([]pool.Pool{only}, nil, "only")
	if err != nil {
		t.Fatal(err)
	}
	withRuns := agentapi.WithRuns(func(st runner.Store, agents runner.AgentFactory) (agentapi.Runs, error) {
		rn, err := runner.New(st, agents)
		return &countingRuns{rn: rn}, err
	})
	r := newRig(t, echo(t), rigConfig{api: []agentapi.Option{agentapi.WithRouter(rt), withRuns}})
	r.run("one", "").all()
	r.run("two", "").all()
	if n := len(only.requests()); n != 2 {
		t.Fatalf("the pool ran %d runs of WithRuns' orchestrator, want 2", n)
	}
	if err := r.srv.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	only.mu.Lock()
	defer only.mu.Unlock()
	if !only.closed {
		t.Error("Shutdown did not close the router's pool")
	}
	for i, a := range only.made {
		a.mu.Lock()
		if a.closes != 1 {
			t.Errorf("agent %d the pool made was closed %d times, want once", i, a.closes)
		}
		a.mu.Unlock()
	}
}
