package openaicompat_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/openai/openai-go/v3/option"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/openaicompat"
)

// sse is a transport that answers every request with a 200 event stream
// holding the next of its bodies, and records the request bodies.
type sse struct {
	mu     sync.Mutex
	bodies []string
	sent   [][]byte
}

func (s *sse) RoundTrip(r *http.Request) (*http.Response, error) {
	var sent []byte
	if r.Body != nil {
		sent, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	body := ""
	if n := len(s.sent); n < len(s.bodies) {
		body = s.bodies[n]
	}
	s.sent = append(s.sent, sent)
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}, nil
}

// streamModel returns a model whose server answers with the given event
// streams, one per request, written as data lines (see chunks).
func streamModel(t testing.TB, bodies ...string) (*openaicompat.Model, *sse) {
	t.Helper()
	tr := &sse{bodies: bodies}
	m, err := openaicompat.New(
		openaicompat.WithModel("gpt-replay"),
		openaicompat.WithBaseURL("https://replay.invalid/v1"),
		openaicompat.WithHTTPClient(&http.Client{Transport: tr}),
		openaicompat.WithClientOptions(option.WithAPIKey("test-key"), option.WithMaxRetries(0)),
	)
	if err != nil {
		t.Fatal(err)
	}
	return m, tr
}

// chunks makes an event stream of chunks, each given as the JSON of its
// first choice (or, starting with `{"usage"` or `{"error"`, of the whole
// chunk), ending with [DONE].
func chunks(choices ...string) string {
	var b strings.Builder
	for _, c := range choices {
		if strings.HasPrefix(c, `{"usage"`) || strings.HasPrefix(c, `{"error"`) {
			b.WriteString("data: " + c + "\n\n")
			continue
		}
		b.WriteString(`data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[` + c + "]}\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

// generate runs one request against bodies and checks the stream.
func generate(t *testing.T, bodies ...string) (llm.Response, error) {
	t.Helper()
	m, _ := streamModel(t, bodies...)
	events, err := drain(m.Generate(t.Context(), userText("Hello.")))
	if err != nil {
		return llm.Response{}, err
	}
	checkEvents(t, events)
	return *events[len(events)-1].Response, nil
}

// TestToolCalls covers the ways servers stream tool calls: parallel calls
// with interleaved fragments, text after a call, parallel calls that share an
// index (told apart by their IDs), a call in one chunk with no ID and
// finish_reason "stop" (older Ollama), and a call without arguments.
func TestToolCalls(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		stream string
		want   llm.Response
	}{
		{
			"interleaved", chunks(
				`{"index":0,"delta":{"content":"Two calls."}}`,
				`{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","type":"function","function":{"name":"echo","arguments":""}}]}}`,
				`{"index":0,"delta":{"tool_calls":[{"index":1,"id":"b","type":"function","function":{"name":"now","arguments":"{\"tz\""}}]}}`,
				`{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"text\":"}}]}}`,
				`{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":":\"UTC\"}"}}]}}`,
				`{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"hi\"}"}}]}}`,
				`{"index":0,"delta":{"content":"Done."}}`,
				`{"index":0,"delta":{},"finish_reason":"tool_calls"}`),
			llm.Response{
				Message: llm.Assistant(llm.Text{Text: "Two calls."}, llm.Text{Text: "Done."},
					llm.ToolUse{ID: "a", Name: "echo", Input: json.RawMessage(`{"text":"hi"}`)},
					llm.ToolUse{ID: "b", Name: "now", Input: json.RawMessage(`{"tz":"UTC"}`)}),
				Stop: llm.StopReason{Kind: llm.StopToolUse},
			},
		},
		{
			"same index, new ID", chunks(
				`{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"echo","arguments":"{\"text\":\"1\"}"}}]}}`,
				`{"index":0,"delta":{"tool_calls":[{"index":0,"id":"b","function":{"name":"echo","arguments":"{\"text\":\"2\"}"}}]}}`,
				`{"index":0,"delta":{},"finish_reason":"tool_calls"}`),
			llm.Response{
				Message: llm.Assistant(
					llm.ToolUse{ID: "a", Name: "echo", Input: json.RawMessage(`{"text":"1"}`)},
					llm.ToolUse{ID: "b", Name: "echo", Input: json.RawMessage(`{"text":"2"}`)}),
				Stop: llm.StopReason{Kind: llm.StopToolUse},
			},
		},
		{
			"no ID, stop", chunks(
				`{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"echo","arguments":"{\"text\":\"x\"}"}}]}}`,
				`{"index":0,"delta":{},"finish_reason":"stop"}`),
			llm.Response{
				Message: llm.Assistant(llm.ToolUse{ID: "call_0", Name: "echo", Input: json.RawMessage(`{"text":"x"}`)}),
				Stop:    llm.StopReason{Kind: llm.StopToolUse},
			},
		},
		{
			"no arguments", chunks(
				`{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"now"}}]}}`,
				`{"index":0,"delta":{},"finish_reason":"tool_calls"}`),
			llm.Response{
				Message: llm.Assistant(llm.ToolUse{ID: "a", Name: "now", Input: json.RawMessage(`{}`)}),
				Stop:    llm.StopReason{Kind: llm.StopToolUse},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, err := generate(t, c.stream)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("response:\n got %#v\nwant %#v", got, c.want)
			}
		})
	}
}

// TestTruncatedToolCallIsDropped checks that a call cut off by the token
// limit (its arguments are not valid JSON) is left out, with max_tokens.
func TestTruncatedToolCallIsDropped(t *testing.T) {
	t.Parallel()
	got, err := generate(t, chunks(
		`{"index":0,"delta":{"content":"Calling."}}`,
		`{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"echo","arguments":"{\"text\":\"a long"}}]}}`,
		`{"index":0,"delta":{},"finish_reason":"length"}`))
	if err != nil {
		t.Fatal(err)
	}
	want := llm.Response{Message: llm.Assistant(llm.Text{Text: "Calling."}), Stop: llm.StopReason{Kind: llm.StopMaxTokens}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("response = %#v, want %#v", got, want)
	}
}

// TestGarbledToolCallIsOverloaded checks that a finished call whose
// arguments are not JSON, or that has no name, fails the stream, retryably.
func TestGarbledToolCallIsOverloaded(t *testing.T) {
	t.Parallel()
	for name, stream := range map[string]string{
		"invalid arguments": chunks(
			`{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"echo","arguments":"{text: hi}"}}]}}`,
			`{"index":0,"delta":{},"finish_reason":"tool_calls"}`),
		"no name": chunks(
			`{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","function":{"arguments":"{}"}}]}}`,
			`{"index":0,"delta":{},"finish_reason":"tool_calls"}`),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := generate(t, stream); !errors.Is(err, llm.ErrOverloaded) {
				t.Errorf("err = %v, want ErrOverloaded", err)
			}
		})
	}
}

// TestReasoningRoundTrip checks that reasoning streamed in a reasoning field
// (Ollama's name for it) comes back as one Opaque block ahead of the text,
// and goes back in the next request in that field.
func TestReasoningRoundTrip(t *testing.T) {
	t.Parallel()
	m, tr := streamModel(t,
		chunks(
			`{"index":0,"delta":{"role":"assistant","content":"","reasoning":"Think <a>"}}`,
			`{"index":0,"delta":{"content":"","reasoning":" & b."}}`,
			`{"index":0,"delta":{"content":"Answer."}}`,
			`{"index":0,"delta":{},"finish_reason":"stop"}`),
		chunks(`{"index":0,"delta":{"content":"Again."}}`, `{"index":0,"delta":{},"finish_reason":"stop"}`))
	req := userText("Hello.")
	first, err := llm.Collect(m.Generate(t.Context(), req))
	if err != nil {
		t.Fatal(err)
	}
	reasoning := llm.Opaque{Provider: openaicompat.Provider, Data: json.RawMessage(`{"reasoning":"Think <a> & b."}`)}
	want := llm.Assistant(reasoning, llm.Text{Text: "Answer."})
	if !reflect.DeepEqual(first.Message, want) {
		t.Fatalf("message = %#v, want %#v", first.Message, want)
	}
	req.Messages = append(req.Messages, first.Message, llm.User(llm.Text{Text: "More."}))
	if _, err := llm.Collect(m.Generate(t.Context(), req)); err != nil {
		t.Fatal(err)
	}
	var sent struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(tr.sent[1], &sent); err != nil {
		t.Fatal(err)
	}
	if got := sent.Messages[1]; got["reasoning"] != "Think <a> & b." || got["content"] != "Answer." {
		t.Errorf("assistant message sent back = %v", got)
	}
}

// TestUsage checks the usage mapping: cached tokens are not input, cache
// writes as OpenAI and LiteLLM report them, and a server that sends no usage.
func TestUsage(t *testing.T) {
	t.Parallel()
	end := `{"index":0,"delta":{"content":"Hi."},"finish_reason":"stop"}`
	for name, c := range map[string]struct {
		usage string
		want  llm.Usage
	}{
		"cache read": {
			`{"usage":{"prompt_tokens":100,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":64}},"choices":[]}`,
			llm.Usage{InputTokens: 36, OutputTokens: 7, CacheReadTokens: 64},
		},
		"LiteLLM cache write": {
			`{"usage":{"prompt_tokens":100,"completion_tokens":7,"cache_creation_input_tokens":30,"prompt_tokens_details":{"cached_tokens":50}},"choices":[]}`,
			llm.Usage{InputTokens: 20, OutputTokens: 7, CacheReadTokens: 50, CacheWriteTokens: 30},
		},
		"no usage":     {"", llm.Usage{}},
		"null usage":   {`{"usage":null,"choices":[]}`, llm.Usage{}},
		"inconsistent": {`{"usage":{"prompt_tokens":10,"completion_tokens":-3,"prompt_tokens_details":{"cached_tokens":64}},"choices":[]}`, llm.Usage{CacheReadTokens: 64}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			parts := []string{end}
			if c.usage != "" {
				parts = append(parts, c.usage)
			}
			got, err := generate(t, chunks(parts...))
			if err != nil {
				t.Fatal(err)
			}
			if got.Usage != c.want {
				t.Errorf("usage = %+v, want %+v", got.Usage, c.want)
			}
		})
	}
}

// TestStopReasons checks the finish reasons the conformance suite does not
// cover.
func TestStopReasons(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		last string
		want llm.StopReason
	}{
		"function_call":             {`{"index":0,"delta":{},"finish_reason":"function_call"}`, llm.StopReason{Kind: llm.StopToolUse}},
		"refusal without a filter":  {`{"index":0,"delta":{"refusal":"No."},"finish_reason":"stop"}`, llm.StopReason{Kind: llm.StopRefusal, Explanation: "No."}},
		"filter without categories": {`{"index":0,"delta":{},"finish_reason":"content_filter"}`, llm.StopReason{Kind: llm.StopRefusal}},
		"two filtered categories": {
			`{"index":0,"delta":{},"finish_reason":"content_filter","content_filter_results":{"violence":{"filtered":true},"hate":{"filtered":true},"sexual":{"filtered":false}}}`,
			llm.StopReason{Kind: llm.StopRefusal, Category: "hate,violence"},
		},
		"unknown": {`{"index":0,"delta":{},"finish_reason":"eos"}`, llm.StopReason{Kind: llm.StopOther, Raw: "eos"}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := generate(t, chunks(`{"index":0,"delta":{"content":"x"}}`, c.last))
			if err != nil {
				t.Fatal(err)
			}
			if got.Stop != c.want {
				t.Errorf("stop = %+v, want %+v", got.Stop, c.want)
			}
		})
	}
}

// TestOtherChoicesIgnored checks that only the first choice makes the reply.
func TestOtherChoicesIgnored(t *testing.T) {
	t.Parallel()
	got, err := generate(t, chunks(
		`{"index":1,"delta":{"content":"other"}},{"index":0,"delta":{"content":"mine"}}`,
		`{"index":1,"delta":{},"finish_reason":"length"},{"index":0,"delta":{},"finish_reason":"stop"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Message.Text() != "mine" || got.Stop.Kind != llm.StopEndTurn {
		t.Errorf("response = %#v", got)
	}
}

// TestStreamCutShortIsOverloaded checks streams that end without a
// finish_reason: with [DONE], without it, and empty.
func TestStreamCutShortIsOverloaded(t *testing.T) {
	t.Parallel()
	for name, stream := range map[string]string{
		"done":    chunks(`{"index":0,"delta":{"content":"Hel"}}`),
		"no done": strings.TrimSuffix(chunks(`{"index":0,"delta":{"content":"Hel"}}`), "data: [DONE]\n\n"),
		"empty":   "",
		"garbled": "data: {\"choices\":[{\"index\":0,\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := generate(t, stream); !errors.Is(err, llm.ErrOverloaded) {
				t.Errorf("err = %v, want ErrOverloaded", err)
			}
		})
	}
}

// checkEvents checks a successful stream: blocks in order, deltas that add
// up to their blocks, and a final message made of the finished blocks.
func checkEvents(t *testing.T, events []llm.Event) {
	t.Helper()
	var (
		blocks []llm.Block
		text   []byte
	)
	if len(events) == 0 {
		t.Fatal("no events")
	}
	for i, ev := range events[:len(events)-1] {
		if ev.Index != len(blocks) {
			t.Fatalf("event %d: index %d, want %d", i, ev.Index, len(blocks))
		}
		switch ev.Kind {
		case llm.EventTextDelta, llm.EventToolInputDelta:
			text = append(text, ev.Text...)
		case llm.EventToolUseStart:
			text = nil
		case llm.EventBlockDone:
			switch b := ev.Block.(type) {
			case llm.Text:
				if b.Text != string(text) {
					t.Fatalf("event %d: text %q, deltas %q", i, b.Text, text)
				}
			case llm.ToolUse:
				if !json.Valid(b.Input) || !bytes.Equal(b.Input, text) && (len(text) != 0 || string(b.Input) != "{}") {
					t.Fatalf("event %d: tool input %q, deltas %q", i, b.Input, text)
				}
			case llm.Opaque:
				if !json.Valid(b.Data) || b.Provider != openaicompat.Provider {
					t.Fatalf("event %d: opaque block %#v", i, b)
				}
			}
			blocks = append(blocks, ev.Block)
			text = nil
		default:
			t.Fatalf("event %d: kind %v", i, ev.Kind)
		}
	}
	last := events[len(events)-1]
	if last.Kind != llm.EventMessageDone || last.Response.Stop.Kind == "" {
		t.Fatalf("last event = %#v", last)
	}
	if len(blocks) != 0 && !reflect.DeepEqual(last.Response.Message.Content, blocks) {
		t.Fatalf("final content %#v, blocks %#v", last.Response.Message.Content, blocks)
	}
}
