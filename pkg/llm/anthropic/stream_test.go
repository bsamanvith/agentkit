package anthropic_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

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

func TestTruncatedToolCallIsDropped(t *testing.T) {
	t.Parallel()
	_, srv := newReplay(t, "testdata/tool_truncated.http")
	events, err := drain(newModel(t, srv).Generate(t.Context(), userText("Write a.txt.")))
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	for _, ev := range events {
		if ev.Kind == llm.EventToolUseStart || ev.Kind == llm.EventToolInputDelta {
			t.Errorf("event %v for a tool call that was cut off", ev.Kind)
		}
	}
	got := events[len(events)-1].Response
	want := llm.Response{
		Message: llm.Assistant(llm.Text{Text: "Writing the file."}),
		Stop:    llm.StopReason{Kind: llm.StopMaxTokens},
		Usage:   llm.Usage{InputTokens: 10, OutputTokens: 64},
	}
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("response = %#v, want %#v", *got, want)
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
// whole reply.
func TestIdleTimeoutIsBetweenEvents(t *testing.T) {
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
		// Each of the 14 events arrives after a pause well under the
		// timeout; all of them together take longer than it.
		for _, ev := range bytes.SplitAfter(body, []byte("\n\n")) {
			time.Sleep(100 * time.Millisecond)
			_, _ = w.Write(ev)
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(srv.Close)
	m := newModel(t, srv, anthropicllm.WithIdleTimeout(800*time.Millisecond))
	if _, err := llm.Collect(m.Generate(t.Context(), userText("Hello."))); err != nil {
		t.Fatalf("Generate: %v", err)
	}
}

func TestNegativeIdleTimeoutIsInvalid(t *testing.T) {
	t.Parallel()
	_, err := anthropicllm.New(anthropicllm.WithIdleTimeout(-time.Second))
	if !errors.Is(err, llm.ErrInvalidRequest) {
		t.Errorf("err = %v, want ErrInvalidRequest", err)
	}
}
