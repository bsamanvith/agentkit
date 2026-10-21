package tool

import (
	"slices"
	"strings"
)

// InterruptError is how a tool asks a person something: a call that returns
// it (directly or wrapped) did not fail, it waits for an answer. A harness
// that may wait suspends the run, and the person's answer becomes the call's
// result; one that may not (an autonomous run) shows the model an error
// instead. Only a tool that declares [Metadata.Interrupts] should return it,
// so that runs that never wait can leave the tool out.
//
// It crosses only an in-process registry: a remote one (package toolrpc)
// reports it as the tool's failure.
type InterruptError struct {
	// Prompt is the question.
	Prompt string
	// Choices are the answers it accepts; empty means any text.
	Choices []string
}

// Interrupt returns an [*InterruptError] asking prompt, answered with one of
// choices, or any text when there are none.
func Interrupt(prompt string, choices ...string) error {
	return &InterruptError{Prompt: prompt, Choices: slices.Clone(choices)}
}

func (e *InterruptError) Error() string {
	msg := "the tool asks a person: " + e.Prompt
	if len(e.Choices) > 0 {
		msg += " (" + strings.Join(e.Choices, ", ") + ")"
	}
	return msg
}
