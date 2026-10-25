package gemini_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/gemini"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// representative is a request with every kind of content: a system prompt,
// tools (one without a schema), a tool-use turn with a thought, a signed
// call and two results (one failed) followed by text, a signed text, a
// signature with nothing before it, an unknown part, other providers'
// opaque blocks (one making up a whole assistant message), a user message of
// two texts, and options.
func representative() llm.Request {
	return llm.Request{
		System: "You are a careful assistant.",
		Tools: []tool.Definition{
			{
				Name:        "search",
				Description: "Search the news.",
				InputSchema: json.RawMessage(`{"type":"object","properties":{"q":{"type":"string","maxLength":200}},"required":["q"],"additionalProperties":false}`),
			},
			{Name: "now"},
		},
		Messages: []llm.Message{
			llm.User(llm.Text{Text: "Find Go news."}),
			llm.Assistant(
				opaque(`{"text":"The user wants Go news & a time.","thought":true}`),
				llm.Text{Text: "Searching."},
				llm.ToolUse{ID: "call_1", Name: "search", Input: json.RawMessage(`{"q":"go <1.27>"}`)},
				opaque(`{"thoughtSignature":"c2lnMQ=="}`),
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
				opaque(`{"thoughtSignature":"c2lnMg=="}`),
			),
			llm.User(llm.Text{Text: "Thanks."}, llm.Text{Text: "Anything else?"}),
			llm.Assistant(opaque(`{"thoughtSignature":"c2lnMw=="}`), opaque(`{ "executableCode": {"language": "PYTHON", "code": "print(1)"} }`)),
			llm.User(llm.Text{Text: "And?"}),
			llm.Assistant(llm.Opaque{Provider: "anthropic", Data: json.RawMessage(`{"type":"redacted_thinking","data":"abc"}`)}),
			llm.User(llm.Text{Text: "Summarize it."}),
		},
		MaxTokens: 1024,
		Provider:  gemini.Options{ThinkingLevel: "high"},
	}
}

// TestRequestBodyGolden checks the URL, the key and the body of a
// representative request: the content mapping, signatures back on their
// parts, tools, the output limit and the thinking level, byte for byte
// against testdata/request.golden.json.
func TestRequestBodyGolden(t *testing.T) {
	t.Parallel()
	rp, srv := newReplay(t, "testdata/conformance/text.http")
	req := representative()
	snapshot := req.Clone()
	if _, err := llm.Collect(newModel(t, srv).Generate(t.Context(), req)); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	sent := rp.sent()[0]
	if want := "/v1beta/models/gemini-replay:streamGenerateContent?alt=sse"; sent.url != want {
		t.Errorf("URL = %q, want %q", sent.url, want)
	}
	if got := sent.header.Get("X-Goog-Api-Key"); got != "test-key" {
		t.Errorf("X-Goog-Api-Key = %q, want the API key", got)
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, sent.body, "", "  "); err != nil {
		t.Fatalf("request body is not JSON: %v\n%s", err, sent.body)
	}
	pretty.WriteByte('\n')
	testkit.Golden(t, "testdata/request.golden.json", pretty.Bytes())
	ja, _ := json.Marshal(req.Messages)
	jb, _ := json.Marshal(snapshot.Messages)
	if !bytes.Equal(ja, jb) {
		t.Error("Generate modified its request")
	}
}

// body sends req to a model made with opts and returns the request body the
// server received, decoded.
func body(t *testing.T, req llm.Request, opts ...gemini.Option) map[string]any {
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

// TestOptions checks how options and output limits reach generationConfig:
// request over model defaults, value or pointer.
func TestOptions(t *testing.T) {
	t.Parallel()
	hi := userText("Hi.")
	with := func(p any, maxTokens int) llm.Request {
		r := hi
		r.Provider, r.MaxTokens = p, maxTokens
		return r
	}
	var nilOptions *gemini.Options
	cases := []struct {
		name string
		req  llm.Request
		opts []gemini.Option
		want string // generationConfig, "" for none
	}{
		{"defaults", with(nil, 0), nil, ""},
		{"request limit", with(nil, 50), []gemini.Option{gemini.WithMaxTokens(10)}, `{"maxOutputTokens":50}`},
		{"model limit", with(nil, 0), []gemini.Option{gemini.WithMaxTokens(10)}, `{"maxOutputTokens":10}`},
		{"level from the model", with(nil, 0), []gemini.Option{gemini.WithOptions(gemini.Options{ThinkingLevel: "low"})}, `{"thinkingConfig":{"thinkingLevel":"low"}}`},
		{
			"level from the request wins", with(&gemini.Options{ThinkingLevel: "high", IncludeThoughts: true}, 0),
			[]gemini.Option{gemini.WithOptions(gemini.Options{ThinkingLevel: "low"})},
			`{"thinkingConfig":{"includeThoughts":true,"thinkingLevel":"high"}}`,
		},
		{"thoughts from the model", with(gemini.Options{}, 0), []gemini.Option{gemini.WithOptions(gemini.Options{IncludeThoughts: true})}, `{"thinkingConfig":{"includeThoughts":true}}`},
		{"nil pointer means none", with(nilOptions, 0), []gemini.Option{gemini.WithOptions(gemini.Options{ThinkingLevel: "low"})}, `{"thinkingConfig":{"thinkingLevel":"low"}}`},
		{"another adapter's options are ignored", with(struct{ Effort string }{"max"}, 0), nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, ok := body(t, c.req, c.opts...)["generationConfig"]
			if c.want == "" {
				if ok {
					t.Errorf("generationConfig = %v, want none", got)
				}
				return
			}
			if b, _ := json.Marshal(got); string(b) != c.want {
				t.Errorf("generationConfig = %s, want %s", b, c.want)
			}
		})
	}
}

// TestSignatureRoundTrip checks that a reply's blocks, sent back as the
// history, give the parts the API sent: the signature on its call.
func TestSignatureRoundTrip(t *testing.T) {
	t.Parallel()
	m, tr := streamModel(t,
		chunks(parts("", `{"functionCall":{"name":"echo","args":{"text":"hi"},"id":"c1"},"thoughtSignature":"c2ln"}`), parts("STOP", `{"text":""}`)),
		chunks(parts("STOP", `{"text":"hi"}`)))
	req := userText("Echo hi.")
	first, err := llm.Collect(m.Generate(t.Context(), req))
	if err != nil {
		t.Fatal(err)
	}
	req.Messages = append(req.Messages, first.Message, llm.User(llm.ToolResult{ToolUseID: "c1", Content: "hi"}))
	if _, err := llm.Collect(m.Generate(t.Context(), req)); err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Contents []json.RawMessage `json:"contents"`
	}
	if err := json.Unmarshal(tr.sent[1], &sent); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`{"role":"user","parts":[{"text":"Echo hi."}]}`,
		`{"role":"model","parts":[{"functionCall":{"id":"c1","name":"echo","args":{"text":"hi"}},"thoughtSignature":"c2ln"}]}`,
		`{"role":"user","parts":[{"functionResponse":{"id":"c1","name":"echo","response":{"output":"hi"}}}]}`,
	}
	if len(sent.Contents) != len(want) {
		t.Fatalf("contents = %s", tr.sent[1])
	}
	for i, c := range sent.Contents {
		if string(c) != want[i] {
			t.Errorf("content %d = %s, want %s", i, c, want[i])
		}
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
		"only empty text":            {Messages: []llm.Message{llm.User(llm.Text{})}},
		"tool use from the user":     {Messages: []llm.Message{llm.User(llm.ToolUse{ID: "c", Name: "n"})}},
		"tool result from the model": {Messages: []llm.Message{user, llm.Assistant(llm.ToolResult{ToolUseID: "c"}), user}},
		"unknown block":              {Messages: []llm.Message{llm.User(llm.Unknown{Type: "image", Data: json.RawMessage(`{"type":"image"}`)})}},
		"Gemini part from the user":  {Messages: []llm.Message{llm.User(opaque(`{"thoughtSignature":"x"}`))}},
		"result of no call":          {Messages: []llm.Message{llm.User(llm.ToolResult{ToolUseID: "c", Content: "x"})}},
		"tool use without a name":    {Messages: []llm.Message{user, llm.Assistant(llm.ToolUse{ID: "c"}), user}},
		"invalid tool input":         {Messages: []llm.Message{user, llm.Assistant(llm.ToolUse{ID: "c", Name: "n", Input: json.RawMessage(`{"a":`)}), user}},
		"Gemini block not an object": {Messages: []llm.Message{user, llm.Assistant(opaque(`["x"]`)), user}},
		"signature not a string":     {Messages: []llm.Message{user, llm.Assistant(llm.Text{Text: "a"}, opaque(`{"thoughtSignature":1}`)), user}},
		"schema not an object":       {Messages: []llm.Message{user}, Tools: []tool.Definition{{Name: "t", InputSchema: json.RawMessage(`[1]`)}}},
		"negative max tokens":        {Messages: []llm.Message{user}, MaxTokens: -1},
		"deferred tools":             {Messages: []llm.Message{user}, Tools: []tool.Definition{{Name: "t"}, {Name: "u"}}, Deferred: []string{"u"}},
		"invalid thinking level":     {Messages: []llm.Message{user}, Provider: gemini.Options{ThinkingLevel: "Very High"}},
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
	for name, opts := range map[string][]gemini.Option{
		"model with a slash": {gemini.WithModel("models/../x")},
		"model with a colon": {gemini.WithModel("gemini:generateContent")},
		"negative limit":     {gemini.WithMaxTokens(-1)},
		"negative idle":      {gemini.WithIdleTimeout(-1)},
		"invalid defaults":   {gemini.WithOptions(gemini.Options{ThinkingLevel: "!"})},
		"negative retries":   {gemini.WithMaxRetries(-1)},
		"no scheme":          {gemini.WithBaseURL("localhost:8080/v1beta")},
		"other scheme":       {gemini.WithBaseURL("ftp://example.com/v1beta")},
		"no host":            {gemini.WithBaseURL("http:///v1beta")},
		"credentials":        {gemini.WithBaseURL("https://user:pass@example.com/v1beta")},
		"query":              {gemini.WithBaseURL("https://example.com/v1beta?key=x")},
		"unparsable":         {gemini.WithBaseURL("http://[::1/v1beta")},
	} {
		if _, err := gemini.New(opts...); !errors.Is(err, llm.ErrInvalidRequest) {
			t.Errorf("%s: err = %v, want ErrInvalidRequest", name, err)
		}
	}
}
