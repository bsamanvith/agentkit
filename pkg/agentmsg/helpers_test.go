package agentmsg_test

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentmsg"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/memory"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// waitFor bounds every wait of these tests for something the agents do.
const waitFor = 10 * time.Second

// brain decides an agent's next reply from the request its model gets.
type brain func(ctx context.Context, req llm.Request) llmtest.Reply

// model is an agent's model: it records every request and answers as its
// brain says.
type model struct {
	t     testing.TB
	think brain

	mu   sync.Mutex
	reqs []llm.Request
}

func (m *model) Generate(ctx context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	m.mu.Lock()
	m.reqs = append(m.reqs, req.Clone())
	m.mu.Unlock()
	return llmtest.NewFake(m.t, m.think(ctx, req)).Generate(ctx, req)
}

func (m *model) requests() []llm.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.reqs)
}

type agentSpec struct {
	name       string
	system     string
	canMessage []string
	think      brain
}

type rig struct {
	t      *testing.T
	st     *memory.Store
	m      *agentmsg.Messenger
	srv    *agentapi.Server
	models map[string]*model
	ctx    context.Context
	tasks  atomic.Int64
}

func newRig(t *testing.T, specs []agentSpec, opts ...agentmsg.Option) *rig {
	t.Helper()
	return newRigOn(t, memory.New(), specs, opts...)
}

// newRigOn is newRig over st.
func newRigOn(t *testing.T, st *memory.Store, specs []agentSpec, opts ...agentmsg.Option) *rig {
	t.Helper()
	var msgAgents []agentmsg.Agent
	for _, s := range specs {
		msgAgents = append(msgAgents, agentmsg.Agent{Name: s.name, Description: "agent " + s.name, CanMessage: s.canMessage})
	}
	m, err := agentmsg.New(st, msgAgents, append([]agentmsg.Option{agentmsg.WithPoll(20 * time.Millisecond)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	tn, err := tenancy.NewTenant(agentapi.DefaultTenant)
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, st: st, m: m, models: map[string]*model{}, ctx: tenancy.With(context.Background(), tn)}
	var agents []agentapi.Agent
	for _, s := range specs {
		tools, err := m.Tools(s.name)
		if err != nil {
			t.Fatal(err)
		}
		reg, err := tool.NewRegistry(tools)
		if err != nil {
			t.Fatal(err)
		}
		md := &model{t: t, think: s.think}
		r.models[s.name] = md
		h, err := harness.New(md, reg, harness.WithSystem(s.system))
		if err != nil {
			t.Fatal(err)
		}
		agents = append(agents, agentapi.Agent{Name: s.name, Harness: h, System: s.system})
	}
	srv, err := agentapi.NewAgents(agents, agentapi.WithStore(st), agentapi.WithRunContext(func(ctx context.Context, rc agentapi.RunContext) context.Context {
		return agentmsg.WithSender(ctx, rc.Agent, rc.ConversationID)
	}))
	if err != nil {
		t.Fatal(err)
	}
	r.srv = srv
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), waitFor)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Logf("shutdown: %v", err)
		}
	})
	return r
}

// dispatch runs a dispatcher until the test ends.
func (r *rig) dispatch(runs agentmsg.Runs, opts ...agentmsg.DispatcherOption) {
	r.t.Helper()
	if runs == nil {
		runs = r.srv
	}
	d, err := r.m.Dispatcher(memory.MessageAdmin(r.st), runs, append([]agentmsg.DispatcherOption{agentmsg.WithRetry(20 * time.Millisecond)}, opts...)...)
	if err != nil {
		r.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = d.Run(ctx)
	}()
	r.t.Cleanup(func() { cancel(); <-done })
}

// task starts a background run of agent on input, in a conversation of its
// own, and returns the conversation.
func (r *rig) task(agent, input string) string {
	r.t.Helper()
	conv := fmt.Sprintf("conv_task_%s_%d", agent, r.tasks.Add(1))
	if _, err := r.srv.Deliver(r.ctx, agent, conv, "task", input); err != nil {
		r.t.Fatalf("starting a task of %s: %v", agent, err)
	}
	return conv
}

// runs returns the runs of a conversation, oldest first.
func (r *rig) runs(conv string) []store.Run {
	r.t.Helper()
	p, err := r.st.List(r.ctx, conv, store.Page{})
	if err != nil {
		r.t.Fatal(err)
	}
	return p.Runs
}

// ended waits until conv has n runs, each ended with its answer stored, and
// returns them as the API shows them.
func (r *rig) ended(conv string, n int) []wire.Run {
	r.t.Helper()
	var out []wire.Run
	testkit.Eventually(r.t, waitFor, func() bool {
		runs := r.runs(conv)
		if len(runs) != n {
			return false
		}
		out = out[:0]
		for _, run := range runs {
			v, err := r.srv.RunResult(r.ctx, run.ID)
			if err != nil || v.FinishedAt == nil {
				return false
			}
			out = append(out, v)
		}
		return true
	})
	return out
}

// thread returns the thread and its messages.
func (r *rig) thread(id string) (store.Thread, []store.Message) {
	r.t.Helper()
	th, err := r.st.GetThread(r.ctx, id)
	if err != nil {
		r.t.Fatal(err)
	}
	ms, err := r.st.ThreadMessages(r.ctx, id, 0, 0)
	if err != nil {
		r.t.Fatal(err)
	}
	return th, ms
}

// onlyThread returns the one thread of the tenant.
func (r *rig) onlyThread() string {
	r.t.Helper()
	p, err := r.st.ListThreads(r.ctx, store.Page{})
	if err != nil || len(p.Threads) != 1 {
		r.t.Fatalf("threads %v, %v; want one", p.Threads, err)
	}
	return p.Threads[0].ID
}

// settled waits until every message of the thread is done.
func (r *rig) settled(thread string) []store.Message {
	r.t.Helper()
	var ms []store.Message
	testkit.Eventually(r.t, waitFor, func() bool {
		_, ms = r.thread(thread)
		return len(ms) > 0 && !slices.ContainsFunc(ms, func(m store.Message) bool { return m.State != store.MessageDone })
	})
	return ms
}

var callIDs atomic.Int64

// call is a reply that calls a tool with input.
func call(name string, input any) llmtest.Reply {
	b, err := json.Marshal(input)
	if err != nil {
		panic(err)
	}
	return llmtest.ToolCalls(llm.ToolUse{ID: fmt.Sprintf("toolu_%d", callIDs.Add(1)), Name: name, Input: b})
}

// send is a reply that sends a message.
func send(to, text, thread string, wait bool) llmtest.Reply {
	in := map[string]any{"to": to, "text": text}
	if thread != "" {
		in["thread"] = thread
	}
	if wait {
		in["wait"] = true
	}
	return call(agentmsg.MessageToolName, in)
}

// last is the last message of a request: the user's prompt, a delivered
// message, or the results of the tool calls of the turn before.
func last(req llm.Request) llm.Message { return req.Messages[len(req.Messages)-1] }

// toolResult returns the result of the last turn's tool call, if the request
// continues one.
func toolResult(req llm.Request) (llm.ToolResult, bool) {
	for _, b := range last(req).Content {
		if r, ok := b.(llm.ToolResult); ok {
			return r, true
		}
	}
	return llm.ToolResult{}, false
}

// delivered returns the message a request's prompt carries, if it is one.
func delivered(req llm.Request) (agentmsg.Envelope, bool) {
	if _, ok := toolResult(req); ok {
		return agentmsg.Envelope{}, false
	}
	return agentmsg.ParseEnvelope(last(req).Text())
}

// answer is a brain that answers every request with text.
func answer(text string) brain {
	return func(context.Context, llm.Request) llmtest.Reply { return llmtest.Text(text) }
}

// toolResults returns the results of every tool call the model was shown.
func toolResults(reqs []llm.Request) []llm.ToolResult {
	var out []llm.ToolResult
	seen := map[string]bool{}
	for _, req := range reqs {
		for _, m := range req.Messages {
			for _, b := range m.Content {
				if r, ok := b.(llm.ToolResult); ok && !seen[r.ToolUseID] {
					seen[r.ToolUseID] = true
					out = append(out, r)
				}
			}
		}
	}
	return out
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

func memoryAdmin(r *rig) store.MessageAdmin { return memory.MessageAdmin(r.st) }

// skewedClock is the machine's clock moved by an offset that can change: the
// clocks of the replicas that write one store, which never agree exactly.
type skewedClock struct {
	clock.Clock
	offset atomic.Int64 // nanoseconds
}

func (c *skewedClock) Now() time.Time {
	return c.Clock.Now().Add(time.Duration(c.offset.Load()))
}
