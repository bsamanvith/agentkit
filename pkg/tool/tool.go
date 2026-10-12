package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"runtime/debug"
	"slices"
)

// Tool is a single callable tool.
//
// Implementations must be safe for concurrent use and must return promptly
// once ctx is done.
type Tool interface {
	// Definition describes the tool. Callers may modify the returned value
	// without affecting the tool.
	Definition() Definition
	// Invoke runs the tool with JSON input and returns JSON output.
	Invoke(ctx context.Context, input json.RawMessage) (json.RawMessage, error)
}

// Definition describes a tool to models and clients.
type Definition struct {
	// Name identifies the tool; it must match ^[a-zA-Z0-9_-]{1,64}$, the rule
	// shared by MCP, Anthropic and OpenAI-compatible APIs.
	Name string `json:"name"`
	// Description tells the model what the tool does and when to use it.
	Description string `json:"description,omitempty"`
	// InputSchema is a JSON Schema for the input. It always describes an object.
	InputSchema json.RawMessage `json:"inputSchema"`
	// OutputSchema is a JSON Schema for the output, if the output is an object.
	OutputSchema json.RawMessage `json:"outputSchema,omitempty"`
	// Metadata says what the tool does (kind, risk, tags, display). It is
	// optional; [Metadata.Class] gives an unclassified tool its default class.
	Metadata
}

// Clone returns a deep copy of d.
func (d Definition) Clone() Definition {
	d.InputSchema = bytes.Clone(d.InputSchema)
	d.OutputSchema = bytes.Clone(d.OutputSchema)
	d.Tags = slices.Clone(d.Tags)
	return d
}

var (
	// ErrInvalidDefinition reports a tool that cannot be built or registered:
	// a bad name, input/output types with no usable JSON Schema, or metadata
	// that breaks its rules (an unknown kind or risk, a malformed tag).
	ErrInvalidDefinition = errors.New("invalid tool definition")
	// ErrInvalidInput reports input that is not valid JSON or does not match
	// the tool's input schema. The tool was not run.
	ErrInvalidInput = errors.New("invalid tool input")
	// ErrToolFailed reports that the tool ran and returned an error (or
	// panicked). The tool's own error is still reachable with errors.Is/As.
	ErrToolFailed = errors.New("tool failed")
	// ErrPanic is joined with ErrToolFailed when the tool panicked. The cause
	// is a *PanicError carrying the stack.
	ErrPanic = errors.New("tool panicked")
	// ErrNotFound reports a call to a tool name the registry does not have.
	ErrNotFound = errors.New("tool not found")
	// ErrDuplicateName reports two tools with the same name in one registry.
	ErrDuplicateName = errors.New("duplicate tool name")
)

// Error is returned for a failed call. It matches its Kind
// ([ErrInvalidInput] or [ErrToolFailed]) and its cause with errors.Is.
type Error struct {
	Tool string // tool name
	Kind error  // ErrInvalidInput or ErrToolFailed
	Err  error  // underlying cause
}

func (e *Error) Error() string {
	return fmt.Sprintf("tool %q: %v: %v", e.Tool, e.Kind, e.Err)
}

// Unwrap returns both the kind and the cause, so errors.Is matches either.
func (e *Error) Unwrap() []error { return []error{e.Kind, e.Err} }

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// checkDefinition validates what every consumer (models, MCP clients,
// policies) relies on: a valid name, an input schema that is a JSON object,
// and metadata that follows its rules.
func checkDefinition(d Definition) error {
	if err := checkName(d.Name); err != nil {
		return err
	}
	var schema map[string]any
	if err := json.Unmarshal(d.InputSchema, &schema); err != nil || schema == nil {
		return fmt.Errorf("%w: tool %q: input schema must be a JSON object", ErrInvalidDefinition, d.Name)
	}
	if err := checkMetadata(d.Metadata); err != nil {
		return fmt.Errorf("%w: tool %q: %w", ErrInvalidDefinition, d.Name, err)
	}
	return nil
}

func checkName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("%w: name %q must match %s", ErrInvalidDefinition, name, namePattern)
	}
	return nil
}

// PanicError is the cause of a call that panicked. Its message holds only the
// panic value, so it is safe to show to a model or an MCP client; the stack is
// kept separately for logs (use errors.As).
type PanicError struct {
	Value any
	Stack []byte
}

func (e *PanicError) Error() string { return fmt.Sprintf("%v: %v", ErrPanic, e.Value) }

// Is reports whether target is [ErrPanic].
func (e *PanicError) Is(target error) bool { return target == ErrPanic }

// recovered converts a recovered panic value into an error matching
// ErrToolFailed and ErrPanic.
func recovered(name string, v any) error {
	return &Error{Tool: name, Kind: ErrToolFailed, Err: &PanicError{Value: v, Stack: debug.Stack()}}
}

// contextError reports that the call stopped because ctx is done.
func contextError(name string, err error) error {
	return fmt.Errorf("tool %q: %w", name, err)
}
