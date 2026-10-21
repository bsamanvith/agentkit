//go:build !nomodels

package main

import (
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/openaicompat"
)

// newOpenAI builds a model on an OpenAI-compatible server: f.baseURL (empty
// for $OPENAI_BASE_URL or OpenAI's API) with f.apiKey (empty sends none).
func newOpenAI(f modelFlags) (llm.Model, error) {
	return openaicompat.New(
		openaicompat.WithModel(f.model),
		openaicompat.WithBaseURL(f.baseURL),
		openaicompat.WithAPIKey(f.apiKey),
		openaicompat.WithOptions(openaicompat.Options{ReasoningEffort: f.effort}),
	)
}
