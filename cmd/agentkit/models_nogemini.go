//go:build nogemini

package main

import "github.com/bsamanvith/agentkit/pkg/llm"

// newGemini is nil: this build has no gemini provider.
var newGemini func(modelFlags) (llm.Model, error)
