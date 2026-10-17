package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
)

func compactHistory() harness.Transcript {
	return harness.Transcript{
		harness.UserText("find the file"),
		llm.Assistant(
			llm.Opaque{Provider: "anthropic", Data: json.RawMessage(`{"type":"thinking","thinking":"secret"}`)},
			llm.Text{Text: "Looking."},
			use("t1", "search", `{"q":"file"}`),
		),
		llm.User(llm.ToolResult{ToolUseID: "t1", Content: strings.Repeat("é", 3000), IsError: true}),
		llm.Assistant(llm.Text{Text: "It is at /tmp/x."}),
	}
}

// Compact sends the history as text, without tools or opaque blocks, with
// the fixed prompt and the harness's request options, and returns the text.
func TestCompactAsksForASummary(t *testing.T) {
	leakcheck(t)
	model := llmtest.NewFake(t, llmtest.Text("  The user wanted a file; it is /tmp/x.\n").WithUsage(llm.Usage{InputTokens: 7, OutputTokens: 3}))
	h, err := harness.New(model, newRegistry(t), harness.WithRequest(harness.RequestOptions{MaxTokens: 99, Provider: "opts"}))
	if err != nil {
		t.Fatal(err)
	}
	s, err := h.Compact(context.Background(), compactHistory())
	if err != nil {
		t.Fatal(err)
	}
	diff(t, "summary", harness.Summary{Text: "The user wanted a file; it is /tmp/x.", Usage: llm.Usage{InputTokens: 7, OutputTokens: 3}}, s)
	reqs := model.Requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests", len(reqs))
	}
	r := reqs[0]
	if r.System != harness.CompactionPrompt || r.MaxTokens != 99 || r.Provider != "opts" || len(r.Tools) != 0 || len(r.Messages) != 1 {
		t.Errorf("request %+v", r)
	}
	text := r.Messages[0].Text()
	for _, want := range []string{"find the file", "Looking.", `[tool call search: {"q":"file"}]`, "[tool error search: é", "more bytes)]", "It is at /tmp/x."} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered history lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "secret") {
		t.Errorf("rendered history has the opaque block:\n%s", text)
	}
	if text != harness.RenderHistory(compactHistory()) {
		t.Errorf("request is not RenderHistory's text")
	}
}

func TestRenderHistoryCutsLongBlocks(t *testing.T) {
	short := strings.Repeat("a", 4000)
	got := harness.RenderHistory(harness.Transcript{llm.User(llm.ToolResult{ToolUseID: "x", Content: short})})
	if !strings.Contains(got, short+"]") {
		t.Errorf("a block of 4000 bytes was cut:\n%s", got)
	}
	got = harness.RenderHistory(harness.Transcript{llm.User(llm.ToolResult{ToolUseID: "x", Content: short + "bb"})})
	if !strings.Contains(got, short+"… (2 more bytes)]") {
		t.Errorf("a block of 4002 bytes:\n%s", got)
	}
	// A cut never splits a rune.
	got = harness.RenderHistory(harness.Transcript{llm.User(llm.ToolResult{ToolUseID: "x", Content: "a" + strings.Repeat("é", 2000)})})
	if !strings.Contains(got, "a"+strings.Repeat("é", 1999)+"… (2 more bytes)]") {
		t.Errorf("a cut through a rune:\n%s", got)
	}
}

func TestCompactFailures(t *testing.T) {
	leakcheck(t)
	cut := llmtest.Text("half a summ")
	cut.Response.Stop.Kind = llm.StopMaxTokens
	for _, c := range []struct {
		name    string
		history harness.Transcript
		reply   llmtest.Reply
		want    error
	}{
		{"model error", compactHistory(), llmtest.Fail(llm.ErrOverloaded), llm.ErrOverloaded},
		{"cut summary", compactHistory(), cut, harness.ErrMaxTokens},
		{"no text", compactHistory(), llmtest.Text(" \n"), harness.ErrUnexpectedStop},
		{"no history", nil, llmtest.Reply{}, harness.ErrInvalidInput},
	} {
		t.Run(c.name, func(t *testing.T) {
			var model *llmtest.Fake
			if c.history != nil {
				model = llmtest.NewFake(t, c.reply)
			} else {
				model = llmtest.NewFake(t)
			}
			h, err := harness.New(model, newRegistry(t))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.Compact(context.Background(), c.history); !errors.Is(err, c.want) {
				t.Errorf("Compact = %v, want %v", err, c.want)
			}
		})
	}
}

func TestContextWindowFull(t *testing.T) {
	for _, c := range []struct {
		name string
		w    harness.ContextWindow
		u    llm.Usage
		want bool
	}{
		{"unknown window", harness.ContextWindow{}, llm.Usage{InputTokens: 1 << 30}, false},
		{"below the default share", harness.ContextWindow{Tokens: 1000}, llm.Usage{InputTokens: 799}, false},
		{"at the default share", harness.ContextWindow{Tokens: 1000}, llm.Usage{InputTokens: 800}, true},
		{"cached tokens count", harness.ContextWindow{Tokens: 1000}, llm.Usage{InputTokens: 200, CacheReadTokens: 500, CacheWriteTokens: 100}, true},
		{"output tokens do not", harness.ContextWindow{Tokens: 1000}, llm.Usage{InputTokens: 799, OutputTokens: 500}, false},
		{"below a share", harness.ContextWindow{Tokens: 1000, Share: 0.5}, llm.Usage{InputTokens: 499}, false},
		{"at a share", harness.ContextWindow{Tokens: 1000, Share: 0.5}, llm.Usage{InputTokens: 500}, true},
		{"whole window", harness.ContextWindow{Tokens: 1000, Share: 1}, llm.Usage{InputTokens: 999}, false},
		{"share past 1 is the default", harness.ContextWindow{Tokens: 1000, Share: 2}, llm.Usage{InputTokens: 800}, true},
	} {
		if got := c.w.Full(c.u); got != c.want {
			t.Errorf("%s: Full = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestContextWindowOption(t *testing.T) {
	leakcheck(t)
	for _, w := range []harness.ContextWindow{{Tokens: -1}, {Tokens: 10, Share: -0.1}, {Tokens: 10, Share: 1.1}} {
		if _, err := harness.New(llmtest.NewFake(t), newRegistry(t), harness.WithContextWindow(w)); !errors.Is(err, harness.ErrInvalidConfig) {
			t.Errorf("New with window %+v = %v, want %v", w, err, harness.ErrInvalidConfig)
		}
	}
	h, err := harness.New(llmtest.NewFake(t), newRegistry(t), harness.WithContextWindow(harness.ContextWindow{Tokens: 100, Share: 1}))
	if err != nil {
		t.Fatal(err)
	}
	if h.ShouldCompact(llm.Usage{InputTokens: 99}) || !h.ShouldCompact(llm.Usage{InputTokens: 100}) {
		t.Errorf("ShouldCompact does not follow the window")
	}
	h, err = harness.New(llmtest.NewFake(t), newRegistry(t))
	if err != nil {
		t.Fatal(err)
	}
	if h.ShouldCompact(llm.Usage{InputTokens: 1 << 30}) {
		t.Errorf("a harness without a window compacts")
	}
}
