package openaicompat_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/openaicompat"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// ownReasoning is a reasoning block as this adapter returns it.
var ownReasoning = llm.Opaque{Provider: openaicompat.Provider, Data: json.RawMessage(`{"reasoning_content":"The user wants Go news & a time."}`)}

// representative is a request with every kind of content: a system prompt,
// tools (one without a schema), a tool-use turn with a reasoning block and
// two results (one failed) followed by text, other providers' opaque blocks
// (one making up a whole assistant message), a user message of two texts,
// and options.
func representative() llm.Request {
	return llm.Request{
		System: "You are a careful assistant.",
		Tools: []tool.Definition{
			{
				Name:        "search",
				Description: "Search the news.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string","maxLength":200}},"required":["q"]}`),
			},
			{Name: "now"},
		},
		Messages: []llm.Message{
			llm.User(llm.Text{Text: "Find Go news."}),
			llm.Assistant(
				ownReasoning,
				llm.Text{Text: "Searching."},
				llm.ToolUse{ID: "call_1", Name: "search", Input: json.RawMessage(`{"q":"go <1.27>"}`)},
				llm.ToolUse{ID: "call_2", Name: "now", Input: json.RawMessage(`{}`)},
			),
			llm.User(
				llm.ToolResult{ToolUseID: "call_1", Content: `[{"title":"Go 1.27 released"}]`},
				llm.ToolResult{ToolUseID: "call_2", Content: "clock unavailable", IsError: true},
				llm.Text{Text: "Only the headline, please."},
			),
			llm.Assistant(
				llm.Opaque{Provider: "anthropic", Data: json.RawMessage(`{"type":"thinking","thinking":"","signature":"EqQBCkYI+/Zg=="}`)},
				llm.Text{Text: "Go 1.27 is out."},
			),
			llm.User(llm.Text{Text: "Thanks."}, llm.Text{Text: "Anything else?"}),
			llm.Assistant(llm.Opaque{Provider: "anthropic", Data: json.RawMessage(`{"type":"redacted_thinking","data":"abc"}`)}),
			llm.User(llm.Text{Text: "Summarize it."}),
		},
		MaxTokens: 1024,
		Provider:  openaicompat.Options{ReasoningEffort: "high"},
	}
}

// TestRequestBodyGolden checks the body of a representative request: the
// message mapping, tools, the output limit, reasoning effort and streaming
// options, byte for byte against testdata/request.golden.json.
func TestRequestBodyGolden(t *testing.T) {
	t.Parallel()
	rp, srv := newReplay(t, "testdata/conformance/text.http")
	req := representative()
	snapshot := req.Clone()
	if _, err := llm.Collect(newModel(t, srv).Generate(t.Context(), req)); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	sent := rp.sent()[0]
	if sent.path != "/v1/chat/completions" {
		t.Errorf("path = %q, want /v1/chat/completions", sent.path)
	}
	if got := sent.header.Get("Authorization"); got != "Bearer test-key" {
		t.Errorf("Authorization = %q, want the API key", got)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, sent.body, "", "  "); err != nil {
		t.Fatalf("request body is not JSON: %v\n%s", err, sent.body)
	}
	pretty.WriteByte('\n')
	testkit.Golden(t, "testdata/request.golden.json", pretty.Bytes())
	if !jsonEqual(t, req, snapshot) {
		t.Error("Generate modified its request")
	}
}

func jsonEqual(t *testing.T, a, b llm.Request) bool {
	t.Helper()
	ja, err := json.Marshal(a.Messages)
	if err != nil {
		t.Fatal(err)
	}
	jb, err := json.Marshal(b.Messages)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.Equal(ja, jb)
}

// body sends req to a model made with opts and returns the request body the
// server received, decoded.
func body(t *testing.T, req llm.Request, opts ...openaicompat.Option) map[string]any {
	t.Helper()
	rp, srv := newReplay(t, "testdata/conformance/text.http")
	if _, err := llm.Collect(newModel(t, srv, opts...).Generate(t.Context(), req)); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(rp.sent()[0].body, &got); err != nil {
		t.Fatal(err)
	}
	return got
}

// TestOptions checks how options and output limits reach the body: request
// over model defaults, value or pointer, and the legacy max_tokens switch.
func TestOptions(t *testing.T) {
	t.Parallel()
	withReasoning := llm.Request{Messages: []llm.Message{
		llm.User(llm.Text{Text: "Hi."}),
		llm.Assistant(ownReasoning, llm.Text{Text: "Hello."}),
		llm.User(llm.Text{Text: "Again."}),
	}}
	with := func(r llm.Request, p any, maxTokens int) llm.Request {
		r.Provider, r.MaxTokens = p, maxTokens
		return r
	}
	var nilOptions *openaicompat.Options
	cases := []struct {
		name     string
		req      llm.Request
		opts     []openaicompat.Option
		want     map[string]any // fields that must have these values
		wantNone []string       // fields that must be absent
	}{
		{
			"defaults", with(withReasoning, nil, 0), nil,
			map[string]any{"stream": true, "stream_options": map[string]any{"include_usage": true}},
			[]string{"max_tokens", "max_completion_tokens", "reasoning_effort", "tool_choice", "tools"},
		},
		{
			"request limit", with(withReasoning, nil, 50),
			[]openaicompat.Option{openaicompat.WithMaxTokens(10)},
			map[string]any{"max_completion_tokens": 50.0},
			[]string{"max_tokens"},
		},
		{
			"model limit", with(withReasoning, nil, 0),
			[]openaicompat.Option{openaicompat.WithMaxTokens(10)},
			map[string]any{"max_completion_tokens": 10.0},
			nil,
		},
		{
			"legacy limit", with(withReasoning, openaicompat.Options{LegacyMaxTokens: true}, 50), nil,
			map[string]any{"max_tokens": 50.0},
			[]string{"max_completion_tokens"},
		},
		{
			"legacy limit by default", with(withReasoning, nil, 50),
			[]openaicompat.Option{openaicompat.WithOptions(openaicompat.Options{LegacyMaxTokens: true})},
			map[string]any{"max_tokens": 50.0},
			nil,
		},
		{
			"effort from the model", with(withReasoning, nil, 0),
			[]openaicompat.Option{openaicompat.WithOptions(openaicompat.Options{ReasoningEffort: "low"})},
			map[string]any{"reasoning_effort": "low"},
			nil,
		},
		{
			"effort from the request wins", with(withReasoning, &openaicompat.Options{ReasoningEffort: "minimal"}, 0),
			[]openaicompat.Option{openaicompat.WithOptions(openaicompat.Options{ReasoningEffort: "low"})},
			map[string]any{"reasoning_effort": "minimal"},
			nil,
		},
		{
			"nil pointer means none", with(withReasoning, nilOptions, 0),
			[]openaicompat.Option{openaicompat.WithOptions(openaicompat.Options{ReasoningEffort: "low"})},
			map[string]any{"reasoning_effort": "low"},
			nil,
		},
		{
			"another adapter's options are ignored", with(withReasoning, struct{ Effort string }{"max"}, 0), nil,
			nil,
			[]string{"reasoning_effort"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := body(t, c.req, c.opts...)
			for k, v := range c.want {
				if b, _ := json.Marshal(got[k]); string(b) != mustJSON(t, v) {
					t.Errorf("%s = %s, want %s", k, b, mustJSON(t, v))
				}
			}
			for _, k := range c.wantNone {
				if _, ok := got[k]; ok {
					t.Errorf("%s = %v, want it absent", k, got[k])
				}
			}
		})
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestReasoningSentBack checks that a reasoning block goes back in the field
// it came in, joined per field when there are several, and that
// DropReasoning leaves it out.
func TestReasoningSentBack(t *testing.T) {
	t.Parallel()
	req := llm.Request{Messages: []llm.Message{
		llm.User(llm.Text{Text: "Hi."}),
		llm.Assistant(
			llm.Opaque{Provider: openaicompat.Provider, Data: json.RawMessage(`{"reasoning":"First, "}`)},
			llm.Text{Text: "Let me look."},
			llm.Opaque{Provider: openaicompat.Provider, Data: json.RawMessage(`{"reasoning":"then <this>."}`)},
			llm.ToolUse{ID: "c1", Name: "now"},
		),
		llm.User(llm.ToolResult{ToolUseID: "c1", Content: "noon"}),
	}}
	assistant := func(b map[string]any) map[string]any {
		return b["messages"].([]any)[1].(map[string]any)
	}
	got := assistant(body(t, req))
	if got["reasoning"] != "First, then <this>." {
		t.Errorf("reasoning = %#v, want the two blocks joined", got["reasoning"])
	}
	if got["content"] != "Let me look." {
		t.Errorf("content = %#v", got["content"])
	}
	req.Provider = openaicompat.Options{DropReasoning: true}
	if got := assistant(body(t, req)); got["reasoning"] != nil {
		t.Errorf("reasoning = %#v with DropReasoning, want it left out", got["reasoning"])
	}
}

// TestAssistantContent checks when an assistant message has content: an
// empty text block is sent as "", and a message of tool calls only has none.
func TestAssistantContent(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		msg  llm.Message
		want any // nil: no content member
	}{
		"empty text":      {llm.Assistant(llm.Text{}, llm.ToolUse{ID: "c1", Name: "now"}), ""},
		"tool calls only": {llm.Assistant(llm.ToolUse{ID: "c1", Name: "now"}), nil},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			req := llm.Request{Messages: []llm.Message{
				llm.User(llm.Text{Text: "Hi."}), c.msg, llm.User(llm.ToolResult{ToolUseID: "c1", Content: "noon"}),
			}}
			got := body(t, req)["messages"].([]any)[1].(map[string]any)
			content, ok := got["content"]
			if ok != (c.want != nil) || ok && content != c.want {
				t.Errorf("content = %#v (present %v), want %#v", content, ok, c.want)
			}
		})
	}
}

// TestInvalidRequests checks that requests the API cannot take fail with
// llm.ErrInvalidRequest before anything is sent.
func TestInvalidRequests(t *testing.T) {
	t.Parallel()
	user := llm.User(llm.Text{Text: "Hi."})
	cases := map[string]llm.Request{
		"no messages":                {},
		"unknown role":               {Messages: []llm.Message{{Role: "system", Content: []llm.Block{llm.Text{Text: "x"}}}}},
		"empty user message":         {Messages: []llm.Message{llm.User()}},
		"tool use from the user":     {Messages: []llm.Message{llm.User(llm.ToolUse{ID: "c", Name: "n"})}},
		"tool result from the model": {Messages: []llm.Message{user, llm.Assistant(llm.ToolResult{ToolUseID: "c"}), user}},
		"unknown block":              {Messages: []llm.Message{llm.User(llm.Unknown{Type: "image", Data: json.RawMessage(`{"type":"image"}`)})}},
		"reasoning from the user":    {Messages: []llm.Message{llm.User(ownReasoning)}},
		"tool result without an ID":  {Messages: []llm.Message{llm.User(llm.ToolResult{Content: "x"})}},
		"tool use without a name":    {Messages: []llm.Message{user, llm.Assistant(llm.ToolUse{ID: "c"}), user}},
		"invalid tool input":         {Messages: []llm.Message{user, llm.Assistant(llm.ToolUse{ID: "c", Name: "n", Input: json.RawMessage(`{"a":`)}), user}},
		"invalid reasoning block":    {Messages: []llm.Message{user, llm.Assistant(llm.Opaque{Provider: openaicompat.Provider, Data: json.RawMessage(`["x"]`)}), user}},
		"unknown reasoning field":    {Messages: []llm.Message{user, llm.Assistant(llm.Opaque{Provider: openaicompat.Provider, Data: json.RawMessage(`{"thinking":"x"}`)}), user}},
		"schema not an object":       {Messages: []llm.Message{user}, Tools: []tool.Definition{{Name: "t", InputSchema: json.RawMessage(`[1]`)}}},
		"negative max tokens":        {Messages: []llm.Message{user}, MaxTokens: -1},
		"deferred tools":             {Messages: []llm.Message{user}, Tools: []tool.Definition{{Name: "t"}, {Name: "u"}}, Deferred: []string{"u"}},
		"invalid effort":             {Messages: []llm.Message{user}, Provider: openaicompat.Options{ReasoningEffort: "High Effort"}},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rp, srv := newReplay(t)
			_, err := llm.Collect(newModel(t, srv).Generate(t.Context(), req))
			if !errors.Is(err, llm.ErrInvalidRequest) {
				t.Errorf("err = %v, want ErrInvalidRequest", err)
			}
			if n := len(rp.sent()); n != 0 {
				t.Errorf("%d requests sent, want none", n)
			}
		})
	}
}

// TestNewRejectsInvalidSettings checks the constructor's validation.
func TestNewRejectsInvalidSettings(t *testing.T) {
	t.Parallel()
	for name, opts := range map[string][]openaicompat.Option{
		"no model":         nil,
		"negative limit":   {openaicompat.WithModel("m"), openaicompat.WithMaxTokens(-1)},
		"negative idle":    {openaicompat.WithModel("m"), openaicompat.WithIdleTimeout(-1)},
		"invalid defaults": {openaicompat.WithModel("m"), openaicompat.WithOptions(openaicompat.Options{ReasoningEffort: "!"})},
		"negative retries": {openaicompat.WithModel("m"), openaicompat.WithMaxRetries(-1)},
		"no scheme":        {openaicompat.WithModel("m"), openaicompat.WithBaseURL("localhost:11434/v1")},
		"other scheme":     {openaicompat.WithModel("m"), openaicompat.WithBaseURL("ftp://example.com/v1")},
		"no host":          {openaicompat.WithModel("m"), openaicompat.WithBaseURL("http:///v1")},
		"credentials":      {openaicompat.WithModel("m"), openaicompat.WithBaseURL("https://user:pass@example.com/v1")},
		"unparsable":       {openaicompat.WithModel("m"), openaicompat.WithBaseURL("http://[::1/v1")},
	} {
		if _, err := openaicompat.New(opts...); !errors.Is(err, llm.ErrInvalidRequest) {
			t.Errorf("%s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
}
