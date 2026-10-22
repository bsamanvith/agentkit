package anthropic_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/llm"
	anthropicllm "github.com/bsamanvith/agentkit/pkg/llm/anthropic"
)

// drain reads a stream to its end.
func drain(seq iter.Seq2[llm.Event, error]) ([]llm.Event, error) {
	var events []llm.Event
	for ev, err := range seq {
		if err != nil {
			return events, err
		}
		events = append(events, ev)
	}
	return events, nil
}

func userText(s string) llm.Request {
	return llm.Request{Messages: []llm.Message{llm.User(llm.Text{Text: s})}}
}

func TestThinkingBlocksRoundTrip(t *testing.T) {
	t.Parallel()
	rp, srv := newReplay(t, fixtures(t, "", "thinking")...)
	m := newModel(t, srv)
	req := userText("What's new in Go?")
	first, err := llm.Collect(m.Generate(t.Context(), req))
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}
	// What the API streamed, decoded: the thinking block with its deltas
	// applied (and a field the SDK does not model), and the redacted block.
	want := []map[string]any{
		{
			"type":         "thinking",
			"thinking":     "The user wants <b>news</b> & dates — I’ll search.",
			"signature":    "ErUBCkYIBxgCKkCx0vq+/9z==",
			"future_field": map[string]any{"z": 1.0, "a": []any{true, nil}},
		},
		{"type": "redacted_thinking", "data": "EmwKAhgBEgy3va3pzix/LafPsn4aDFIT2Xlx"},
	}
	var opaque []llm.Opaque
	for _, b := range first.Message.Content {
		if o, ok := b.(llm.Opaque); ok {
			opaque = append(opaque, o)
		}
	}
	if len(opaque) != len(want) {
		t.Fatalf("opaque blocks = %d, want %d: %#v", len(opaque), len(want), first.Message.Content)
	}
	for i, o := range opaque {
		var got map[string]any
		if o.Provider != "anthropic" || json.Unmarshal(o.Data, &got) != nil || !reflect.DeepEqual(got, want[i]) {
			t.Errorf("opaque block %d = %s (provider %q), want %v", i, o.Data, o.Provider, want[i])
		}
	}

	req.Messages = append(req.Messages, first.Message,
		llm.User(llm.ToolResult{ToolUseID: "toolu_rt", Content: "Go 1.27 is out."}))
	if _, err := llm.Collect(m.Generate(t.Context(), req)); err != nil {
		t.Fatalf("second turn: %v", err)
	}
	sent := rp.sent()[1].body
	for i, o := range opaque {
		if !bytes.Contains(sent, o.Data) {
			t.Errorf("next request does not contain opaque block %d byte for byte:\n%s\nrequest: %s", i, o.Data, sent)
		}
	}
	var next struct {
		Messages []struct {
			Content []map[string]any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(sent, &next); err != nil {
		t.Fatal(err)
	}
	if got := next.Messages[1].Content[:2]; !reflect.DeepEqual(got, want) {
		t.Errorf("assistant turn sent back starts with\n %v\nwant\n %v", got, want)
	}
}

func TestRefusalStopReason(t *testing.T) {
	t.Parallel()
	_, srv := newReplay(t, "testdata/refusal_cyber.http")
	got, err := llm.Collect(newModel(t, srv).Generate(t.Context(), userText("Hello.")))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	want := llm.StopReason{Kind: llm.StopRefusal, Category: "cyber", Explanation: "This request was declined by a safety classifier."}
	if got.Stop != want {
		t.Errorf("stop = %+v, want %+v", got.Stop, want)
	}
	if len(got.Message.Content) != 0 {
		t.Errorf("content = %#v, want none", got.Message.Content)
	}
}

func TestInterleavedBlocksStreamOneAtATime(t *testing.T) {
	t.Parallel()
	_, srv := newReplay(t, "testdata/interleaved.http")
	events, err := drain(newModel(t, srv).Generate(t.Context(), userText("Count.")))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	call := llm.ToolUse{ID: "toolu_i", Name: "echo", Input: json.RawMessage(`{"text":"x"}`)}
	want := []llm.Event{
		{Kind: llm.EventTextDelta, Index: 0, Text: "One, "},
		{Kind: llm.EventTextDelta, Index: 0, Text: "two."},
		{Kind: llm.EventBlockDone, Index: 0, Block: llm.Text{Text: "One, two."}},
		{Kind: llm.EventToolUseStart, Index: 1, Block: llm.ToolUse{ID: "toolu_i", Name: "echo"}},
		{Kind: llm.EventToolInputDelta, Index: 1, Text: `{"text":`},
		{Kind: llm.EventToolInputDelta, Index: 1, Text: `"x"}`},
		{Kind: llm.EventBlockDone, Index: 1, Block: call},
		{Kind: llm.EventTextDelta, Index: 2, Text: "Three."},
		{Kind: llm.EventBlockDone, Index: 2, Block: llm.Text{Text: "Three."}},
	}
	if len(events) != len(want)+1 || !reflect.DeepEqual(events[:len(want)], want) {
		t.Fatalf("events:\n got %#v\nwant %#v", events, want)
	}
	done := events[len(want)]
	if done.Kind != llm.EventMessageDone ||
		!reflect.DeepEqual(done.Response.Message, llm.Assistant(llm.Text{Text: "One, two."}, call, llm.Text{Text: "Three."})) {
		t.Errorf("final event = %#v", done)
	}
}

func TestStreamCutShortIsOverloaded(t *testing.T) {
	t.Parallel()
	_, srv := newReplay(t, "testdata/cut_short.http")
	events, err := drain(newModel(t, srv).Generate(t.Context(), userText("Hello.")))
	if !errors.Is(err, llm.ErrOverloaded) || !llm.IsRetryable(err) {
		t.Errorf("err = %v, want a retryable ErrOverloaded", err)
	}
	if len(events) == 0 {
		t.Error("no event before the stream broke off")
	}
}

// A block start the API sent without a block is a garbled stream, not an
// empty Opaque block (whose data would not be JSON).
func TestBlockStartWithoutBlockIsOverloaded(t *testing.T) {
	t.Parallel()
	for name, start := range map[string]string{
		"missing":  `{"type":"content_block_start","index":0}`,
		"null":     `{"type":"content_block_start","index":0,"content_block":null}`,
		"no type":  `{"type":"content_block_start","index":0,"content_block":{}}`,
		"no index": `{"type":"content_block_start"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			body := "event:content_block_start\ndata:" + start + "\n\n" +
				"event:message_stop\ndata:{\"type\":\"message_stop\"}\n\n"
			m, err := anthropicllm.New(
				anthropicllm.WithBaseURL("http://replay.invalid"),
				anthropicllm.WithHTTPClient(&http.Client{Transport: sseBody(body)}),
				anthropicllm.WithClientOptions(option.WithAPIKey("test-key"), option.WithMaxRetries(0)),
			)
			if err != nil {
				t.Fatal(err)
			}
			events, err := drain(m.Generate(t.Context(), userText("Hello.")))
			if !errors.Is(err, llm.ErrOverloaded) {
				t.Errorf("err = %v, want ErrOverloaded; events %#v", err, events)
			}
		})
	}
}

func TestRetriesStayOn(t *testing.T) {
	t.Parallel()
	rp, srv := newReplay(t, fixtures(t, "", "retry")...)
	// The SDK's default retry policy: only the key is set.
	m, err := anthropicllm.New(anthropicllm.WithBaseURL(srv.URL),
		anthropicllm.WithClientOptions(option.WithAPIKey("test-key")))
	if err != nil {
		t.Fatal(err)
	}
	got, err := llm.Collect(m.Generate(t.Context(), userText("Hello.")))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	sent := rp.sent()
	if got.Message.Text() != "Recovered." || len(sent) != 2 {
		t.Fatalf("reply %q after %d requests, want the retried reply after 2", got.Message.Text(), len(sent))
	}
	if !bytes.Equal(sent[0].body, sent[1].body) {
		t.Errorf("the retry sent a different body:\n%s\n%s", sent[0].body, sent[1].body)
	}
}

func TestFallbackLeavesOutDeclinedThinking(t *testing.T) {
	t.Parallel()
	rp, srv := newReplay(t, "testdata/fallback.http", "testdata/conformance/text.http")
	m := newModel(t, srv)
	req := userText("Explain the exploit.")
	first, err := llm.Collect(m.Generate(t.Context(), req))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	var types []string
	for _, b := range first.Message.Content {
		switch b := b.(type) {
		case llm.Opaque:
			var head struct{ Type, Signature string }
			_ = json.Unmarshal(b.Data, &head)
			types = append(types, head.Type+":"+head.Signature)
		case llm.Text:
			types = append(types, "text:"+b.Text)
		}
	}
	want := []string{"thinking:Sig-declined==", "text:Partial ", "fallback:", "thinking:Sig-fallback==", "text:answer."}
	if !reflect.DeepEqual(types, want) {
		t.Fatalf("reply blocks = %q, want %q", types, want)
	}

	req.Messages = append(req.Messages, first.Message, llm.User(llm.Text{Text: "Go on."}))
	if _, err := llm.Collect(m.Generate(t.Context(), req)); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	sent := rp.sent()[1].body
	if bytes.Contains(sent, []byte("Sig-declined==")) {
		t.Error("the declined attempt's thinking block was sent back")
	}
	for _, keep := range []string{`"Partial "`, `"type":"fallback"`, "Sig-fallback=="} {
		if !bytes.Contains(sent, []byte(keep)) {
			t.Errorf("request lacks %s: %s", keep, sent)
		}
	}
}

func TestErrorMapping(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name    string
		fixture string
		want    error
		status  int
	}{
		{"429", "testdata/conformance/error_rate_limited.http", llm.ErrRateLimited, 429},
		{"529", "testdata/conformance/error_overloaded.http", llm.ErrOverloaded, 529},
		{"400", "testdata/conformance/error_invalid_request.http", llm.ErrInvalidRequest, 400},
		{"400 prompt too long", "testdata/conformance/error_context_too_long.http", llm.ErrContextTooLong, 400},
		{"401", "testdata/conformance/error_auth.http", llm.ErrAuth, 401},
		{"403", "testdata/errors/permission.http", llm.ErrAuth, 403},
		{"404", "testdata/errors/not_found.http", llm.ErrInvalidRequest, 404},
		{"413", "testdata/errors/too_large.http", llm.ErrContextTooLong, 413},
		{"500", "testdata/errors/api_error.http", llm.ErrOverloaded, 500},
		{"503 without a body", "testdata/errors/unavailable.http", llm.ErrOverloaded, 503},
		{"error event mid-stream", "testdata/conformance/error_mid_stream.http", llm.ErrOverloaded, 200},
		{"rate limit event mid-stream", "testdata/errors/mid_stream_rate_limit.http", llm.ErrRateLimited, 200},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, srv := newReplay(t, c.fixture)
			_, err := llm.Collect(newModel(t, srv).Generate(t.Context(), userText("Hello.")))
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			var apiErr *anthropic.Error
			if !errors.As(err, &apiErr) || apiErr.StatusCode != c.status {
				t.Errorf("errors.As(*anthropic.Error) = %v, want the SDK error with status %d", apiErr, c.status)
			}
		})
	}
}

// stall answers like the API, sends a ping, then keeps the stream open
// without another byte until the client goes away or the test ends.
func stall(t *testing.T) *httptest.Server {
	t.Helper()
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "event: ping\ndata: {\"type\": \"ping\"}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-done:
		}
	}))
	t.Cleanup(func() { close(done); srv.Close() })
	return srv
}

// A stream that stays open but stops sending is given up after the idle
// timeout, as a retryable error, not the caller's cancellation.
func TestStalledStreamIsOverloaded(t *testing.T) {
	t.Parallel()
	m := newModel(t, stall(t), anthropicllm.WithIdleTimeout(50*time.Millisecond))
	_, err := drain(m.Generate(t.Context(), userText("Hello.")))
	if !errors.Is(err, llm.ErrOverloaded) || !llm.IsRetryable(err) {
		t.Errorf("err = %v, want a retryable ErrOverloaded", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, reported as a cancellation", err)
	}
}

// The caller's own cancellation stays a cancellation, whatever the idle
// timeout.
func TestCancelledStallIsCanceled(t *testing.T) {
	t.Parallel()
	m := newModel(t, stall(t), anthropicllm.WithIdleTimeout(time.Hour))
	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(20*time.Millisecond, cancel)
	_, err := drain(m.Generate(ctx, userText("Hello.")))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// Events keep a stream alive: the timeout is between events, not for the
// whole reply. The idle clock is a fake one the server moves: once each text
// delta has reached the caller and the stream waits again, by just under the
// timeout, so the reply takes three times as long as it.
func TestIdleTimeoutIsBetweenEvents(t *testing.T) {
	t.Parallel()
	const idle = time.Second
	clk := clocktest.NewFake(time.Time{})
	seen := make(chan struct{})
	events := []string{
		`{"type":"message_start","message":{"id":"msg_idle","type":"message","role":"assistant","model":"claude-opus-5-5","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":1}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"One, "}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"two, "}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"three."}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":6}}`,
		`{"type":"message_stop"}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, data := range events {
			var ev struct{ Type string }
			_ = json.Unmarshal([]byte(data), &ev)
			_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, data)
			w.(http.Flusher).Flush()
			if !strings.Contains(data, `"text_delta"`) {
				continue
			}
			select {
			case <-seen:
			case <-r.Context().Done():
				return
			}
			// The caller has the delta, and its time does not count: the
			// timeout runs again once the stream waits for the next event.
			if clk.WaitForTimers(r.Context(), 1) != nil {
				return
			}
			clk.Advance(idle - time.Millisecond)
		}
	}))
	t.Cleanup(srv.Close)
	m := newModel(t, srv, anthropicllm.WithIdleTimeout(idle), anthropicllm.WithClock(clk))
	deltas := 0
	for ev, err := range m.Generate(t.Context(), userText("Hello.")) {
		if err != nil {
			t.Fatalf("Generate after %d text deltas: %v", deltas, err)
		}
		if ev.Kind == llm.EventTextDelta {
			deltas++
			seen <- struct{}{}
		}
	}
	if deltas != 3 {
		t.Errorf("got %d text deltas, want 3", deltas)
	}
}

// TestIdleTimeoutIgnoresTheConsumer: a caller slower than the idle timeout
// to handle each event is not a stalled stream.
func TestIdleTimeoutIgnoresTheConsumer(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := os.ReadFile("testdata/interleaved.http")
		if err != nil {
			t.Error(err)
			return
		}
		_, body, _ := bytes.Cut(data, []byte("\n\n"))
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	// The idle clock is a fake one that moves only while the caller has an
	// event, and by more than the timeout each time.
	const idle = time.Second
	clk := clocktest.NewFake(time.Time{})
	m := newModel(t, srv, anthropicllm.WithIdleTimeout(idle), anthropicllm.WithClock(clk))
	for _, err := range m.Generate(t.Context(), userText("Hello.")) {
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		clk.Advance(2 * idle)
	}
}

func TestNegativeIdleTimeoutIsInvalid(t *testing.T) {
	t.Parallel()
	_, err := anthropicllm.New(anthropicllm.WithIdleTimeout(-time.Second))
	if !errors.Is(err, llm.ErrInvalidRequest) {
		t.Errorf("err = %v, want ErrInvalidRequest", err)
	}
}
