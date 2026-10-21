package pool_test

import (
	"context"
	"errors"
	"iter"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/runner/pool"
)

// fake is a pool that runs nothing: its agents say which pool made them.
type fake struct {
	name string
	iso  pool.Isolation
}

func (f fake) Name() string              { return f.name }
func (f fake) Isolation() pool.Isolation { return f.iso }
func (f fake) Agent(context.Context, pool.Request) (runner.Agent, error) {
	return madeBy(f.name), nil
}
func (f fake) Close() error { return nil }

type madeBy string

func (madeBy) Run(context.Context, harness.Input) iter.Seq2[harness.Event, error] {
	return func(func(harness.Event, error) bool) {}
}

func router(t *testing.T, pools []pool.Pool, rules []pool.Rule, def string) *pool.Router {
	t.Helper()
	rt, err := pool.NewRouter(pools, rules, def)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	return rt
}

// placed routes r and returns the pool whose agent the router gave.
func placed(t *testing.T, rt *pool.Router, r pool.Request) (string, error) {
	t.Helper()
	a, err := rt.Agent(t.Context(), r)
	if err != nil {
		return "", err
	}
	return string(a.(madeBy)), nil
}

func chat(agent string) pool.Request {
	return pool.Request{Placement: runner.Placement{ConversationID: "c", Interactive: true}, Agent: agent}
}

func background(agent string) pool.Request {
	return pool.Request{Placement: runner.Placement{ConversationID: "c"}, Agent: agent}
}

func TestRouterRoutesByRule(t *testing.T) {
	t.Parallel()
	rt := router(t,
		[]pool.Pool{fake{"default", pool.Process}, fake{"chat", pool.Process}, fake{"background", pool.Process}, fake{"research", pool.Process}},
		[]pool.Rule{
			{Pool: "research", When: pool.All(pool.Background(), pool.Agents("researcher"))},
			{Pool: "chat", When: pool.Chats()},
			{Pool: "background", When: pool.Background()},
		},
		"default")
	for _, c := range []struct {
		name string
		req  pool.Request
		want string
	}{
		{"a chat", chat("a"), "chat"},
		{"a background run", background("a"), "background"},
		{"the researcher's background run", background("researcher"), "research"},
		{"the researcher's chat", chat("researcher"), "chat"},
	} {
		if got, err := placed(t, rt, c.req); err != nil || got != c.want {
			t.Errorf("%s: placed in %q, %v; want %q", c.name, got, err, c.want)
		}
	}

	only := router(t, []pool.Pool{fake{"default", pool.Process}, fake{"chat", pool.Process}}, []pool.Rule{{Pool: "chat", When: pool.Chats()}}, "default")
	if got, err := placed(t, only, background("a")); err != nil || got != "default" {
		t.Errorf("a run no rule takes: placed in %q, %v; want the default", got, err)
	}
}

func TestRouterNeverDowngrades(t *testing.T) {
	t.Parallel()
	pools := []pool.Pool{fake{"default", pool.Process}, fake{"chat", pool.Process}, fake{"gvisor", pool.Sandbox}, fake{"vm", pool.VM}}
	rt := router(t, pools,
		[]pool.Rule{
			{Pool: "chat", When: pool.Chats()},      // too weak for the agents below: passed over
			{Pool: "gvisor", When: pool.Chats()},    // chats that need a sandbox
			{Pool: "vm", When: pool.Needs(pool.VM)}, // anything that needs a VM
		},
		"default")
	for _, c := range []struct {
		name string
		iso  pool.Isolation
		req  func(string) pool.Request
		want string
	}{
		{"a chat that needs a sandbox", pool.Sandbox, chat, "gvisor"},
		{"a chat that needs a VM", pool.VM, chat, "vm"},
		{"a background run that needs a VM", pool.VM, background, "vm"},
	} {
		r := c.req("a")
		r.Isolation = c.iso
		if got, err := placed(t, rt, r); err != nil || got != c.want {
			t.Errorf("%s: placed in %q, %v; want %q", c.name, got, err, c.want)
		}
	}

	// No rule gives a background run that needs a sandbox a pool, and the
	// default is a process: it must not run.
	r := background("a")
	r.Isolation = pool.Sandbox
	if got, err := placed(t, rt, r); !errors.Is(err, pool.ErrNoPool) {
		t.Errorf("a background run that needs a sandbox: placed in %q, %v; want ErrNoPool", got, err)
	}
	if _, err := pool.Default().Agent(t.Context(), r); !errors.Is(err, pool.ErrNoPool) {
		t.Errorf("the default router ran an agent that needs a sandbox: %v", err)
	}
}

func TestNewRouterRejects(t *testing.T) {
	t.Parallel()
	p := fake{"p", pool.Process}
	for _, c := range []struct {
		name  string
		pools []pool.Pool
		rules []pool.Rule
		def   string
	}{
		{"no default", []pool.Pool{p}, nil, ""},
		{"an unknown default", []pool.Pool{p}, nil, "q"},
		{"a rule naming an unknown pool", []pool.Pool{p}, []pool.Rule{{Pool: "q"}}, "p"},
		{"two pools of one name", []pool.Pool{p, fake{"p", pool.VM}}, nil, "p"},
		{"a pool with no name", []pool.Pool{p, fake{"", pool.VM}}, nil, "p"},
		{"a nil pool", []pool.Pool{p, nil}, nil, "p"},
		{"a nil pointer pool", []pool.Pool{p, (*spawner)(nil)}, nil, "p"},
	} {
		if _, err := pool.NewRouter(c.pools, c.rules, c.def); !errors.Is(err, pool.ErrInvalidConfig) {
			t.Errorf("%s: NewRouter = %v, want ErrInvalidConfig", c.name, err)
		}
	}
}

// The process pool runs the agent the server built, and refuses a request
// that has none rather than run nothing.
func TestProcessRunsTheBuiltAgent(t *testing.T) {
	t.Parallel()
	p := pool.NewProcess("default")
	built := madeBy("server")
	r := chat("a")
	r.Built = built
	if a, err := p.Agent(t.Context(), r); err != nil || a != runner.Agent(built) {
		t.Errorf("Agent = %v, %v; want the built agent", a, err)
	}
	if _, err := p.Agent(t.Context(), chat("a")); !errors.Is(err, pool.ErrNoBuiltAgent) {
		t.Errorf("Agent with no built agent = %v, want ErrNoBuiltAgent", err)
	}
}

func TestParseIsolation(t *testing.T) {
	t.Parallel()
	for _, i := range []pool.Isolation{pool.Process, pool.Sandbox, pool.VM} {
		if got, err := pool.ParseIsolation(i.String()); err != nil || got != i {
			t.Errorf("ParseIsolation(%q) = %v, %v", i, got, err)
		}
	}
	if _, err := pool.ParseIsolation("container"); !errors.Is(err, pool.ErrInvalidConfig) {
		t.Errorf("ParseIsolation(container) = %v, want ErrInvalidConfig", err)
	}
}
