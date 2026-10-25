//go:build !nomodels && !nogemini

package main

import (
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/gemini"
)

// newGemini builds a Gemini model, with its key from $GEMINI_API_KEY (or
// $GOOGLE_API_KEY). The effort is its thinking level: xhigh and max are
// high, Gemini's highest.
var newGemini = func(f modelFlags) (llm.Model, error) {
	level := f.effort
	if level == "xhigh" || level == "max" {
		level = "high"
	}
	return gemini.New(
		gemini.WithModel(f.model),
		gemini.WithOptions(gemini.Options{ThinkingLevel: level}),
	)
}
