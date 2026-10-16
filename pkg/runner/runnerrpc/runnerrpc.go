// Package runnerrpc puts agents on the wire: [Register] serves named
// [runner.Agent] values (harnesses: a model and its tools) on an
// [rpc.Server], and a [Client]'s agents implement runner.Agent by calling
// them. The runner (pkg/runner) keeps a run's log, lease and recovery where
// the store is; only the turn loop moves, so an agent API can run its agents
// in a runner container, and a runner pool (E20) can be one.
//
// A remote agent streams the same events as the local one, and fails with
// the same errors: a failed run is a [*harness.RunError] with what the run
// had done, matching the same harness and llm errors (llm.IsRetryable
// included), and a refusal is a [*harness.RefusalError].
package runnerrpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"maps"
	"slices"
	"time"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/rpc"
	"github.com/bsamanvith/agentkit/pkg/runner"
)

// Service is the service's name: a component serving it mounts its server at
// rpc.PathPrefix + Service + "/".
const Service = "runner"

// ErrNoAgent means the server runs no agent of that name.
var ErrNoAgent = errors.New("agent not run here")

// The service's methods.
var (
	agentsMethod = rpc.Unary[struct{}, agentsResp]{Name: Service + "/Agents"}
	runMethod    = rpc.Stream[runReq, event]{Name: Service + "/Run", Errors: codes}
)

// codes are the errors a run's caller tells apart: the harness's, the
// model's (retries depend on them) and this package's.
var codes = []rpc.Code{
	{Name: "runner.no_agent", Err: ErrNoAgent},
	{Name: "harness.invalid_input", Err: harness.ErrInvalidInput},
	{Name: "harness.invalid_config", Err: harness.ErrInvalidConfig},
	{Name: "harness.budget_exceeded", Err: harness.ErrBudgetExceeded},
	{Name: "harness.max_tokens", Err: harness.ErrMaxTokens},
	{Name: "harness.refused", Err: harness.ErrRefused},
	{Name: "harness.unexpected_stop", Err: harness.ErrUnexpectedStop},
	{Name: "llm.rate_limited", Err: llm.ErrRateLimited},
	{Name: "llm.overloaded", Err: llm.ErrOverloaded},
	{Name: "llm.invalid_request", Err: llm.ErrInvalidRequest},
	{Name: "llm.auth", Err: llm.ErrAuth},
	{Name: "llm.context_too_long", Err: llm.ErrContextTooLong},
}

type agentsResp struct {
	Agents []string `json:"agents"`
}

type runReq struct {
	Agent string `json:"agent"`
	// Message is nil to resume a transcript without a new message.
	Message    *llm.Message  `json:"message,omitempty"`
	Transcript []llm.Message `json:"transcript,omitempty"`
	At         time.Time     `json:"at,omitzero"`
}

// event is a [harness.Event] on the wire: only the fields its kind sets.
type event struct {
	Kind       harness.EventKind     `json:"kind"`
	Turn       int                   `json:"turn,omitempty"`
	At         time.Time             `json:"at,omitzero"`
	Text       string                `json:"text,omitempty"`
	Block      int                   `json:"block,omitempty"`
	Call       int                   `json:"call,omitempty"`
	ToolUse    json.RawMessage       `json:"tool_use,omitempty"`
	ToolResult json.RawMessage       `json:"tool_result,omitempty"`
	Duration   time.Duration         `json:"duration,omitempty"`
	Prompt     *harness.TimedMessage `json:"prompt,omitempty"`
	TurnRecord *harness.TurnRecord   `json:"turn_record,omitempty"`
	Result     *result               `json:"result,omitempty"`
}

// result is a [harness.Result] on the wire. Final is derived on the way back
// for a partial result (see [harness.NewRunError]).
type result struct {
	Transcript []llm.Message  `json:"transcript"`
	Final      *llm.Message   `json:"final,omitempty"`
	Usage      llm.Usage      `json:"usage"`
	Turns      int            `json:"turns"`
	Record     harness.Record `json:"record"`
}

// failure is what a failed run's error carries besides its codes.
type failure struct {
	// Partial is what a run that failed as a *harness.RunError had done.
	Partial *result  `json:"partial,omitempty"`
	Refusal *refusal `json:"refusal,omitempty"`
}

type refusal struct {
	Category    string `json:"category,omitempty"`
	Explanation string `json:"explanation,omitempty"`
}

// Register serves agents on s, by name. Each call of Run runs its agent's
// Run on the request's input and streams the events back.
func Register(s *rpc.Server, agents map[string]runner.Agent) error {
	for name, a := range agents {
		if a == nil {
			return fmt.Errorf("runnerrpc: agent %q is nil", name)
		}
	}
	agents = maps.Clone(agents)
	names := slices.Sorted(maps.Keys(agents))
	agentsMethod.Handle(s, func(context.Context, struct{}) (agentsResp, error) {
		return agentsResp{Agents: slices.Clone(names)}, nil
	})
	runMethod.Handle(s, func(ctx context.Context, r runReq) iter.Seq2[event, error] {
		return func(yield func(event, error) bool) {
			a := agents[r.Agent]
			if a == nil {
				yield(event{}, fmt.Errorf("%w: %q", ErrNoAgent, r.Agent))
				return
			}
			in := harness.Input{Transcript: r.Transcript, At: r.At}
			if r.Message != nil {
				in.Message = *r.Message
			}
			for ev, err := range a.Run(ctx, in) {
				if err != nil {
					yield(event{}, encodeFailure(err))
					return
				}
				w, err := toWire(ev)
				if err != nil {
					yield(event{}, err)
					return
				}
				if !yield(w, nil) {
					return
				}
			}
		}
	})
	return nil
}

// Client calls a server's agents. It is safe for concurrent use.
type Client struct{ conn rpc.Conn }

// NewClient returns a client that calls through conn.
func NewClient(conn rpc.Conn) *Client { return &Client{conn: conn} }

// Agents lists the names of the server's agents, sorted.
func (c *Client) Agents(ctx context.Context) ([]string, error) {
	resp, err := agentsMethod.Call(ctx, c.conn, struct{}{})
	return resp.Agents, err
}

// Agent returns the server's agent name. It does not call the server: an
// agent it does not run fails each run with [ErrNoAgent].
func (c *Client) Agent(name string) runner.Agent { return &agent{c: c, name: name} }

type agent struct {
	c    *Client
	name string
}

var _ runner.Agent = (*agent)(nil)

func (a *agent) Run(ctx context.Context, in harness.Input) iter.Seq2[harness.Event, error] {
	return func(yield func(harness.Event, error) bool) {
		req := runReq{Agent: a.name, Transcript: in.Transcript, At: in.At}
		if in.Message.Role != "" || len(in.Message.Content) > 0 {
			m := in.Message
			req.Message = &m
		}
		for w, err := range runMethod.Call(ctx, a.c.conn, req) {
			if err != nil {
				yield(harness.Event{}, decodeFailure(err))
				return
			}
			ev, err := fromWire(w)
			if err != nil {
				yield(harness.Event{}, fmt.Errorf("agent %q: %w: %w", a.name, rpc.ErrUnavailable, err))
				return
			}
			if !yield(ev, nil) {
				return
			}
		}
	}
}

func toWire(ev harness.Event) (event, error) {
	w := event{
		Kind: ev.Kind, Turn: ev.Turn, At: ev.At, Text: ev.Text, Block: ev.Block, Call: ev.Call,
		Duration: ev.Duration, Prompt: ev.Prompt, TurnRecord: ev.TurnRecord,
	}
	var err error
	switch ev.Kind {
	case harness.EventToolStart:
		w.ToolUse, err = json.Marshal(ev.ToolUse)
	case harness.EventToolDone:
		w.ToolResult, err = json.Marshal(ev.ToolResult)
	case harness.EventRunDone:
		if ev.Result != nil {
			r := resultToWire(*ev.Result)
			w.Result = &r
		}
	}
	return w, err
}

func fromWire(w event) (harness.Event, error) {
	ev := harness.Event{
		Kind: w.Kind, Turn: w.Turn, At: w.At, Text: w.Text, Block: w.Block, Call: w.Call,
		Duration: w.Duration, Prompt: w.Prompt, TurnRecord: w.TurnRecord,
	}
	if w.ToolUse != nil {
		b, err := llm.UnmarshalBlock(w.ToolUse)
		u, ok := b.(llm.ToolUse)
		if err != nil || !ok {
			return ev, fmt.Errorf("tool use: %w", errors.Join(err, errors.New("not a tool use")))
		}
		ev.ToolUse = u
	}
	if w.ToolResult != nil {
		b, err := llm.UnmarshalBlock(w.ToolResult)
		r, ok := b.(llm.ToolResult)
		if err != nil || !ok {
			return ev, fmt.Errorf("tool result: %w", errors.Join(err, errors.New("not a tool result")))
		}
		ev.ToolResult = r
	}
	if w.Result != nil {
		r := w.Result.toResult()
		ev.Result = &r
	}
	return ev, nil
}

func resultToWire(r harness.Result) result {
	w := result{Transcript: r.Transcript, Usage: r.Usage, Turns: r.Turns, Record: r.Record}
	if r.Final.Role != "" {
		f := r.Final
		w.Final = &f
	}
	return w
}

func (w result) toResult() harness.Result {
	r := harness.Result{Transcript: w.Transcript, Usage: w.Usage, Turns: w.Turns, Record: w.Record}
	if w.Final != nil {
		r.Final = *w.Final
	}
	return r
}

// encodeFailure is a run's error as the server sends it: the cause, with
// what the run had done and the details of a refusal.
func encodeFailure(err error) error {
	var f failure
	if re, ok := errors.AsType[*harness.RunError](err); ok {
		p := resultToWire(re.Partial())
		f.Partial = &p
		err = re.Err
	}
	if r, ok := errors.AsType[*harness.RefusalError](err); ok {
		f.Refusal = &refusal{Category: r.Category, Explanation: r.Explanation}
	}
	if f.Partial == nil && f.Refusal == nil {
		return err
	}
	return rpc.WithData(err, f)
}

// decodeFailure turns the error of a remote run back into what the local
// run would have returned.
func decodeFailure(err error) error {
	we, ok := errors.AsType[*rpc.Error](err)
	var f failure
	if !ok || !we.Data(&f) {
		return err
	}
	cause := err
	if f.Refusal != nil {
		cause = &harness.RefusalError{Category: f.Refusal.Category, Explanation: f.Refusal.Explanation}
	}
	if f.Partial == nil {
		return cause
	}
	return harness.NewRunError(cause, f.Partial.toResult())
}
