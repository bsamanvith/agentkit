package main

import (
	"errors"
	"fmt"
	"slices"

	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/runner/pool"
)

// runnersConfig is the "runners" section of the serve config file: the
// runner pools the agent API runs its agents in (E20), the routes that send
// runs to them, and the default pool. Without it there is one process pool,
// "default", with no limit of its own: the server as it ran before pools.
//
//	"runners": {
//	  "pools": [
//	    {"name": "chat", "isolation": "process", "limit": 16},
//	    {"name": "background", "limit": 4}
//	  ],
//	  "routes": [
//	    {"pool": "chat", "runs": "chat"},
//	    {"pool": "background", "runs": "background"}
//	  ],
//	  "default": "background"
//	}
type runnersConfig struct {
	Pools  []poolConfig  `json:"pools"`
	Routes []routeConfig `json:"routes"`
	// Default names the pool of the runs no route takes; it may be left
	// out with one pool.
	Default string `json:"default"`
}

// poolConfig is one pool of runners.pools.
type poolConfig struct {
	// Name names the pool in routes, runs (GET /v1/runs/{id}) and logs, and
	// is the lane of the run queue its background runs wait in.
	Name string `json:"name"`
	// Isolation is the pool's: "process" (the default) until the sandbox
	// (T106) and vm (T107) pools exist.
	Isolation string `json:"isolation"`
	// Limit caps the pool's runs going on at once: a chat past it gets 429,
	// a background run waits in the store for a free slot. 0: no limit of
	// its own (the API's max_concurrent_runs still holds).
	Limit int `json:"limit"`
}

// routeConfig is one route of runners.routes: the runs it takes go to Pool.
// Every condition given must hold; routes are tried in order, and a route
// whose pool is less isolated than the run's agent needs is passed over.
type routeConfig struct {
	Pool string `json:"pool"`
	// Runs is "chat" (a user waits: POST /v1/runs), "background" (queued
	// work, and runs the dispatcher resumes), or "" for both.
	Runs string `json:"runs"`
	// Agents limits the route to the runs of these agents.
	Agents []string `json:"agents"`
	// Isolation limits the route to the runs of agents that need at least
	// this isolation (an agent's "isolation").
	Isolation string `json:"isolation"`
}

// The values of routeConfig.Runs.
const (
	runsChat       = "chat"
	runsBackground = "background"
)

// poolTask is the task that brings the pools of an isolation level.
var poolTask = map[pool.Isolation]string{pool.Sandbox: "T106", pool.VM: "T107"}

// errRunners prefixes every mistake in the runners section.
var errRunners = errors.New("runners")

// defaultPool names the pool of the runs no route takes.
func (c runnersConfig) defaultPool() string {
	switch {
	case c.Default != "":
		return c.Default
	case len(c.Pools) > 0:
		return c.Pools[0].Name
	}
	return pool.DefaultName
}

// dispatched names the pools that background runs may be routed to, the
// default first: each gets a dispatcher of its lane (agentapi's
// Server.Dispatcher). A pool only chats are routed to gets none, so no
// dispatcher waits on the slots its chats need.
func (c runnersConfig) dispatched() []string {
	out := []string{c.defaultPool()}
	for _, r := range c.Routes {
		if r.Runs != runsChat && !slices.Contains(out, r.Pool) {
			out = append(out, r.Pool)
		}
	}
	return out
}

// router builds the pools and routes of c for agents (whose isolations,
// parsed, are in isolations), and checks that every agent's chats and
// background runs have a pool isolated enough; legacy says the agents come
// from the one-agent form (serveConfig.legacy), for the names in errors.
// With no pools it returns a nil router: the server's default.
func (c runnersConfig) router(agents []agentDef, isolations map[string]pool.Isolation, legacy bool) (*pool.Router, error) {
	fail := func(format string, args ...any) (*pool.Router, error) {
		return nil, fmt.Errorf("%w.%s", errRunners, fmt.Sprintf(format, args...))
	}
	if len(c.Pools) == 0 {
		if len(c.Routes) > 0 || c.Default != "" {
			return fail("pools: routes and a default need pools")
		}
		for _, a := range agents {
			if iso := isolations[a.Name]; iso > pool.Process {
				return nil, fmt.Errorf("%s.isolation: %s needs a %s pool in runners.pools (%s pools come with %s)", a.label(legacy), iso, iso, iso, poolTask[iso])
			}
		}
		return nil, nil
	}

	var pools []pool.Pool
	var opts []pool.RouterOption
	for i, p := range c.Pools {
		switch {
		case !validAgentName.MatchString(p.Name):
			return fail("pools[%d].name %q: want ^[a-zA-Z0-9_-]{1,64}$", i, p.Name)
		case slices.ContainsFunc(c.Pools[:i], func(q poolConfig) bool { return q.Name == p.Name }):
			return fail("pools[%d].name %q: two pools have that name", i, p.Name)
		case p.Limit < 0:
			return fail("pools.%s.limit: negative", p.Name)
		}
		iso, err := pool.ParseIsolation(p.Isolation)
		if err != nil {
			return fail("pools.%s.isolation: %v", p.Name, err)
		}
		if iso != pool.Process {
			return fail("pools.%s.isolation: %s pools come with %s; only process pools run yet", p.Name, iso, poolTask[iso])
		}
		pools = append(pools, pool.NewProcess(p.Name))
		if p.Limit > 0 {
			opts = append(opts, pool.WithLimit(p.Name, p.Limit))
		}
	}
	known := func(name string) bool {
		return slices.ContainsFunc(c.Pools, func(p poolConfig) bool { return p.Name == name })
	}
	def := c.defaultPool()
	switch {
	case c.Default == "" && len(c.Pools) > 1:
		return fail("default: name the pool of the runs no route takes")
	case !known(def):
		return fail("default: %q is not one of the pools", def)
	}

	var rules []pool.Rule
	for i, r := range c.Routes {
		if !known(r.Pool) {
			return fail("routes[%d].pool: %q is not one of the pools", i, r.Pool)
		}
		var when []pool.Match
		switch r.Runs {
		case runsChat:
			when = append(when, pool.Chats())
		case runsBackground:
			when = append(when, pool.Background())
		case "":
		default:
			return fail("routes[%d].runs: %q (want %q, %q, or none for both)", i, r.Runs, runsChat, runsBackground)
		}
		for _, name := range r.Agents {
			if _, ok := isolations[name]; !ok {
				return fail("routes[%d].agents: %q is not one of the agents", i, name)
			}
		}
		if r.Agents != nil && len(r.Agents) == 0 {
			return fail("routes[%d].agents: empty, so the route takes no run; leave it out for every agent", i)
		}
		if len(r.Agents) > 0 {
			when = append(when, pool.Agents(r.Agents...))
		}
		if r.Isolation != "" {
			iso, err := pool.ParseIsolation(r.Isolation)
			if err != nil {
				return fail("routes[%d].isolation: %v", i, err)
			}
			when = append(when, pool.Needs(iso))
		}
		rules = append(rules, pool.Rule{Pool: r.Pool, When: pool.All(when...)})
	}
	rt, err := pool.NewRouter(pools, rules, def, opts...)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errRunners, err)
	}
	// Every agent's runs must have a pool: a run that has none would fail
	// at run time instead (pool.ErrNoPool).
	for _, a := range agents {
		for _, interactive := range []bool{true, false} {
			req := pool.Request{Placement: runner.Placement{Interactive: interactive}, Agent: a.Name, Isolation: isolations[a.Name]}
			if _, err := rt.Route(req); err != nil {
				_ = rt.Close()
				iso := isolations[a.Name]
				return nil, fmt.Errorf("%s.isolation: no pool in runners runs its %s runs with %s isolation (%s pools come with %s)",
					a.label(legacy), map[bool]string{true: runsChat, false: runsBackground}[interactive], iso, iso, poolTask[iso])
			}
		}
	}
	return rt, nil
}
