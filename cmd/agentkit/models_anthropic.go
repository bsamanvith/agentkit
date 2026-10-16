//go:build !nomodels

package main

import (
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/anthropic"
)

func newClaude(f modelFlags) (llm.Model, error) {
	return anthropic.New(
		anthropic.WithModel(f.model),
		anthropic.WithOptions(anthropic.Options{Effort: anthropic.Effort(f.effort)}),
	)
}
