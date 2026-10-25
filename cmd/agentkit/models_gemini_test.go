//go:build !noagent && !nomodels && !nogemini

package main

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/llm/gemini"
)

func TestDefaultGeminiModelIsTheAdapters(t *testing.T) {
	t.Parallel()
	if defaultGeminiModelID != gemini.DefaultModel {
		t.Errorf("defaultGeminiModelID = %q, want gemini.DefaultModel %q", defaultGeminiModelID, gemini.DefaultModel)
	}
}

// geminiConfig is a server whose agent runs Claude by default and allows
// Gemini models by name and by pattern, with a variant for Gemini.
const geminiConfig = `{
  "mcp": {"toolsets": [{"name": "demo-a", "type": "demo"}]},
  "agents": [{"name": "mixed", "model": {"id": "m1", "effort": "high"}, "system": "You are mixed.", "toolsets": ["demo-a"],
    "models": {
      "allow": ["gemini/g1"],
      "allow_patterns": ["gemini/g2-.*"],
      "variants": [{"match": "re:gemini/.*", "effort": "max", "system_append": "You run on Gemini."}]
    }}]
}`

// An agent runs a Gemini model it allows on the gemini provider, with its
// variant, emulated tool search and Gemini's context window; a
// conversation that ran on Claude cannot go on on Gemini; and GET
// /v1/models lists Gemini's models.
func TestGeminiModels(t *testing.T) {
	t.Parallel()
	m := newCountingModels(t, "m1", "g1", "g2-x")
	client := serveModels(t, geminiConfig, m, "")
	ctx := t.Context()
	for _, tc := range []struct{ model, id string }{{"gemini/g1", "g1"}, {"gemini/g2-x", "g2-x"}} {
		run, err := client.Run(ctx, agentclient.RunRequest{Agent: "mixed", Message: "hi", Model: tc.model})
		if err != nil {
			t.Fatalf("run on %s: %v", tc.model, err)
		}
		if run.Model != tc.model || run.Answer != "from "+tc.id {
			t.Errorf("run on %s = model %q, answer %q", tc.model, run.Model, run.Answer)
		}
		f := m.flags[tc.id]
		if f.provider != providerGemini || f.effort != "max" {
			t.Errorf("%s built with %+v, want provider gemini and the variant's effort", tc.id, f)
		}
		if got := m.fakes[tc.id].systems(); len(got) != 1 || got[0] != "You are mixed.\n\nYou run on Gemini." {
			t.Errorf("%s got systems %q", tc.id, got)
		}
	}

	claude, err := client.Run(ctx, agentclient.RunRequest{Agent: "mixed", Message: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Run(ctx, agentclient.RunRequest{Agent: "mixed", Message: "and now?", Model: "gemini/g1", ConversationID: claude.ConversationID})
	if !errors.Is(err, agentclient.ErrModelProviderMismatch) {
		t.Errorf("switching to Gemini mid-conversation = %v, want ErrModelProviderMismatch", err)
	}

	list, err := client.Models(ctx)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(list.Providers, func(p wire.ModelProvider) bool { return p.Name == providerGemini })
	if i < 0 || !slices.Contains(list.Providers[i].Models, "gemini/"+gemini.DefaultModel) {
		t.Errorf("GET /v1/models = %+v, want gemini with its default model", list.Providers)
	}
}

// TestGeminiModelConfig checks the model settings a gemini agent gets and
// the ones it cannot have.
func TestGeminiModelConfig(t *testing.T) {
	t.Parallel()
	g := agentModelConfig{Provider: providerGemini}
	if g.id() != defaultGeminiModelID || g.name() != "gemini/"+defaultGeminiModelID {
		t.Errorf("default gemini model = %q (%q)", g.id(), g.name())
	}
	if g.toolSearch() != toolSearchEmulated || g.window().Tokens != geminiContextWindow {
		t.Errorf("gemini: tool search %q, window %d", g.toolSearch(), g.window().Tokens)
	}
	if f := g.flags(func(string) string { return "a-key" }); f.apiKey != "" || f.provider != providerGemini {
		t.Errorf("gemini flags = %+v; its key comes from $GEMINI_API_KEY in the adapter", f)
	}
	for _, c := range []struct{ def, want string }{
		{`{"model": {"provider": "gemini", "tool_search": "native"}}`, `"native" is for provider "anthropic"; gemini models use "emulated"`},
		{`{"model": {"provider": "gemini"}, "memory": {"native_tool": true}}`, "native memory tool needs provider"},
		{`{"model": {"provider": "gemini", "base_url": "http://x"}}`, `base_url and api_key_env are for provider "openai"`},
	} {
		d := agentDef{Name: "g"}
		if err := json.Unmarshal([]byte(c.def), &d); err != nil {
			t.Fatal(err)
		}
		if err := d.check(false); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: check = %v, want %q", c.def, err, c.want)
		}
	}
	if err := (agentDef{Name: "g", Model: agentModelConfig{Provider: providerGemini, Effort: "max"}}).check(false); err != nil {
		t.Errorf("a gemini agent: %v", err)
	}
	if _, err := buildModel(agentModelConfig{Provider: providerGemini, ID: "gemini-3.5-flash", Effort: "xhigh"}.flags(nil)); err != nil {
		t.Errorf("building a gemini model: %v", err)
	}
}
