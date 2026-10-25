package gemini_test

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

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/gemini"
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
// streams, one per request (see chunks).
func streamModel(t testing.TB, bodies ...string) (*gemini.Model, *sse) {
	t.Helper()
	tr := &sse{bodies: bodies}
	m, err := gemini.New(
		gemini.WithModel("gemini-replay"),
		gemini.WithBaseURL("https://replay.invalid/v1beta"),
		gemini.WithHTTPClient(&http.Client{Transport: tr}),
		gemini.WithAPIKey("test-key"), gemini.WithMaxRetries(0),
	)
	if err != nil {
		t.Fatal(err)
	}
	return m, tr
}

// chunks writes an event stream of the given chunks (JSON objects).
func chunks(cs ...string) string {
	var b strings.Builder
	for _, c := range cs {
		b.WriteString("data: " + c + "\r\n\r\n")
	}
	return b.String()
}

// parts is a chunk of one candidate with the given parts (JSON objects)
// and finish reason (none if empty).
func parts(finish string, ps ...string) string {
	c := `{"candidates":[{"content":{"parts":[` + strings.Join(ps, ",") + `],"role":"model"},"index":0`
	if finish != "" {
		c += `,"finishReason":"` + finish + `"`
	}
	return c + `}]}`
}

// generate runs one request against the given streams and checks the
// events of a successful one.
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

func opaque(data string) llm.Opaque {
	return llm.Opaque{Provider: gemini.Provider, Data: json.RawMessage(data)}
}

// TestBlocks checks how parts become blocks: text merged across chunks,
// signatures after the block they sign, thoughts, empty and unknown parts.
func TestBlocks(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		stream string
		want   []llm.Block
		stop   llm.StopKind
	}{
		{
			"text, then its signature on an empty part",
			chunks(parts("", `{"text":"pine"}`), parts("", `{"text":"apple"}`), parts("STOP", `{"text":"","thoughtSignature":"c2ln"}`)),
			[]llm.Block{llm.Text{Text: "pineapple"}, opaque(`{"thoughtSignature":"c2ln"}`)},
			llm.StopEndTurn,
		},
		{
			"signed text ends its block",
			chunks(parts("", `{"text":"a","thoughtSignature":"czE="}`), parts("STOP", `{"text":"b"}`)),
			[]llm.Block{llm.Text{Text: "a"}, opaque(`{"thoughtSignature":"czE="}`), llm.Text{Text: "b"}},
			llm.StopEndTurn,
		},
		{
			"a signed call, then an empty part",
			chunks(parts("", `{"functionCall":{"name":"echo","args":{"text":"<hi>"},"id":"call_9"},"thoughtSignature":"c2ln"}`), parts("STOP", `{"text":""}`)),
			[]llm.Block{llm.ToolUse{ID: "call_9", Name: "echo", Input: json.RawMessage(`{"text":"<hi>"}`)}, opaque(`{"thoughtSignature":"c2ln"}`)},
			llm.StopToolUse,
		},
		{
			"two calls in one chunk, without args",
			chunks(parts("STOP", `{"functionCall":{"name":"now","id":"a"}}`, `{"functionCall":{"name":"now","args":{},"id":"b"}}`)),
			[]llm.Block{llm.ToolUse{ID: "a", Name: "now", Input: json.RawMessage(`{}`)}, llm.ToolUse{ID: "b", Name: "now", Input: json.RawMessage(`{}`)}},
			llm.StopToolUse,
		},
		{
			"thoughts merged, then text",
			chunks(parts("", `{"text":"2 < 3","thought":true}`), parts("", `{"text":" & so","thought":true,"thoughtSignature":"dA=="}`, `{"text":"Yes."}`), parts("STOP")),
			[]llm.Block{opaque(`{"text":"2 < 3 & so","thought":true,"thoughtSignature":"dA=="}`), llm.Text{Text: "Yes."}},
			llm.StopEndTurn,
		},
		{
			"a signature with no text before it is a part of its own",
			chunks(parts("STOP", `{"text": "", "thoughtSignature": "c2ln"}`)),
			[]llm.Block{opaque(`{"text":"","thoughtSignature":"c2ln"}`)},
			llm.StopEndTurn,
		},
		{
			"an unknown part is kept",
			chunks(parts("", `{"text":"Running."}`), parts("STOP", `{"executableCode": {"language": "PYTHON", "code": "print(1)"}}`)),
			[]llm.Block{llm.Text{Text: "Running."}, opaque(`{"executableCode":{"language":"PYTHON","code":"print(1)"}}`)},
			llm.StopEndTurn,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, err := generate(t, c.stream)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Message.Content, c.want) {
				t.Errorf("content:\n got %#v\nwant %#v", got.Message.Content, c.want)
			}
			if got.Stop.Kind != c.stop {
				t.Errorf("stop = %+v, want %s", got.Stop, c.stop)
			}
		})
	}
}

// TestCallWithoutID checks that a call the API sends without an ID gets one.
func TestCallWithoutID(t *testing.T) {
	t.Parallel()
	got, err := generate(t, chunks(parts("STOP", `{"functionCall":{"name":"now","args":{}}}`, `{"functionCall":{"name":"now","args":{}}}`)))
	if err != nil {
		t.Fatal(err)
	}
	a, b := got.Message.Content[0].(llm.ToolUse), got.Message.Content[1].(llm.ToolUse)
	if !strings.HasPrefix(a.ID, "call_") || a.ID == b.ID {
		t.Errorf("IDs %q and %q, want two different call_ IDs", a.ID, b.ID)
	}
}

// TestUsage checks the token counts: cached tokens out of the input, thoughts
// in the output, the last chunk's counts.
func TestUsage(t *testing.T) {
	t.Parallel()
	got, err := generate(t, chunks(
		`{"candidates":[{"content":{"parts":[{"text":"Hi"}]},"index":0}],"usageMetadata":{"promptTokenCount":5}}`,
		`{"candidates":[{"content":{"parts":[{"text":"."}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":60,"toolUsePromptTokenCount":7,"candidatesTokenCount":9,"thoughtsTokenCount":21,"totalTokenCount":137}}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	want := llm.Usage{InputTokens: 47, OutputTokens: 30, CacheReadTokens: 60}
	if got.Usage != want {
		t.Errorf("usage = %+v, want %+v", got.Usage, want)
	}
}

// TestStopReasons checks the finish reasons and a blocked prompt.
func TestStopReasons(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		stream string
		want   llm.StopReason
	}{
		{"max tokens", chunks(parts("MAX_TOKENS", `{"text":"cut o"}`)), llm.StopReason{Kind: llm.StopMaxTokens}},
		{
			"safety", chunks(`{"candidates":[{"content":{"parts":[]},"finishReason":"SAFETY","finishMessage":"Blocked.","safetyRatings":[{"category":"HARM_CATEGORY_HARASSMENT","probability":"HIGH","blocked":true},{"category":"HARM_CATEGORY_HATE_SPEECH","probability":"LOW"}],"index":0}]}`),
			llm.StopReason{Kind: llm.StopRefusal, Category: "HARM_CATEGORY_HARASSMENT", Explanation: "Blocked."},
		},
		{"recitation", chunks(parts("RECITATION")), llm.StopReason{Kind: llm.StopRefusal, Category: "RECITATION"}},
		{
			"prompt blocked", chunks(`{"promptFeedback":{"blockReason":"PROHIBITED_CONTENT","blockReasonMessage":"No."},"usageMetadata":{"promptTokenCount":4}}`),
			llm.StopReason{Kind: llm.StopRefusal, Category: "PROHIBITED_CONTENT", Explanation: "No."},
		},
		{"other", chunks(parts("OTHER", `{"text":"x"}`)), llm.StopReason{Kind: llm.StopOther, Raw: "OTHER"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got, err := generate(t, c.stream)
			if err != nil {
				t.Fatal(err)
			}
			if got.Stop != c.want {
				t.Errorf("stop = %+v, want %+v", got.Stop, c.want)
			}
		})
	}
}

// TestFailedStreams checks streams that cannot be made into a reply: they
// are transient failures, so a retry samples again.
func TestFailedStreams(t *testing.T) {
	t.Parallel()
	for name, stream := range map[string]string{
		"no finish reason":  chunks(parts("", `{"text":"Hel"}`)),
		"empty":             "",
		"garbled":           "data: {\"candidates\":[{\"index\":0,\n\n",
		"garbled part":      chunks(parts("STOP", `"text"`)),
		"call without name": chunks(parts("STOP", `{"functionCall":{"args":{}}}`)),
		"malformed call":    chunks(`{"candidates":[{"content":{"parts":[]},"finishReason":"MALFORMED_FUNCTION_CALL","finishMessage":"Malformed function call: print(x","index":0}]}`),
		"unexpected call":   chunks(parts("UNEXPECTED_TOOL_CALL")),
		"error in stream":   chunks(parts("", `{"text":"Hel"}`), `{"error":{"code":503,"message":"The model is overloaded.","status":"UNAVAILABLE"}}`),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := generate(t, stream); !errors.Is(err, llm.ErrOverloaded) {
				t.Errorf("err = %v, want ErrOverloaded", err)
			}
		})
	}
}

// TestOtherCandidatesIgnored checks that only the first candidate is read.
func TestOtherCandidatesIgnored(t *testing.T) {
	t.Parallel()
	got, err := generate(t, chunks(`{"candidates":[{"content":{"parts":[{"text":"other"}]},"index":1},{"content":{"parts":[{"text":"Hi"}]},"finishReason":"STOP","index":0}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got.Message.Text() != "Hi" {
		t.Errorf("text = %q, want Hi", got.Message.Text())
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
				if !json.Valid(b.Input) || !bytes.Equal(b.Input, text) {
					t.Fatalf("event %d: tool input %q, deltas %q", i, b.Input, text)
				}
			case llm.Opaque:
				if !json.Valid(b.Data) || b.Provider != gemini.Provider {
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
