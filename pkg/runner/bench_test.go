package runner_test

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/memory"
	"github.com/bsamanvith/agentkit/pkg/store/sqlite"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// benchModel answers a prompt "t<n>:..." with n echo tool calls, one per
// turn, then a text answer. It keeps no state, so any number of runs share it,
// and it costs what the scripted fake costs: the runner's overhead is the
// difference between a run through the runner and the same run through the
// harness alone.
type benchModel struct {
	tb      testing.TB
	latency time.Duration // per call, for load tests
}

func (m benchModel) Generate(ctx context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	prompt, turn := "", 0
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if msg := req.Messages[i]; msg.Role == llm.RoleUser && strings.HasPrefix(msg.Text(), "t") {
			prompt = msg.Text()
			turn = (len(req.Messages) - 1 - i) / 2
			break
		}
	}
	tools, _ := strconv.Atoi(strings.TrimPrefix(prompt[:max(strings.IndexByte(prompt, ':'), 0)], "t"))
	usage := llm.Usage{InputTokens: 100, OutputTokens: 20}
	reply := llmtest.Text("answer to " + prompt).WithUsage(usage)
	if turn < tools {
		reply = llmtest.ToolCalls(llm.ToolUse{
			ID: fmt.Sprintf("call-%d", turn), Name: "echo", Input: json.RawMessage(`{"s":"x"}`),
		}).WithUsage(usage)
	}
	if m.latency > 0 {
		select {
		case <-time.After(m.latency):
		case <-ctx.Done():
		}
	}
	return llmtest.NewFake(m.tb, reply).Generate(ctx, req)
}

// benchEnv is a runner over a fresh store of the given backend, with the
// bench model and an echo tool. A backend named with "+read" declares the
// tool read-only, so the runner keeps a run's turns and stores them in one
// write; otherwise it stores each turn as it completes.
type benchEnv struct {
	r      *runner.Runner
	s      runner.Store
	admin  store.Admin
	ctx    context.Context
	agents runner.AgentFactory
}

func newBenchEnv(tb testing.TB, backend string, latency time.Duration, opts ...runner.Option) *benchEnv {
	tb.Helper()
	acme, err := tenancy.NewTenant("acme")
	if err != nil {
		tb.Fatal(err)
	}
	e := &benchEnv{ctx: tenancy.With(context.Background(), acme)}
	backend, read := strings.CutSuffix(backend, "+read")
	var toolOpts []tool.FuncOption
	if read {
		toolOpts = append(toolOpts, tool.WithMetadata(tool.Metadata{Kind: tool.KindRead}))
	}
	switch backend {
	case "memory":
		m := memory.New()
		e.s, e.admin = m, memory.Admin(m)
	case "sqlite":
		s, err := sqlite.Open(context.Background(), filepath.Join(tb.TempDir(), "bench.db"))
		if err != nil {
			tb.Fatal(err)
		}
		tb.Cleanup(func() { _ = s.Close() })
		e.s, e.admin = s, sqlite.Admin(s)
	default:
		tb.Fatalf("unknown backend %q", backend)
	}
	echo, err := tool.Func("echo", "Echo s.", func(_ context.Context, in struct {
		S string `json:"s"`
	},
	) (string, error) {
		return "echo " + in.S, nil
	}, toolOpts...)
	if err != nil {
		tb.Fatal(err)
	}
	tools, err := tool.NewRegistry([]tool.Tool{echo})
	if err != nil {
		tb.Fatal(err)
	}
	model := benchModel{tb: tb, latency: latency}
	e.agents = func(context.Context, runner.Placement) (runner.Agent, error) { return harness.New(model, tools) }
	if e.r, err = runner.New(e.s, e.agents, append([]runner.Option{runner.WithClock(clock.Real())}, opts...)...); err != nil {
		tb.Fatal(err)
	}
	return e
}

// run sends one message and drains its execution.
func (e *benchEnv) run(tb testing.TB, conv string, tools int) {
	x, err := e.r.Run(e.ctx, conv, harness.UserText(fmt.Sprintf("t%d:hi", tools)))
	if err != nil {
		tb.Fatal(err)
	}
	done, err := x.Wait()
	if err != nil || done.Run.Status != store.StatusCompleted {
		tb.Fatalf("run ended %v, %v", done.Run.Status, err)
	}
}

// BenchmarkRun sends one message to a new conversation per operation, from
// parallel callers: a run with 0 or 3 tool turns, through the harness alone
// (no persistence: the baseline) and through the runner on each backend.
// The runner's cost per run is the difference with the baseline.
//
// Run with -benchtime=1000x: the memory store's Create scans every run of the
// tenant, so its numbers grow with the number of operations.
func BenchmarkRun(b *testing.B) {
	for _, tools := range []int{0, 3} {
		b.Run(fmt.Sprintf("tools=%d/harness", tools), func(b *testing.B) {
			e := newBenchEnv(b, "memory", 0)
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					h, err := e.agents(e.ctx, runner.Placement{})
					if err != nil {
						b.Fatal(err)
					}
					for _, err := range h.Run(e.ctx, harness.Input{Message: harness.UserText(fmt.Sprintf("t%d:hi", tools))}) {
						if err != nil {
							b.Fatal(err)
						}
					}
				}
			})
		})
		for _, backend := range []string{"memory", "sqlite", "sqlite+read"} {
			if tools == 0 && backend == "sqlite+read" {
				continue // no tools: the same as sqlite
			}
			b.Run(fmt.Sprintf("tools=%d/%s", tools, backend), func(b *testing.B) {
				e := newBenchEnv(b, backend, 0)
				var n atomic.Int64
				b.ReportAllocs()
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						e.run(b, fmt.Sprint("c", n.Add(1)), tools)
					}
				})
			})
		}
	}
}

// BenchmarkRunInLongConversation sends one message per operation to a
// conversation that already holds 200 messages (100 runs), copied in before
// the operation so every operation sees the same length. The harness baseline
// gets the same 200 messages as its transcript: every run hands the model the
// whole conversation, with or without the runner. The runner's own cost is
// the difference, and BenchmarkRun's is the same for an empty conversation.
//
// cold is the conversation's first run on this process, which loads its
// context from the store; warm has it cached, as for the next message.
func BenchmarkRunInLongConversation(b *testing.B) {
	for _, name := range []string{"harness", "memory/cold", "memory/warm", "sqlite/cold", "sqlite/warm"} {
		backend, cache, _ := strings.Cut(name, "/")
		b.Run(name, func(b *testing.B) {
			kind := backend
			if backend == "harness" {
				kind = "memory" // only to build the template conversation
			}
			e := newBenchEnv(b, kind, 0)
			for range 100 {
				e.run(b, "template", 0)
			}
			page, err := e.s.History(e.ctx, "template", store.Page{Order: store.Ascending, Limit: store.MaxPageLimit})
			if err != nil || len(page.Entries) != 200 {
				b.Fatalf("template has %d entries, %v", len(page.Entries), err)
			}
			tr, _, err := e.r.Context(e.ctx, "template")
			if err != nil {
				b.Fatal(err)
			}
			n := 0
			b.ReportAllocs()
			for b.Loop() {
				if backend == "harness" {
					h, err := e.agents(e.ctx, runner.Placement{})
					if err != nil {
						b.Fatal(err)
					}
					for _, err := range h.Run(e.ctx, harness.Input{Transcript: tr, Message: harness.UserText("t0:hi")}) {
						if err != nil {
							b.Fatal(err)
						}
					}
					continue
				}
				b.StopTimer()
				n++
				conv := fmt.Sprint("long", n)
				for _, en := range page.Entries {
					if err := e.s.Append(e.ctx, conv, en); err != nil {
						b.Fatal(err)
					}
				}
				if cache == "warm" {
					if _, _, err := e.r.Context(e.ctx, conv); err != nil {
						b.Fatal(err)
					}
				}
				b.StartTimer()
				e.run(b, conv, 0)
			}
		})
	}
}

// BenchmarkContext reads a 200-message (200-entry) conversation: through the
// runner's cache (hit) and straight from the store (cold).
func BenchmarkContext(b *testing.B) {
	for _, backend := range []string{"memory", "sqlite"} {
		e := newBenchEnv(b, backend, 0)
		for range 100 {
			e.run(b, "c", 0)
		}
		b.Run(backend+"/cached", func(b *testing.B) {
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if tr, _, err := e.r.Context(e.ctx, "c"); err != nil || len(tr) != 200 {
						b.Fatalf("Context = %d messages, %v", len(tr), err)
					}
				}
			})
		})
		b.Run(backend+"/store", func(b *testing.B) {
			b.ReportAllocs()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if v, err := e.s.Context(e.ctx, "c"); err != nil || len(v.Entries) != 200 {
						b.Fatalf("store Context = %d entries, %v", len(v.Entries), err)
					}
				}
			})
		})
	}
}

// BenchmarkRecover resumes 100 runs of a dead process per operation, each
// with its prompt stored and its lease expired, in one Recover pass with the
// default workers, and reports runs per second.
func BenchmarkRecover(b *testing.B) {
	const runs = 100
	for _, backend := range []string{"memory", "sqlite"} {
		b.Run(backend, func(b *testing.B) {
			e := newBenchEnv(b, backend, 0)
			rc, err := runner.NewRecovery(e.r, e.admin, runner.WithBatch(runs))
			if err != nil {
				b.Fatal(err)
			}
			round := 0
			b.ReportAllocs()
			for b.Loop() {
				b.StopTimer()
				round++
				for i := range runs {
					e.abandon(b, fmt.Sprintf("r%d-%d", round, i))
				}
				b.StartTimer()
				rep, err := rc.Recover(e.ctx)
				if err != nil || rep.Resumed != runs {
					b.Fatalf("Recover = %+v, %v", rep, err)
				}
			}
			b.ReportMetric(float64(runs*b.N)/b.Elapsed().Seconds(), "runs/s")
		})
	}
}

// abandon leaves a run as a process that died after storing its prompt
// leaves it: running, the prompt stored, the lease expired.
func (e *benchEnv) abandon(tb testing.TB, conv string) {
	id := "run-" + conv
	if _, err := e.s.Create(e.ctx, id, conv); err != nil {
		tb.Fatal(err)
	}
	if _, err := e.s.Claim(e.ctx, id, "dead", time.Nanosecond); err != nil {
		tb.Fatal(err)
	}
	rec, err := json.Marshal(runner.Record{Prompt: &runner.PromptRecord{At: time.Now()}})
	if err != nil {
		tb.Fatal(err)
	}
	if err := e.s.Append(e.ctx, conv, store.Entry{
		Seq: 1, Kind: store.KindPrompt, RunID: id, Record: rec,
		Messages: []llm.Message{llm.User(llm.Text{Text: "t1:hi"})},
	}); err != nil {
		tb.Fatal(err)
	}
}
