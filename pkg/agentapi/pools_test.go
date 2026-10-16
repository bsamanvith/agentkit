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

// recording is a process pool that notes the agent of each request it runs.
type recording struct {
	pool.Pool
	mu  sync.Mutex
	ran []string
}

func (p *recording) Agent(ctx context.Context, r pool.Request) (runner.Agent, error) {
	p.mu.Lock()
	p.ran = append(p.ran, r.Agent)
	p.mu.Unlock()
	return p.Pool.Agent(ctx, r)
}

func (p *recording) agents() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.ran)
}

// TestRunsRouteThroughPools: the server's router places each chat run (T104):
// a chat goes to the chat pool, and an agent that needs more isolation than
// any pool gives does not run at all.
func TestRunsRouteThroughPools(t *testing.T) {
	t.Parallel()
	plain, locked := echo(t), echo(t)
	chats := &recording{Pool: pool.NewProcess("chat")}
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
	if got := chats.agents(); !slices.Equal(got, []string{"plain"}) || len(plain.requests()) != 1 {
		t.Errorf("the chat pool ran %v and plain's model got %d requests; want plain's chat run there", got, len(plain.requests()))
	}

	resp, body := r.do("POST", "/v1/runs", jsonRun("hello", "", "locked"), "Accept", "application/json")
	wantError(t, "a run of an agent no pool may run", resp, body, http.StatusConflict, agentapi.CodeInvalidRequest)
	if n := len(locked.requests()); n != 0 {
		t.Errorf("the agent that needs a VM ran in process: its model got %d requests", n)
	}
}
