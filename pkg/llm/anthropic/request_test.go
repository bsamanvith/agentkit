package anthropic_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/anthropic"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// representative is a request with every kind of content: a system prompt,
// tools, a tool-use turn with a thinking block and two results (one failed),
// another provider's opaque block, and options.
func representative() llm.Request {
	return llm.Request{
		System: "You are a careful assistant.",
		Tools: []tool.Definition{
			{
				Name:        "search",
				Description: "Search the news.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`),
			},
			{Name: "now", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
		Messages: []llm.Message{
			llm.User(llm.Text{Text: "Find Go news."}),
			llm.Assistant(
				llm.Opaque{Provider: "anthropic", Data: json.RawMessage(`{"type":"thinking","thinking":"","signature":"EqQBCkYI+/Zg=="}`)},
				llm.Text{Text: "Searching."},
				llm.ToolUse{ID: "toolu_1", Name: "search", Input: json.RawMessage(`{"q":"go"}`)},
				llm.ToolUse{ID: "toolu_2", Name: "now", Input: json.RawMessage(`{}`)},
			),
			llm.User(
				llm.ToolResult{ToolUseID: "toolu_1", Content: `[{"title":"Go 1.27 released"}]`},
				llm.ToolResult{ToolUseID: "toolu_2", Content: "clock unavailable", IsError: true},
			),
			llm.Assistant(
				llm.Opaque{Provider: "openai", Data: json.RawMessage(`{"type":"reasoning","encrypted_content":"gAAAA"}`)},
				llm.Text{Text: "Go 1.27 is out."},
			),
			llm.User(llm.Text{Text: "Thanks. Summarize it."}),
		},
		MaxTokens: 1024,
		Provider: anthropic.Options{
			Effort:          anthropic.EffortHigh,
			ThinkingDisplay: anthropic.ThinkingSummarized,
			Cache:           []anthropic.CachePoint{anthropic.CacheTools, anthropic.CacheSystem, anthropic.CacheConversation},
			Betas:           []string{"context-management-2025-06-27"},
		},
	}
}

func TestRequestBodyGolden(t *testing.T) {
	t.Parallel()
	rp, srv := newReplay(t, "testdata/conformance/text.http")
	if _, err := llm.Collect(newModel(t, srv).Generate(t.Context(), representative())); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	sent := rp.sent()[0]
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, sent.body, "", "  "); err != nil {
		t.Fatalf("request body is not JSON: %v\n%s", err, sent.body)
	}
	pretty.WriteByte('\n')
	testkit.Golden(t, "testdata/request.golden.json", pretty.Bytes())

	betas := strings.Split(sent.header.Get("Anthropic-Beta"), ",")
	for _, want := range []string{"server-side-fallback-2026-07-01", "context-management-2025-06-27"} {
		if !slices.Contains(betas, want) {
			t.Errorf("anthropic-beta = %q, want it to contain %q", betas, want)
		}
	}
}

// body sends req to a replay server and returns the request body as JSON.
func body(t *testing.T, req llm.Request, opts ...anthropic.Option) (map[string]any, string) {
	t.Helper()
	rp, srv := newReplay(t, "testdata/conformance/text.http")
	if _, err := llm.Collect(newModel(t, srv, opts...).Generate(t.Context(), req)); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	sent := rp.sent()[0]
	var m map[string]any
	if err := json.Unmarshal(sent.body, &m); err != nil {
		t.Fatal(err)
	}
	return m, sent.header.Get("Anthropic-Beta")
}

func TestOptions(t *testing.T) {
	t.Parallel()
	hello := []llm.Message{llm.User(llm.Text{Text: "Hello."})}
	for _, c := range []struct {
		name     string
		model    []anthropic.Option
		provider any
		effort   string
		fallback bool
		cached   bool
	}{
		{name: "defaults", effort: "medium", fallback: true, cached: true},
		{
			name:     "request options",
			provider: anthropic.Options{Effort: anthropic.EffortLow, Fallback: anthropic.FallbackOff, NoCache: true},
			effort:   "low",
		},
		{
			name:     "model defaults under a request pointer",
			model:    []anthropic.Option{anthropic.WithOptions(anthropic.Options{Effort: anthropic.EffortMax, Fallback: anthropic.FallbackOff})},
			provider: &anthropic.Options{Cache: []anthropic.CachePoint{}},
			effort:   "max",
		},
		{
			name:     "another provider's options are ignored",
			model:    []anthropic.Option{anthropic.WithOptions(anthropic.Options{Effort: anthropic.EffortHigh})},
			provider: struct{ Effort string }{"low"},
			effort:   "high", fallback: true, cached: true,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, betas := body(t, llm.Request{Messages: hello, Provider: c.provider}, c.model...)
			if e := got["output_config"].(map[string]any)["effort"]; e != c.effort {
				t.Errorf("effort = %v, want %v", e, c.effort)
			}
			if got["thinking"].(map[string]any)["type"] != "adaptive" {
				t.Errorf("thinking = %v, want adaptive", got["thinking"])
			}
			_, hasFallback := got["fallbacks"]
			if hasFallback != c.fallback || strings.Contains(betas, "server-side-fallback") != c.fallback {
				t.Errorf("fallbacks = %v (beta %q), want fallback %v", got["fallbacks"], betas, c.fallback)
			}
			if c.fallback && got["fallbacks"] != "default" {
				t.Errorf("fallbacks = %v, want \"default\"", got["fallbacks"])
			}
			if _, cached := got["cache_control"]; cached != c.cached {
				t.Errorf("cache_control = %v, want cached %v", got["cache_control"], c.cached)
			}
		})
	}
}

func TestInvalidRequests(t *testing.T) {
	t.Parallel()
	hello := []llm.Message{llm.User(llm.Text{Text: "Hello."})}
	for _, c := range []struct {
		name string
		req  llm.Request
	}{
		{"unknown effort", llm.Request{Messages: hello, Provider: anthropic.Options{Effort: "extreme"}}},
		{"negative max tokens", llm.Request{Messages: hello, MaxTokens: -1}},
		{"opaque data that is not a block", llm.Request{Messages: []llm.Message{
			llm.User(llm.Text{Text: "Hi."}),
			llm.Assistant(llm.Opaque{Provider: "anthropic", Data: json.RawMessage(`[1]`)}),
		}}},
		{"tool input that is not JSON", llm.Request{Messages: []llm.Message{
			llm.Assistant(llm.ToolUse{ID: "toolu_1", Name: "x", Input: json.RawMessage(`{"a":`)}),
		}}},
		{"pointer block", llm.Request{Messages: []llm.Message{llm.User(&llm.Text{Text: "Hi."})}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			rp, srv := newReplay(t)
			_, err := llm.Collect(newModel(t, srv).Generate(t.Context(), c.req))
			if !errors.Is(err, llm.ErrInvalidRequest) {
				t.Errorf("err = %v, want ErrInvalidRequest", err)
			}
			if n := len(rp.sent()); n != 0 {
				t.Errorf("%d requests sent, want none", n)
			}
		})
	}
}

func TestNewRejectsInvalidSettings(t *testing.T) {
	t.Parallel()
	for name, opt := range map[string]anthropic.Option{
		"empty model":         anthropic.WithModel(""),
		"zero max tokens":     anthropic.WithMaxTokens(0),
		"unknown cache point": anthropic.WithOptions(anthropic.Options{Cache: []anthropic.CachePoint{"everything"}}),
		"unknown fallback":    anthropic.WithOptions(anthropic.Options{Fallback: "sometimes"}),
	} {
		if _, err := anthropic.New(opt); !errors.Is(err, llm.ErrInvalidRequest) {
			t.Errorf("%s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
}
