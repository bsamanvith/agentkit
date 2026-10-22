package coretools

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// UserType is the type of the [User] factory.
const UserType = "user"

// AskUserName is the name of the [User] factory's tool.
const AskUserName = "ask_user"

// Limits of ask_user's input.
const (
	maxQuestion = 2000
	maxChoices  = 10
	maxChoice   = 200
)

// User returns the "user" factory: one tool, ask_user, with which the model
// asks the person it works for a question, optionally with choices. The tool
// interrupts ([tool.Metadata.Interrupts], read/low): it never answers itself
// but returns a [*tool.InterruptError], so an interactive or supervised run
// suspends until the person answers, and the answer is its result; an
// autonomous run does not offer it. It takes no settings.
func User() toolset.Factory { return userFactory{} }

type userFactory struct{}

func (userFactory) Type() string { return UserType }

type askIn struct {
	Question string   `json:"question" jsonschema:"the question for the user: one, clear, self-contained"`
	Choices  []string `json:"choices,omitempty" jsonschema:"the answers to choose from, if the user should pick one (2 to 10); leave out for a free answer"`
}

func (userFactory) Build(_ context.Context, in toolset.BuildInput) ([]tool.Tool, error) {
	settings := in.Settings
	if err := toolset.DecodeSettings(settings, &struct{}{}); err != nil {
		return nil, err
	}
	ask, err := tool.Func(AskUserName,
		"Ask the user a question and wait for the answer, which is this tool's result. "+
			"Use it when you need a decision or a fact only the user has; do not use it for what you can find out yourself.",
		func(_ context.Context, in askIn) (string, error) {
			q := strings.TrimSpace(in.Question)
			switch {
			case q == "":
				return "", errors.New("the question is empty")
			case len(q) > maxQuestion:
				return "", fmt.Errorf("the question is longer than %d bytes", maxQuestion)
			case len(in.Choices) == 1 || len(in.Choices) > maxChoices:
				return "", fmt.Errorf("give 2 to %d choices, or none", maxChoices)
			}
			for i, c := range in.Choices {
				if strings.TrimSpace(c) == "" || len(c) > maxChoice {
					return "", fmt.Errorf("choice %d is empty or longer than %d bytes", i+1, maxChoice)
				}
				if slices.Contains(in.Choices[:i], c) {
					return "", fmt.Errorf("choice %q is repeated", c)
				}
			}
			return "", tool.Interrupt(q, in.Choices...)
		},
		tool.WithMetadata(tool.Metadata{
			Kind: tool.KindRead, Risk: tool.RiskLow, Interrupts: true,
			Display: tool.Display{Title: "Ask the user", Icon: "question", Present: "Asking you", Past: "Asked you"},
		}))
	if err != nil {
		return nil, err
	}
	return []tool.Tool{ask}, nil
}
