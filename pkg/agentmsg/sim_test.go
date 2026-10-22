package agentmsg_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentmsg"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/memory"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// The simulation (ENGINEERING.md §2.6) plays a random history, chosen by
// seed, of three agents that may all message each other, on one memory
// store and a fake clock, driven one step at a time: an agent's task sends
// a message (in a new thread or one it is part of); a dispatcher (of two,
// sharing the work through leases) takes one step, and may crash before or
// after it starts a run, or after any of its writes; a run of an agent ends,
// with tokens, after sending a message of its own in the thread it got now
// and then (so threads ping-pong into their limits); or the clock moves on
// (leases lapse, retries come due, budgets run out, rate limits refill).
//
// The runs are the simulation's own ([simRuns], an [agentmsg.Runs]): a run
// is a prompt in its conversation and ends when the simulation says so, so
// every interleaving is chosen by the seed, never the scheduler. After every
// step the log's invariants are checked ([checkLog]); at the end every run
// is let finish and the dispatchers drain the log, after which every
// message must be processed and every request answered, unless its thread
// was stopped.

const (
	simTTL   = 10 * time.Second
	simRetry = time.Second
	simHops  = 6
)

var simAgents = []string{"a", "b", "c"}

func TestSimulation(t *testing.T) {
	t.Parallel()
	seeds := 60
	if testing.Short() {
		seeds = 20
	}
	mix := map[string]int{}
	ran := 0
	for seed := range uint64(seeds) {
		ok := t.Run(fmt.Sprint("seed", seed), func(t *testing.T) {
			ran++
			for k, v := range simulate(t, seed) {
				mix[k] += v
			}
		})
		if !ok {
			t.Fatalf("seed %d failed; rerun with -run 'TestSimulation/seed%d$'", seed, seed)
		}
	}
	if ran < seeds {
		return // a -run of some seeds
	}
	// Every kind of step and outcome the simulation can reach, it reached:
	// a simulation that never crashes or never stops a thread checks little.
	for _, k := range []string{
		"send", "send refused", "dispatch", "crash before deliver", "crash after deliver", "crash after write",
		"run ends", "run sends", "busy", "advance", "stopped " + agentmsg.CodeHopLimit, "stopped " + agentmsg.CodeTokenBudget,
		"stopped " + agentmsg.CodeTimeBudget, "refused " + agentmsg.CodeRateLimited, "redelivered",
	} {
		if mix[k] == 0 {
			t.Errorf("no seed reached %q: %v", k, mix)
		}
	}
	t.Logf("mix: %v", mix)
}

func FuzzSimulation(f *testing.F) {
	f.Add(uint64(1))
	f.Fuzz(func(t *testing.T, seed uint64) { simulate(t, seed) })
}

func simulate(t *testing.T, seed uint64) map[string]int {
	rng := rand.New(rand.NewPCG(seed, 0x7073))
	mix := map[string]int{}
	clk := clocktest.NewFake(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	mem := memory.New(memory.WithClock(clk))
	st := &crashStore{Store: mem}
	var specs []agentmsg.Agent
	for _, a := range simAgents {
		var others []string
		for _, b := range simAgents {
			if b != a {
				others = append(others, b)
			}
		}
		specs = append(specs, agentmsg.Agent{Name: a, CanMessage: others})
	}
	var ids int
	m, err := agentmsg.New(st, specs, agentmsg.WithClock(clk), agentmsg.WithIDs(func(prefix string) string {
		ids++
		return fmt.Sprintf("%s%04d", prefix, ids)
	}), agentmsg.WithLimits(agentmsg.Limits{
		MaxHops: simHops, MaxTokens: 600, MaxDuration: 3 * time.Minute, SendEvery: 5 * time.Second, SendBurst: 3,
	}))
	if err != nil {
		t.Fatal(err)
	}
	tn, err := tenancy.NewTenant("sim")
	if err != nil {
		t.Fatal(err)
	}
	ctx := tenancy.With(context.Background(), tn)
	runs := &simRuns{st: mem, clk: clk, byConv: map[string][]*simRun{}, byID: map[string]*simRun{}}
	var dispatchers []*agentmsg.Dispatcher
	for i := range 2 {
		d, err := m.Dispatcher(memory.MessageAdmin(mem), runs, agentmsg.WithOwner(fmt.Sprint("d", i)),
			agentmsg.WithLease(simTTL), agentmsg.WithRetry(simRetry))
		if err != nil {
			t.Fatal(err)
		}
		dispatchers = append(dispatchers, d)
	}
	sendTools := map[string]tool.Tool{}
	for _, a := range simAgents {
		tools, err := m.Tools(a)
		if err != nil {
			t.Fatal(err)
		}
		for _, tl := range tools {
			if tl.Definition().Name == agentmsg.MessageToolName {
				sendTools[a] = tl
			}
		}
	}
	// sendFrom sends a message as agent from its conversation conv: in
	// thread (or a new one), to another agent at random.
	sendFrom := func(from, conv, thread string) {
		to := simAgents[rng.IntN(len(simAgents))]
		for to == from {
			to = simAgents[rng.IntN(len(simAgents))]
		}
		in := map[string]any{"to": agentmsg.Address(to), "text": fmt.Sprintf("from %s in %s", from, conv)}
		if thread != "" {
			in["thread"] = thread
		}
		b, _ := json.Marshal(in)
		_, err := sendTools[from].Invoke(agentmsg.WithSender(ctx, from, conv), b)
		var me *agentmsg.Error
		switch {
		case err == nil:
			mix["send"]++
		case errors.As(err, &me):
			mix["send refused"]++
			mix["refused "+me.Code]++
		default:
			t.Fatalf("seed %d: send: %v", seed, err)
		}
	}
	// step takes a dispatcher's step, crashing it as the seed says.
	step := func(drain bool) bool {
		d := dispatchers[rng.IntN(len(dispatchers))]
		sctx, crash := context.WithCancel(ctx)
		defer crash()
		mode := 0
		if !drain {
			mode = rng.IntN(8)
		}
		switch mode {
		case 1:
			mix["crash before deliver"]++
			sctx = withCrash(sctx, &crashPlan{beforeDeliver: true, crash: crash})
		case 2:
			mix["crash after deliver"]++
			sctx = withCrash(sctx, &crashPlan{afterDeliver: true, crash: crash})
		case 3:
			mix["crash after write"]++
			sctx = withCrash(sctx, &crashPlan{afterWrites: 1 + rng.IntN(2), crash: crash})
		}
		worked, err := d.Step(sctx)
		if err != nil && sctx.Err() == nil {
			t.Fatalf("seed %d: step: %v", seed, err)
		}
		if worked {
			mix["dispatch"]++
		}
		return worked
	}
	// end ends a run: now and then it first sends a message in the thread
	// of the message it processes.
	end := func(r *simRun, mayReply bool) {
		if mayReply && rng.IntN(2) == 0 {
			if env, ok := agentmsg.ParseEnvelope(r.input); ok {
				mix["run sends"]++
				sendFrom(r.agent, r.conv, env.Thread)
			}
		}
		r.done, r.answer, r.tokens = true, "answer of "+r.id, rng.IntN(400)
		mix["run ends"]++
	}

	for range 40 + rng.IntN(80) {
		switch k := rng.IntN(10); {
		case k < 2:
			a := simAgents[rng.IntN(len(simAgents))]
			thread := ""
			if th := threadsOf(ctx, t, mem); len(th) > 0 && rng.IntN(3) == 0 {
				thread = th[rng.IntN(len(th))]
			}
			sendFrom(a, "task_"+a, thread)
		case k < 6:
			step(false)
		case k < 8:
			if open := runs.unfinished(); len(open) > 0 {
				end(open[rng.IntN(len(open))], true)
			}
		default:
			mix["advance"]++
			clk.Advance(time.Duration(rng.IntN(int(2 * simTTL))))
		}
		checkLog(ctx, t, seed, mem, runs)
	}

	// Drain: every run ends without sending, every lease lapses, and the
	// dispatchers act until nothing is due.
	for range 1000 {
		for _, r := range runs.unfinished() {
			end(r, false)
		}
		clk.Advance(simTTL + simRetry)
		for step(true) {
			checkLog(ctx, t, seed, mem, runs)
		}
		if len(runs.unfinished()) == 0 && settled(ctx, t, mem) {
			break
		}
	}
	checkLog(ctx, t, seed, mem, runs)
	checkSettled(ctx, t, seed, mem, mix)
	mix["busy"] += runs.busy
	return mix
}

// crashPlan says where a dispatcher's step crashes: it cancels the step's
// context, and every later write of the step fails.
type crashPlan struct {
	beforeDeliver, afterDeliver bool
	afterWrites                 int
	writes                      int
	crash                       context.CancelFunc
}

type crashKey struct{}

func withCrash(ctx context.Context, p *crashPlan) context.Context {
	return context.WithValue(ctx, crashKey{}, p)
}

func planOf(ctx context.Context) *crashPlan {
	p, _ := ctx.Value(crashKey{}).(*crashPlan)
	return p
}

// crashStore is the messenger's store: a write in a crashed step fails, and
// a write may crash the step once it has landed.
type crashStore struct{ *memory.Store }

func (s *crashStore) write(ctx context.Context) func() {
	return func() {
		if p := planOf(ctx); p != nil && p.afterWrites > 0 {
			if p.writes++; p.writes == p.afterWrites {
				p.crash()
			}
		}
	}
}

func (s *crashStore) SendMessage(ctx context.Context, m store.Message) (store.Message, error) {
	if err := ctx.Err(); err != nil {
		return store.Message{}, err
	}
	defer s.write(ctx)()
	return s.Store.SendMessage(ctx, m)
}

func (s *crashStore) UpdateMessage(ctx context.Context, id, owner string, u store.MessageUpdate) (store.Message, error) {
	if err := ctx.Err(); err != nil {
		return store.Message{}, err
	}
	defer s.write(ctx)()
	return s.Store.UpdateMessage(ctx, id, owner, u)
}

func (s *crashStore) StopThread(ctx context.Context, id, code, reason string) (store.Thread, error) {
	if err := ctx.Err(); err != nil {
		return store.Thread{}, err
	}
	defer s.write(ctx)()
	return s.Store.StopThread(ctx, id, code, reason)
}

// simRun is a run of the simulation: its prompt is in its conversation, and
// it ends when the simulation says.
type simRun struct {
	id, agent, conv, input string
	done                   bool
	answer                 string
	tokens                 int
}

// simRuns starts and reports the simulation's runs, one unfinished run per
// conversation at most.
type simRuns struct {
	st     *memory.Store
	clk    *clocktest.Fake
	byConv map[string][]*simRun
	byID   map[string]*simRun
	order  []*simRun
	busy   int
}

func (s *simRuns) Deliver(ctx context.Context, agent, conv, title, input string) (store.Run, error) {
	p := planOf(ctx)
	if p != nil && p.beforeDeliver {
		p.crash()
		return store.Run{}, ctx.Err()
	}
	rs := s.byConv[conv]
	if len(rs) > 0 && !rs[len(rs)-1].done {
		s.busy++
		return store.Run{}, fmt.Errorf("conversation %s: %w", conv, runner.ErrRunUnfinished)
	}
	c, err := s.st.GetConversation(ctx, conv)
	if errors.Is(err, store.ErrNotFound) {
		c, err = s.st.CreateConversation(ctx, store.Conversation{ID: conv, Agent: agent, Title: title})
	}
	if err != nil {
		return store.Run{}, err
	}
	if c.Agent != agent {
		return store.Run{}, fmt.Errorf("conversation %s is %s's, not %s's", conv, c.Agent, agent)
	}
	view, err := s.st.Context(ctx, conv)
	if err != nil {
		return store.Run{}, err
	}
	r := &simRun{id: fmt.Sprintf("run_%04d", len(s.order)+1), agent: agent, conv: conv, input: input}
	if err := s.st.Append(ctx, conv, store.Entry{
		Seq: view.Seq + 1, Kind: store.KindPrompt, Messages: []llm.Message{llm.User(llm.Text{Text: input})}, RunID: r.id,
	}); err != nil {
		return store.Run{}, err
	}
	s.byConv[conv] = append(rs, r)
	s.byID[r.id] = r
	s.order = append(s.order, r)
	if p != nil && p.afterDeliver {
		p.crash()
	}
	return store.Run{ID: r.id, ConversationID: conv}, nil
}

func (s *simRuns) RunResult(_ context.Context, id string) (wire.Run, error) {
	r := s.byID[id]
	if r == nil {
		return wire.Run{}, store.ErrNotFound
	}
	out := wire.Run{ID: r.id, ConversationID: r.conv}
	if r.done {
		at := s.clk.Now()
		out.FinishedAt, out.Answer, out.Usage = &at, r.answer, llm.Usage{InputTokens: r.tokens}
	}
	return out, nil
}

func (s *simRuns) unfinished() []*simRun {
	var out []*simRun
	for _, r := range s.order {
		if !r.done {
			out = append(out, r)
		}
	}
	return out
}

// threadsOf returns the IDs of the tenant's threads, oldest first.
func threadsOf(ctx context.Context, t *testing.T, st *memory.Store) []string {
	t.Helper()
	var out []string
	p := store.Page{Limit: store.MaxPageLimit}
	for {
		page, err := st.ListThreads(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		for _, th := range page.Threads {
			out = append(out, th.ID)
		}
		if page.Next == "" {
			return out
		}
		p.Cursor = page.Next
	}
}

func messagesOf(ctx context.Context, t *testing.T, st *memory.Store, thread string) []store.Message {
	t.Helper()
	ms, err := st.ThreadMessages(ctx, thread, 0, store.MaxPageLimit)
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

// checkLog checks the invariants of the message log against the runs:
//
//   - a thread's hops are 1..n, dense, and Hops is n; at most simHops of
//     its messages are not notices, and once a notice is in a thread only
//     notices follow; a thread with a notice is stopped;
//   - a reply answers a request of its thread before it, to the request's
//     sender, and a request has at most one reply, only once its run has
//     ended;
//   - every message is processed at most once: at most one prompt in all
//     the runs carries it, and one that left queued names that prompt's
//     run, in the message's ToConversation, of its recipient;
//   - a conversation gets the messages of a thread in hop order.
func checkLog(ctx context.Context, t *testing.T, seed uint64, st *memory.Store, runs *simRuns) {
	t.Helper()
	fail := func(format string, args ...any) {
		t.Helper()
		t.Fatalf("seed %d: "+format, append([]any{seed}, args...)...)
	}
	carried := map[string][]*simRun{} // message ID → the runs whose prompt carries it
	for _, r := range runs.order {
		if env, ok := agentmsg.ParseEnvelope(r.input); ok {
			carried[env.ID] = append(carried[env.ID], r)
		}
	}
	for _, id := range threadsOf(ctx, t, st) {
		th, err := st.GetThread(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		ms := messagesOf(ctx, t, st, id)
		if th.Hops != len(ms) {
			fail("thread %s: Hops %d, %d messages", id, th.Hops, len(ms))
		}
		byID := map[string]store.Message{}
		replies := map[string]int{}
		requests, noticed := 0, false
		for i, msg := range ms {
			byID[msg.ID] = msg
			if msg.Hop != i+1 {
				fail("thread %s: message %d has hop %d", id, i+1, msg.Hop)
			}
			switch msg.Kind {
			case store.MessageNotice:
				noticed = true
			default:
				if noticed {
					fail("thread %s: a %s after a stop notice: %+v", id, msg.Kind, msg)
				}
				requests++
			}
			if msg.Kind == store.MessageReply {
				req, ok := byID[msg.InReplyTo]
				switch {
				case !ok || req.Kind != store.MessageRequest:
					fail("thread %s: reply %s answers %q, no earlier request", id, msg.ID, msg.InReplyTo)
				case msg.To != req.From || msg.ToConversation != req.FromConversation || msg.From != req.To:
					fail("thread %s: reply %+v does not go back to the sender of %+v", id, msg, req)
				}
				if replies[req.ID]++; replies[req.ID] > 1 {
					fail("thread %s: request %s answered twice", id, req.ID)
				}
				if r := runs.byID[req.RunID]; r == nil || !r.done {
					fail("thread %s: request %s answered before its run %q ended", id, req.ID, req.RunID)
				}
			}
			got := carried[msg.ID]
			if len(got) > 1 {
				fail("message %s processed by %d runs", msg.ID, len(got))
			}
			if msg.State != store.MessageQueued {
				if len(got) != 1 || got[0].id != msg.RunID || got[0].conv != msg.ToConversation || agentmsg.Address(got[0].agent) != msg.To {
					fail("message %+v left queued, but its runs are %v", msg, got)
				}
			}
		}
		if requests > simHops {
			fail("thread %s has %d messages that are not notices, over the hop limit %d", id, requests, simHops)
		}
		if noticed && th.Status != store.ThreadStopped {
			fail("thread %s has a stop notice but is %s", id, th.Status)
		}
	}
	// Per conversation, the messages of each thread arrive in hop order.
	for conv, rs := range runs.byConv {
		last := map[string]int{}
		for _, r := range rs {
			env, ok := agentmsg.ParseEnvelope(r.input)
			if !ok {
				continue
			}
			if env.Hop <= last[env.Thread] {
				fail("conversation %s got hop %d of thread %s after hop %d", conv, env.Hop, env.Thread, last[env.Thread])
			}
			last[env.Thread] = env.Hop
		}
	}
}

// settled reports whether every message is done.
func settled(ctx context.Context, t *testing.T, st *memory.Store) bool {
	for _, id := range threadsOf(ctx, t, st) {
		for _, msg := range messagesOf(ctx, t, st, id) {
			if msg.State != store.MessageDone {
				return false
			}
		}
	}
	return true
}

// checkSettled checks the end of the drain: every message is done, and
// every request of a thread still open has its reply.
func checkSettled(ctx context.Context, t *testing.T, seed uint64, st *memory.Store, mix map[string]int) {
	t.Helper()
	for _, id := range threadsOf(ctx, t, st) {
		th, err := st.GetThread(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if th.Status == store.ThreadStopped {
			mix["stopped "+th.StopCode]++
		}
		ms := messagesOf(ctx, t, st, id)
		answered := map[string]bool{}
		for _, msg := range ms {
			if msg.Kind == store.MessageReply {
				answered[msg.InReplyTo] = true
			}
		}
		for _, msg := range ms {
			if msg.State != store.MessageDone {
				t.Fatalf("seed %d: after the drain, message %+v is not done", seed, msg)
			}
			if msg.Attempts > 1 {
				mix["redelivered"]++
			}
			if msg.Kind == store.MessageRequest && !answered[msg.ID] && th.Status == store.ThreadOpen {
				t.Fatalf("seed %d: request %s of open thread %s has no reply", seed, msg.ID, id)
			}
		}
		if !slices.IsSortedFunc(ms, func(a, b store.Message) int { return a.Hop - b.Hop }) {
			t.Fatalf("seed %d: thread %s out of order", seed, id)
		}
	}
}
