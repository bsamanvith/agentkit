package main

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/llm"
)

// The model adapters serve can build. They are in their own files, which the
// nomodels tag leaves out, so an image whose agents run on a remote runner
// (agentkit-api) links no model adapter:
//
//	nomodels  no Anthropic adapter (models_anthropic.go), no
//	          OpenAI-compatible one (models_openai.go) and no Gemini one
//	          (models_gemini.go); agent-api needs $AGENTKIT_RUNNER_URL, and
//	          run and flow demo fail
//	nogemini  no Gemini adapter, and no gemini provider: a config that
//	          names it fails as for an unknown provider
//
// defaultModelID is anthropic.DefaultModel, which a nomodels build cannot
// name; TestDefaultModelIsTheAdapters keeps them equal.
const defaultModelID = "claude-haiku-5-5"

// defaultProvider is the provider of a model config that names none.
const defaultProvider = "anthropic"

// modelProviders are the model adapters this binary can build, by the name a
// config's model.provider gives. A provider is a package under pkg/llm
// implementing llm.Model, a file here that builds it (behind a tag of its
// own, as models_anthropic.go is behind nomodels) and its line in this table.
func modelProviders() map[string]func(modelFlags) (llm.Model, error) {
	p := map[string]func(modelFlags) (llm.Model, error){
		defaultProvider: newClaude,
		providerOpenAI:  newOpenAI,
	}
	if newGemini != nil { // nil with nogemini
		p[providerGemini] = newGemini
	}
	return p
}

// buildModel builds the model of f's provider.
func buildModel(f modelFlags) (llm.Model, error) {
	p := f.provider
	if p == "" {
		p = defaultProvider
	}
	build, ok := modelProviders()[p]
	if !ok {
		return nil, fmt.Errorf("provider %q: want %s", p, providerNames())
	}
	return build(f)
}

// providerNames lists the providers for an error message.
func providerNames() string {
	names := slices.Sorted(maps.Keys(modelProviders()))
	return `"` + strings.Join(names, `", "`) + `"`
}
