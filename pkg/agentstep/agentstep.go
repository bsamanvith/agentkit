package agentstep

import (
	"context"
	"errors"
	"fmt"
	"iter"

	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
)

var (
	// ErrInvalidConfig means [New] was given a missing name, agent, saver or
	// state key.
	ErrInvalidConfig = errors.New("invalid agent step configuration")
	// ErrNoRun means the step ran outside [flow.Run] and [flow.Resume], so it
	// has no run id and path to save its transcript under.
	ErrNoRun = errors.New("agent step must run under flow.Run or flow.Resume")
)

// Agent runs an agent's turns on an input: a [*harness.Harness], or anything
// with the same Run, such as a runner.Agent (a remote runner or a pool's
// agent), so that a step can run where the server's runs do.
type Agent interface {
	Run(ctx context.Context, in harness.Input) iter.Seq2[harness.Event, error]
}

var _ Agent = (*harness.Harness)(nil)

// Keys are the state keys a [Step] reads and writes.
type Keys struct {
	// Input holds the prompt, a string.
	Input string
	// Output receives the step's [Output].
	Output string
}

// Output is what a [Step] writes to the state when its agent has answered.
type Output struct {
	// Answer is the text of the model's last message.
	Answer string `json:"answer"`
	// Transcript is the whole conversation, the prompt first.
	Transcript harness.Transcript `json:"transcript"`
}

// Option configures [New].
type Option func(*Step)

// WithObserver sets a function that receives every harness event of the step's
// runs, with the step's path. It is called on the goroutine running the step,
// after the event's messages have been saved; steps in a [flow.Parallel] call
// it concurrently, so it must be safe for concurrent use.
func WithObserver(fn func(path string, ev harness.Event)) Option {
	return func(s *Step) { s.observe = fn }
}

// Step is a [flow.Step] that runs an agent. See the package documentation.
type Step struct {
	name    string
	agent   Agent
	saver   TranscriptSaver
	keys    Keys
	observe func(string, harness.Event)
}

// New returns a Step called name that runs agent on the prompt under keys.Input,
// saves its transcript through saver after every turn, and writes its
// [Output] under keys.Output. It fails with [ErrInvalidConfig] for a missing
// argument, an empty key, or the same key for input and output.
func New(name string, agent Agent, saver TranscriptSaver, keys Keys, opts ...Option) (*Step, error) {
	h, isHarness := agent.(*harness.Harness)
	switch {
	case name == "":
		return nil, fmt.Errorf("%w: empty name", ErrInvalidConfig)
	case agent == nil || isHarness && h == nil:
		return nil, fmt.Errorf("%w: %s: nil agent", ErrInvalidConfig, name)
	case saver == nil:
		return nil, fmt.Errorf("%w: %s: nil transcript saver", ErrInvalidConfig, name)
	case keys.Input == "" || keys.Output == "":
		return nil, fmt.Errorf("%w: %s: input and output keys are required", ErrInvalidConfig, name)
	case keys.Input == keys.Output:
		return nil, fmt.Errorf("%w: %s: input and output key are both %q", ErrInvalidConfig, name, keys.Input)
	}
	s := &Step{name: name, agent: agent, saver: saver, keys: keys}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// Name implements [flow.Namer].
func (s *Step) Name() string { return s.name }

// Run implements [flow.Step]: it starts the agent, or continues it from the
// transcript saved by an earlier attempt, and writes its [Output].
func (s *Step) Run(ctx context.Context, st *flow.State) error {
	runID, path, ok := flow.StepPath(ctx)
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoRun, s.name)
	}
	saved, err := s.saver.Load(ctx, runID, path)
	if err != nil {
		return fmt.Errorf("load transcript of %s: %w", path, err)
	}

	var in harness.Input
	switch {
	case len(saved) == 0:
		prompt, err := flow.Get[string](st, s.keys.Input)
		if err != nil {
			return fmt.Errorf("input %q: %w", s.keys.Input, err)
		}
		in.Message = harness.UserText(prompt)
	case saved[len(saved)-1].Role == llm.RoleUser:
		in.Transcript = saved
	default:
		// Only an answering turn leaves the transcript on an assistant
		// message: the run finished before flow saved its checkpoint.
		if err := saved.Validate(); err != nil {
			return fmt.Errorf("saved transcript of %s: %w", path, err)
		}
		return s.write(st, saved)
	}

	var res *harness.Result
	for ev, err := range s.agent.Run(ctx, in) {
		if err != nil {
			return err
		}
		var msgs []llm.Message
		switch ev.Kind {
		case harness.EventRunStart:
			if ev.Prompt != nil {
				msgs = []llm.Message{ev.Prompt.Message}
			}
		case harness.EventTurnDone:
			if stop := ev.TurnRecord.Stop.Kind; stop == llm.StopToolUse || stop == llm.StopEndTurn {
				for _, m := range ev.TurnRecord.Messages {
					msgs = append(msgs, m.Message)
				}
			}
		case harness.EventRunDone:
			res = ev.Result
		}
		if len(msgs) > 0 {
			if err := s.saver.Append(ctx, runID, path, msgs); err != nil {
				return fmt.Errorf("save transcript of %s: %w", path, err)
			}
		}
		if s.observe != nil {
			s.observe(path, ev)
		}
	}
	if res == nil {
		return fmt.Errorf("%s: harness run ended without a result", path)
	}
	return s.write(st, res.Transcript)
}

func (s *Step) write(st *flow.State, t harness.Transcript) error {
	return flow.Set(st, s.keys.Output, Output{Answer: t[len(t)-1].Text(), Transcript: t})
}
