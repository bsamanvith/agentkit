package models

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/anthropic"
	"github.com/bsamanvith/agentkit/pkg/llm/gemini"
	"github.com/bsamanvith/agentkit/pkg/llm/openaicompat"
)

// OpenRouterURL is OpenRouter's OpenAI-compatible API.
const OpenRouterURL = "https://openrouter.ai/api/v1"

// Provider is one place models run.
type Provider struct {
	// Name is the provider part of a model name.
	Name string
	// Keys are the environment variables its API key is read from, the
	// first set one wins.
	Keys []string
	// DefaultID is the model a bare provider name means; empty when a name
	// must give one.
	DefaultID string
	// Cost says what using it costs, for the operator.
	Cost string
	// build makes the model with the key.
	build func(id, key string) (llm.Model, error)
}

// Providers returns the providers a lane can use, in no particular order.
func Providers() []Provider {
	return []Provider{
		{
			Name: "anthropic", Keys: []string{"ANTHROPIC_API_KEY", "ANTHROPIC_KEY"},
			DefaultID: anthropic.DefaultModel, Cost: "paid per token",
			build: func(id, key string) (llm.Model, error) {
				// The SDK reads the key from ANTHROPIC_API_KEY only; cloud
				// sessions set ANTHROPIC_KEY.
				if os.Getenv("ANTHROPIC_API_KEY") == "" {
					if err := os.Setenv("ANTHROPIC_API_KEY", key); err != nil {
						return nil, err
					}
				}
				return anthropic.New(anthropic.WithModel(id))
			},
		},
		{
			Name: "openai", Keys: []string{"OPENAI_API_KEY", "OPENAI_KEY"},
			DefaultID: "gpt-5-mini", Cost: "paid per token",
			build: func(id, key string) (llm.Model, error) {
				return openaicompat.New(openaicompat.WithModel(id), openaicompat.WithAPIKey(key),
					openaicompat.WithBaseURL(openaicompat.DefaultBaseURL))
			},
		},
		{
			Name: "openrouter", Keys: []string{"OPENROUTER_API_KEY", "OPENROUTER_KEY"},
			Cost: "paid per token; ids ending :free are free with daily limits",
			build: func(id, key string) (llm.Model, error) {
				return openaicompat.New(openaicompat.WithModel(id), openaicompat.WithAPIKey(key),
					openaicompat.WithBaseURL(OpenRouterURL))
			},
		},
		{
			Name: "gemini", Keys: []string{"GEMINI_API_KEY", "GEMINI_KEY", "GOOGLE_API_KEY"},
			DefaultID: gemini.DefaultModel, Cost: "free tier with daily limits, then paid",
			build: func(id, key string) (llm.Model, error) {
				return gemini.New(gemini.WithAPIKey(key), gemini.WithModel(id))
			},
		},
	}
}

// ErrUnknownProvider means a model name's provider is not in [Providers].
var ErrUnknownProvider = errors.New("unknown model provider")

// Name is a model's full name, "<provider>/<id>".
type Name struct {
	Provider, ID string
}

func (n Name) String() string { return n.Provider + "/" + n.ID }

// provider returns n's provider.
func (n Name) provider() (Provider, bool) {
	for _, p := range Providers() {
		if p.Name == n.Provider {
			return p, true
		}
	}
	return Provider{}, false
}

// Parse reads a comma-separated lane: model names, or bare providers for
// their default model. Duplicates are refused.
func Parse(list string) ([]Name, error) {
	var out []Name
	seen := map[Name]bool{}
	for f := range strings.SplitSeq(list, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		prov, id, _ := strings.Cut(f, "/")
		n := Name{Provider: prov, ID: id}
		p, ok := n.provider()
		if !ok {
			return nil, fmt.Errorf("%w %q in %q: want %s", ErrUnknownProvider, prov, f, providerNames())
		}
		if n.ID == "" {
			if p.DefaultID == "" {
				return nil, fmt.Errorf("model %q: %s needs a model id, as in %s/<id>", f, prov, prov)
			}
			n.ID = p.DefaultID
		}
		if seen[n] {
			return nil, fmt.Errorf("model %s is in the list twice", n)
		}
		seen[n] = true
		out = append(out, n)
	}
	return out, nil
}

func providerNames() string {
	var s []string
	for _, p := range Providers() {
		s = append(s, p.Name)
	}
	return strings.Join(s, ", ")
}

// Build makes the model n names with the key from getenv, and says which
// variable it came from. It fails when no key is set.
func Build(n Name, getenv func(string) string) (llm.Model, error) {
	p, ok := n.provider()
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownProvider, n.Provider)
	}
	key := firstSet(getenv, p.Keys)
	if key == "" {
		return nil, fmt.Errorf("%w: model %s needs %s", ErrNoKey, n, strings.Join(p.Keys, " or "))
	}
	return p.build(n.ID, key)
}

// ErrNoKey means a model's provider has no API key in the environment.
var ErrNoKey = errors.New("no API key")

func firstSet(getenv func(string) string, keys []string) string {
	for _, k := range keys {
		if v := getenv(k); v != "" {
			return v
		}
	}
	return ""
}
