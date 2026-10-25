//go:build !nomodels

package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/anthropic"
)

func TestDefaultModelIsTheAdapters(t *testing.T) {
	t.Parallel()
	if defaultModelID != anthropic.DefaultModel {
		t.Errorf("defaultModelID = %q, want anthropic.DefaultModel %q", defaultModelID, anthropic.DefaultModel)
	}
}

// TestOpenAIProvider builds an openai model from an agent's config, with its
// key from the variable the config names, and runs it against a stand-in
// OpenAI-compatible server on plain http.
func TestOpenAIProvider(t *testing.T) {
	t.Parallel()
	var gotAuth, gotPath, gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Model string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotAuth, gotPath, gotModel = r.Header.Get("Authorization"), r.URL.Path, body.Model
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi.\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()
	env := map[string]string{"VLLM_KEY": "k-123", defaultOpenAIKeyEnv: "not-this"}
	getenv := func(k string) string { return env[k] }
	cfg := agentModelConfig{Provider: providerOpenAI, ID: "llama3.2", BaseURL: srv.URL + "/v1", APIKeyEnv: "VLLM_KEY"}
	m, err := buildModel(cfg.flags(getenv))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := llm.Collect(m.Generate(t.Context(), llm.Request{Messages: []llm.Message{llm.User(llm.Text{Text: "Hello."})}}))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Text() != "Hi." || gotAuth != "Bearer k-123" || gotPath != "/v1/chat/completions" || gotModel != "llama3.2" {
		t.Errorf("reply %q; sent %s for %q with %q", resp.Message.Text(), gotPath, gotModel, gotAuth)
	}

	// The default variable, and no key when the named one is unset.
	if f := (agentModelConfig{Provider: providerOpenAI, ID: "m"}).flags(getenv); f.apiKey != "not-this" {
		t.Errorf("default key variable: apiKey = %q", f.apiKey)
	}
	if f := (agentModelConfig{Provider: providerOpenAI, ID: "m", APIKeyEnv: "UNSET"}).flags(getenv); f.apiKey != "" {
		t.Errorf("unset key variable: apiKey = %q", f.apiKey)
	}
	// Anthropic models never get the OpenAI key, and get the default ID.
	if f := (agentModelConfig{}).flags(getenv); f.apiKey != "" || f.model != defaultModelID {
		t.Errorf("anthropic flags = %+v", f)
	}
	if _, err := buildModel(modelFlags{provider: providerOpenAI, model: "m", baseURL: "localhost:11434"}); err == nil {
		t.Error("a base URL without a scheme was accepted")
	}
}

// TestModelIDWrittenAsName: an ID written as the model's name,
// "anthropic/<id>", is that model, not "anthropic/anthropic/<id>"; an
// openai ID keeps its slashes.
func TestModelIDWrittenAsName(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		cfg      agentModelConfig
		name, id string
	}{
		{agentModelConfig{ID: "anthropic/claude-x"}, "anthropic/claude-x", "claude-x"},
		{agentModelConfig{Provider: "anthropic", ID: "anthropic/claude-x"}, "anthropic/claude-x", "claude-x"},
		{agentModelConfig{Provider: "anthropic", ID: "claude-x"}, "anthropic/claude-x", "claude-x"},
		{agentModelConfig{Provider: providerOpenAI, ID: "openai/gpt-4o"}, "openai/openai/gpt-4o", "openai/gpt-4o"},
	} {
		if got := tc.cfg.name(); got != tc.name {
			t.Errorf("%+v: name() = %q, want %q", tc.cfg, got, tc.name)
		}
		if got := tc.cfg.flags(func(string) string { return "" }).model; got != tc.id {
			t.Errorf("%+v: the adapter's model = %q, want %q", tc.cfg, got, tc.id)
		}
		if got := wireModel(tc.cfg).ID; got != tc.id {
			t.Errorf("%+v: wireModel ID = %q, want %q", tc.cfg, got, tc.id)
		}
	}
}
