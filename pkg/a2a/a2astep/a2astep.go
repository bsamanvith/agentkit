// Package a2astep runs a remote A2A agent as a [flow.Step]: it sends the
// prompt in the flow state to the agent and writes the agent's answer back,
// so a remote agent can be put in sequences, run in parallel and paused for
// approval like any other step.
//
// # Saving and resuming
//
// A remote task can run longer than the process that started it. So a Step
// saves the task's ID, through a [TaskSaver] keyed by the flow run id and the
// step's path ([flow.StepPath]), as soon as the agent has given it, and
// before it waits. When flow runs the step again (after a crash, a failure
// or a cancellation), the step follows the same task instead of starting a
// new one:
//
//   - nothing saved: a new task with the prompt from the state;
//   - a task that is still working, or completed: wait for it (no new
//     message), and write its answer;
//   - a task that failed, was canceled or was rejected: running the step
//     again is a retry, so a new task is started and its ID saved;
//   - a task the agent no longer knows: the step fails with
//     a2a.ErrTaskNotFound rather than send the prompt again, because the
//     first task may have had effects.
//
// The process can still stop between the agent creating the task and the
// step saving its ID; a step run again then starts a second task.
//
// A task that needs input or authentication fails the step with
// [ErrNeedsInput]: a flow step has no one to ask. The task ID is saved, so a
// person can answer the task elsewhere and resume the flow.
package a2astep

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/bsamanvith/agentkit/pkg/a2a"
	"github.com/bsamanvith/agentkit/pkg/a2a/client"
	"github.com/bsamanvith/agentkit/pkg/flow"
)

var (
	// ErrInvalidConfig means [New] was given a missing name, agent, saver
	// or state key.
	ErrInvalidConfig = errors.New("invalid A2A step configuration")
	// ErrNoRun means the step ran outside flow.Run and flow.Resume.
	ErrNoRun = errors.New("A2A step must run under flow.Run or flow.Resume")
	// ErrNeedsInput means the remote task waits for input or
	// authentication.
	ErrNeedsInput = errors.New("the remote task needs input")
)

// TaskError is a remote task that ended without completing: failed,
// canceled or rejected. Message is the agent's status message.
type TaskError struct {
	TaskID  string
	State   string
	Message string
}

func (e *TaskError) Error() string {
	s := fmt.Sprintf("remote task %s ended %s", e.TaskID, e.State)
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

// Agent is a remote agent: a [*client.Client], or anything with the same
// Start and Wait.
type Agent interface {
	Start(ctx context.Context, msg a2a.Message) (a2a.Task, error)
	Wait(ctx context.Context, id string) (a2a.Task, error)
	GetTask(ctx context.Context, id string) (a2a.Task, error)
}

var _ Agent = (*client.Client)(nil)

// TaskSaver keeps the remote task ID of each A2A step of each flow run.
// Implementations must be safe for concurrent use and honor ctx.
type TaskSaver interface {
	// Load returns the task ID saved for the step at path of run runID, ""
	// when none is.
	Load(ctx context.Context, runID, path string) (string, error)
	// Save sets it, replacing an earlier one.
	Save(ctx context.Context, runID, path, taskID string) error
}

// Keys are the state keys a [Step] reads and writes.
type Keys struct {
	// Input holds the prompt, a string.
	Input string
	// Output receives the step's [Output].
	Output string
}

// Output is what a [Step] writes to the state when the remote task has
// completed. Answer is third-party data.
type Output struct {
	TaskID    string         `json:"task_id,omitempty"`
	ContextID string         `json:"context_id,omitempty"`
	Answer    string         `json:"answer"`
	Artifacts []a2a.Artifact `json:"artifacts,omitempty"`
}

// Step is a [flow.Step] that runs a remote agent. See the package
// documentation.
type Step struct {
	name  string
	agent Agent
	saver TaskSaver
	keys  Keys
}

// New returns a Step called name that sends the prompt under keys.Input to
// agent, saves the task's ID through saver, and writes an [Output] under
// keys.Output.
func New(name string, agent Agent, saver TaskSaver, keys Keys) (*Step, error) {
	switch {
	case name == "":
		return nil, fmt.Errorf("%w: empty name", ErrInvalidConfig)
	case agent == nil || (reflect.ValueOf(agent).Kind() == reflect.Pointer && reflect.ValueOf(agent).IsNil()):
		return nil, fmt.Errorf("%w: %s: nil agent", ErrInvalidConfig, name)
	case saver == nil:
		return nil, fmt.Errorf("%w: %s: nil task saver", ErrInvalidConfig, name)
	case keys.Input == "" || keys.Output == "":
		return nil, fmt.Errorf("%w: %s: input and output keys are required", ErrInvalidConfig, name)
	case keys.Input == keys.Output:
		return nil, fmt.Errorf("%w: %s: input and output key are both %q", ErrInvalidConfig, name, keys.Input)
	}
	return &Step{name: name, agent: agent, saver: saver, keys: keys}, nil
}

// Name implements flow.Namer.
func (s *Step) Name() string { return s.name }

// Run implements [flow.Step].
func (s *Step) Run(ctx context.Context, st *flow.State) error {
	runID, path, ok := flow.StepPath(ctx)
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoRun, s.name)
	}
	id, err := s.saver.Load(ctx, runID, path)
	if err != nil {
		return fmt.Errorf("load the task of %s: %w", path, err)
	}
	var t a2a.Task
	if id != "" {
		if t, err = s.agent.GetTask(ctx, id); err != nil {
			return fmt.Errorf("%s: the saved task %s: %w", path, id, err)
		}
		if failed(t.Status.State) {
			id = "" // a retry
		}
	}
	if id == "" {
		prompt, err := flow.Get[string](st, s.keys.Input)
		if err != nil {
			return fmt.Errorf("input %q: %w", s.keys.Input, err)
		}
		if t, err = s.agent.Start(ctx, client.UserText(prompt)); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if t.ID != "" {
			if err := s.saver.Save(ctx, runID, path, t.ID); err != nil {
				return fmt.Errorf("save the task of %s: %w", path, err)
			}
		}
	}
	if t.ID != "" && !client.Settled(t.Status.State) {
		if t, err = s.agent.Wait(ctx, t.ID); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	switch state := t.Status.State; {
	case state == a2a.StateCompleted:
	case failed(state):
		return &TaskError{TaskID: t.ID, State: state, Message: client.MessageText(t.Status.Message)}
	default:
		return fmt.Errorf("%w: %s: task %s is %s: %s", ErrNeedsInput, path, t.ID, state, client.MessageText(t.Status.Message))
	}
	return flow.Set(st, s.keys.Output, Output{TaskID: t.ID, ContextID: t.ContextID, Answer: client.Answer(t), Artifacts: t.Artifacts})
}

func failed(s string) bool {
	return s == a2a.StateFailed || s == a2a.StateCanceled || s == a2a.StateRejected
}

// MemorySaver is an in-process [TaskSaver], for tests and short-lived runs.
type MemorySaver struct {
	mu sync.Mutex
	m  map[[2]string]string
}

// NewMemorySaver returns an empty MemorySaver.
func NewMemorySaver() *MemorySaver { return &MemorySaver{m: map[[2]string]string{}} }

// Load implements [TaskSaver].
func (s *MemorySaver) Load(ctx context.Context, runID, path string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[[2]string{runID, path}], nil
}

// Save implements [TaskSaver].
func (s *MemorySaver) Save(ctx context.Context, runID, path, taskID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[[2]string{runID, path}] = taskID
	return nil
}
