package agentclient_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
)

// TestStreamedRunFollowUpAndJSON: against the real server, a streamed run
// yields exactly the events the server wrote, in order, ending with
// run_done; a follow-up in the same conversation sends the model the first
// run's messages; and JSON mode returns the same run GET /v1/runs/{id} does.
func TestStreamedRunFollowUpAndJSON(t *testing.T) {
	t.Parallel()
	r := newRig(t, &model{replies: []llmtest.Reply{
		llmtest.ToolCalls(call("toolu_1", "echo", `{"text":"hi"}`)),
		llmtest.Text("It said [a] hi."),
		llmtest.Text("Still hi."),
		llmtest.Text("Hi, as JSON."),
	}})
	ctx := t.Context()

	first, err := r.client.Stream(ctx, agentclient.RunRequest{Message: "Echo hi."})
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	runID, convID := first.RunID(), first.ConversationID() // from the headers, before any event
	got := readAll(first)
	want := expectedRead(referenceSSE(r.serverWrote(runID)))
	if diff := sameRead(got, want); diff != "" {
		t.Fatalf("first run: %s", diff)
	}
	types := map[string]bool{}
	for _, e := range got.events {
		types[e.Type] = true
	}
	if len(got.events) < 6 || got.events[0].Type != wire.TypeRunStart || !types[wire.TypeToolResult] || got.events[len(got.events)-1].Type != wire.TypeRunDone {
		t.Fatalf("first run's events %s: want run_start, a tool call, ..., run_done", jsonOf(got.events))
	}
	if runID == "" || runID != got.events[0].RunID || convID != got.events[0].ConversationID {
		t.Errorf("stream says run %q in %q, run_start says %q in %q", runID, convID, got.events[0].RunID, got.events[0].ConversationID)
	}
	if end := got.events[len(got.events)-1]; end.Answer != "It said [a] hi." {
		t.Errorf("answer %q", end.Answer)
	}

	second, err := r.client.Stream(ctx, agentclient.RunRequest{Message: "Again?", ConversationID: convID})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	got2 := readAll(second)
	if diff := sameRead(got2, expectedRead(referenceSSE(r.serverWrote(second.RunID())))); diff != "" {
		t.Fatalf("follow-up: %s", diff)
	}
	if end := got2.events[len(got2.events)-1]; second.ConversationID() != convID || end.Type != wire.TypeRunDone || end.Answer != "Still hi." {
		t.Errorf("follow-up in %q ended with %s", second.ConversationID(), jsonOf(end))
	}
	conv, err := r.client.GetConversation(ctx, convID)
	if err != nil {
		t.Fatal(err)
	}
	var announced []wire.TimedMessage // what both streams said the conversation got
	for _, e := range append(got.events, got2.events...) {
		switch e.Type {
		case wire.TypeRunStart:
			announced = append(announced, *e.Prompt)
		case wire.TypeTurnDone:
			announced = append(announced, e.Record.Messages...)
		}
	}
	if conv.Status != wire.StatusIdle || !jsonEqual(conv.Messages, announced) {
		t.Errorf("conversation %s, want idle with\n%s", jsonOf(conv), jsonOf(announced))
	}
	reqs := r.model.requests()
	if last := reqs[len(reqs)-1].Messages; len(last) != len(conv.Messages)-1 || last[0].Text() != "Echo hi." {
		t.Errorf("the follow-up sent the model %d messages, want the conversation's first %d", len(last), len(conv.Messages)-1)
	}

	run, err := r.client.Run(ctx, agentclient.RunRequest{Message: "As JSON.", ConversationID: convID})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := r.client.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != wire.StatusDone || run.Answer != "Hi, as JSON." || run.ConversationID != convID || !jsonEqual(run, stored) {
		t.Errorf("JSON run %s, GET says %s", jsonOf(run), jsonOf(stored))
	}
	streamed, err := r.client.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if end := got.events[len(got.events)-1]; streamed.Status != wire.StatusDone || streamed.Answer != end.Answer || streamed.Usage != *end.Usage || streamed.Turns != end.Turns {
		t.Errorf("streamed run %s disagrees with its run_done %s", jsonOf(streamed), jsonOf(end))
	}
}

// goldenStreams are T036's golden SSE files: what the server writes.
var goldenStreams = []string{"tool_run.sse", "refusal.sse"}

func readGolden(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "agentapi", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestGoldenStreams: the client reads T036's golden streams as the events
// their data lines are, byte for byte, and each re-encodes to the same line
// (client and server agree on the wire by the same fixtures).
func TestGoldenStreams(t *testing.T) {
	t.Parallel()
	for _, name := range goldenStreams {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			body := readGolden(t, name)
			var lines []string
			for line := range strings.SplitSeq(string(body), "\n") {
				if data, ok := strings.CutPrefix(line, "data: "); ok {
					lines = append(lines, data)
				}
			}
			s, _ := streamOf(t, body)
			got := readAll(s)
			if diff := sameRead(got, expectedRead(lines)); diff != "" {
				t.Fatal(diff)
			}
			for i, e := range got.events {
				if b, err := json.Marshal(e); err != nil || string(b) != lines[i] {
					t.Errorf("event %d re-encodes as\n%s\nwant\n%s", i, b, lines[i])
				}
			}
			end := got.events[len(got.events)-1]
			if name == "tool_run.sse" && (len(got.events) != 11 || end.Type != wire.TypeRunDone) ||
				name == "refusal.sse" && (end.Type != wire.TypeRunError || end.Error.Code != wire.CodeRefused) {
				t.Errorf("%s ends with %s after %d events", name, jsonOf(end), len(got.events))
			}
		})
	}
}

// TestSSEFraming: what the SSE standard allows reads as the same events:
// CRLF and CR line ends, data over several lines, comments, no space after
// the colon, no id or event field, a byte order mark, blank lines between
// events, unknown fields; and an event the stream cuts off is not read.
func TestSSEFraming(t *testing.T) {
	t.Parallel()
	start := `{"v":1,"type":"run_start","seq":0,"run_id":"r","conversation_id":"c"}`
	done := `{"v":1,"type":"run_done","seq":1,"run_id":"r","answer":"a"}`
	tests := []struct {
		name string
		body string
		want []string // the events' data
		err  error
	}{
		{"LF", "id: 0\nevent: run_start\ndata: " + start + "\n\nid: 1\nevent: run_done\ndata: " + done + "\n\n", []string{start, done}, io.EOF},
		{"CRLF", "id: 0\r\nevent: run_start\r\ndata: " + start + "\r\n\r\ndata: " + done + "\r\n\r\n", []string{start, done}, io.EOF},
		{"CR", "data: " + start + "\r\rdata: " + done + "\r\r", []string{start, done}, io.EOF},
		{"mixed line ends", "data: " + start + "\r\n\ndata: " + done + "\r\r\n", []string{start, done}, io.EOF},
		{
			"data over several lines",
			"data: {\"v\":1,\ndata:  \"type\":\"run_start\",\"seq\":0,\ndata: \"run_id\":\"r\"}\n\ndata: " + done + "\n\n",
			[]string{"{\"v\":1,\n \"type\":\"run_start\",\"seq\":0,\n\"run_id\":\"r\"}", done},
			io.EOF,
		},
		{
			"data over several lines, CRLF",
			"data: {\"v\":1,\r\ndata: \"type\":\"run_start\"}\r\n\r\ndata: " + done + "\r\n\r\n",
			[]string{"{\"v\":1,\n\"type\":\"run_start\"}", done},
			io.EOF,
		},
		{"comments", ": hello\n\ndata: " + start + "\n: keep-alive\n\n: keep-alive\n\ndata: " + done + "\n:\n\n", []string{start, done}, io.EOF},
		{"no space after the colon", "data:" + start + "\n\ndata:" + done + "\n\n", []string{start, done}, io.EOF},
		{"no id or event", "data: " + start + "\n\ndata: " + done + "\n\n", []string{start, done}, io.EOF},
		{"byte order mark", "\uFEFFdata: " + start + "\n\ndata: " + done + "\n\n", []string{start, done}, io.EOF},
		{"blank lines", "\n\n\ndata: " + start + "\n\n\n\ndata: " + done + "\n\n", []string{start, done}, io.EOF},
		{"unknown fields", "retry: 10\nfoo: bar\nevent: something_else\ndata: " + start + "\nid\n\ndata: " + done + "\n\n", []string{start, done}, io.EOF},
		{"event without its blank line", "data: " + start + "\n\ndata: " + done + "\n", []string{start}, agentclient.ErrStreamTruncated},
		{"line cut off", "data: " + start + "\n\ndata: " + done, []string{start}, agentclient.ErrStreamTruncated},
		{"nothing", "", nil, agentclient.ErrStreamTruncated},
		{"only comments", ": keep-alive\n\n", nil, agentclient.ErrStreamTruncated},
		{"data that is not JSON", "data: " + start + "\n\ndata: hello\n\n", []string{start}, agentclient.ErrBadResponse},
		{"an event without a type", "data: {\"v\":1,\"seq\":0}\n\n", nil, agentclient.ErrBadResponse},
		{"null", "data: null\n\n", nil, agentclient.ErrBadResponse},
		{"events after the end are not read", "data: " + done + "\n\ndata: " + start + "\n\n", []string{done}, io.EOF},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, _ := streamOf(t, []byte(tt.body))
			got := readAll(s)
			if endsAs(got.err, tt.err) != "" || len(got.raw) != len(tt.want) || strings.Join(got.raw, "\x00") != strings.Join(tt.want, "\x00") {
				t.Errorf("read %q, then %v; want %q, then %v", got.raw, got.err, tt.want, tt.err)
			}
			if diff := sameRead(got, expectedRead(referenceSSE([]byte(tt.body)))); diff != "" {
				t.Errorf("disagrees with the reference reading: %s", diff)
			}
			if _, err := s.Next(); endsAs(err, tt.err) != "" {
				t.Errorf("Next after the end = %v, want %v again", err, tt.err)
			}
		})
	}
}

// TestLineLimit: a line or an event's data over the limit fails the stream
// with ErrResponseTooLarge, after reading at most the limit and the read
// buffer past what came before; events before it are read.
func TestLineLimit(t *testing.T) {
	t.Parallel()
	start := `{"v":1,"type":"run_start","seq":0,"run_id":"r"}`
	tests := []struct {
		name string
		head string
		tail string // repeated for ever
	}{
		{"a line with no end", "data: " + start + "\n\n", "x"},
		{"data over many lines", "data: " + start + "\n\n", "data: xxxxxxxx\n"},
		{"a comment with no end", "data: " + start + "\n\n", ":"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			const limit = 1000
			s, bs := streamFrom(t, func() io.Reader {
				return io.MultiReader(strings.NewReader(tt.head), endless(tt.tail))
			}, agentclient.WithMaxResponseBytes(limit))
			got := readAll(s)
			if len(got.raw) != 1 || got.raw[0] != start || endsAs(got.err, agentclient.ErrResponseTooLarge) != "" {
				t.Errorf("read %q, then %v; want the first event, then ErrResponseTooLarge", got.raw, got.err)
			}
			if n := bs.read.Load(); n > int64(len(tt.head)+limit+readSlack) {
				t.Errorf("read %d bytes, want at most %d", n, len(tt.head)+limit+readSlack)
			}
		})
	}
}

// readSlack is how far past the limit the client may read: its read buffer.
const readSlack = 64 << 10

// endless repeats s for ever.
func endless(s string) io.Reader { return &repeater{s: s} }

type repeater struct {
	s string
	i int
}

func (r *repeater) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.s[r.i%len(r.s)]
		r.i++
	}
	return len(p), nil
}

// TestUnknownEventsAndFieldsPassThrough: the additive rule, on T036's golden
// streams. With unknown fields added at every level of every event and of its
// messages and blocks, a block of a new type in every message (as in T036's
// TestEventJSONIsAdditive), unknown SSE fields, and events of an unknown type
// in between, the client reads the same known events (each new block kept as
// an llm.Unknown block), passes the unknown events through with their raw
// data, and ends only at run_done or run_error.
func TestUnknownEventsAndFieldsPassThrough(t *testing.T) {
	t.Parallel()
	for _, name := range goldenStreams {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var body strings.Builder
			var want read
			for line := range strings.SplitSeq(string(readGolden(t, name)), "\n") {
				data, ok := strings.CutPrefix(line, "data: ")
				if !ok {
					continue
				}
				var known wire.Event
				if err := json.Unmarshal([]byte(data), &known); err != nil {
					t.Fatal(err)
				}
				unknown := fmt.Sprintf(`{"v":1,"type":"thinking_delta","seq":%d,"run_id":%q,"at":"2026-10-11T12:00:00Z","thinking":"hm","nested":{"a":[1]}}`, known.Seq, known.RunID)
				grown := grow(t, data)
				for _, m := range eventMessages(&known) {
					withNewBlock(m)
				}
				fmt.Fprintf(&body, "retry: 1000\nevent: thinking_delta\ndata: %s\n\nid: %d\nevent: %s\nx-new-field: 1\ndata: %s\n\n", unknown, known.Seq, known.Type, grown)
				var u wire.Event
				if err := json.Unmarshal([]byte(unknown), &u); err != nil {
					t.Fatal(err)
				}
				want.events = append(want.events, u, known)
				want.raw = append(want.raw, unknown, grown)
			}
			want.err = io.EOF
			s, _ := streamOf(t, []byte(body.String()))
			if diff := sameRead(readAll(s), want); diff != "" {
				t.Error(diff)
			}
		})
	}
}

// grow adds unknown fields to an event and to its objects, and grows its
// messages (growMessages), as T036's TestEventJSONIsAdditive does.
func grow(t *testing.T, data string) string {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal([]byte(data), &obj); err != nil {
		t.Fatal(err)
	}
	addUnknown(obj)
	for _, key := range []string{"record", "error", "usage"} {
		if sub, ok := obj[key].(map[string]any); ok {
			addUnknown(sub)
		}
	}
	growMessages(obj)
	b, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// newBlock is a content block of a type a later release may add, and
// newBlockJSON its JSON as json.Marshal writes it inside a grown event.
var (
	newBlock = map[string]any{
		"type": "citation_card", "url": "https://example.com/<a>",
		"added_in_a_later_release": map[string]any{"nested": []any{1.0, "two"}},
	}
	newBlockJSON, _ = json.Marshal(newBlock)
)

// growMessages adds to every message under v (an object with "role" and
// "content") unknown fields, on the message and on each of its blocks, and
// newBlock as its first block; and unknown fields to every timed message (an
// object with "at" and "message").
func growMessages(v any) {
	switch v := v.(type) {
	case map[string]any:
		content, isMessage := v["content"].([]any)
		if _, ok := v["role"]; ok && isMessage {
			for _, b := range content {
				if b, ok := b.(map[string]any); ok {
					addUnknown(b)
				}
			}
			v["content"] = append([]any{newBlock}, content...)
			addUnknown(v)
			return
		}
		for _, x := range v {
			growMessages(x)
		}
		_, hasAt := v["at"]
		if _, ok := v["message"]; ok && hasAt {
			addUnknown(v)
		}
	case []any:
		for _, x := range v {
			growMessages(x)
		}
	}
}

// withNewBlock adds to m what reading a grown message gives: newBlock, kept
// as an llm.Unknown block.
func withNewBlock(m *llm.Message) {
	m.Content = append([]llm.Block{llm.Unknown{Type: "citation_card", Data: newBlockJSON}}, m.Content...)
}

// eventMessages are the messages in e.
func eventMessages(e *wire.Event) []*llm.Message {
	var ms []*llm.Message
	if e.Prompt != nil {
		ms = append(ms, &e.Prompt.Message)
	}
	if e.Record != nil {
		for i := range e.Record.Messages {
			ms = append(ms, &e.Record.Messages[i].Message)
		}
	}
	if e.Final != nil {
		ms = append(ms, e.Final)
	}
	return ms
}

func addUnknown(obj map[string]any) {
	obj["added_in_a_later_release"] = map[string]any{"nested": []any{1, "two"}}
	obj["another_new_flag"] = true
}

// TestCloseCancelsTheRun: closing a stream before its last event, or ending
// its context, cancels the run on the server: the run ends failed with
// "cancelled", no further model call starts, and Next reports why it stopped.
func TestCloseCancelsTheRun(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		after string // the event to read before stopping; "" for none
		stop  func(s *agentclient.Stream, cancel context.CancelFunc)
		err   error
	}{
		{"close after the first turn", wire.TypeTurnDone, func(s *agentclient.Stream, _ context.CancelFunc) { _ = s.Close() }, agentclient.ErrClosed},
		{"close before any event", "", func(s *agentclient.Stream, _ context.CancelFunc) { _ = s.Close() }, agentclient.ErrClosed},
		{"context ended", wire.TypeTurnDone, func(_ *agentclient.Stream, cancel context.CancelFunc) { cancel() }, context.Canceled},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// The first reply calls a tool; the second waits until the run
			// is cancelled.
			m := &model{replies: []llmtest.Reply{llmtest.ToolCalls(call("toolu_1", "echo", `{"text":"x"}`)), llmtest.Text("never sent")}, gate: make(chan struct{}), pass: 1}
			if tt.after == "" {
				m.pass = 0
			}
			r := newRig(t, m)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			s, err := r.client.Stream(ctx, agentclient.RunRequest{Message: "echo x"})
			if err != nil {
				t.Fatal(err)
			}
			if tt.after != "" {
				until(t, s, tt.after)
			}
			tt.stop(s, cancel)
			for range 2 {
				if _, err := s.Next(); endsAs(err, tt.err) != "" {
					t.Errorf("Next after stopping: %s", endsAs(err, tt.err))
				}
			}
			run := r.waitEnded(s.RunID())
			if run.Status != wire.StatusFailed || run.Error == nil || run.Error.Code != wire.CodeCancelled {
				t.Errorf("run = %s, want failed, cancelled", jsonOf(run))
			}
			if n := len(m.requests()); n > m.pass+1 {
				t.Errorf("%d model calls after the run was cancelled at call %d", n, m.pass+1)
			}
		})
	}
}

// TestNextAfterClose: after Close, Next reads nothing more, not even events
// the client has already received; Close can be called again.
func TestNextAfterClose(t *testing.T) {
	t.Parallel()
	s, _ := streamOf(t, readGolden(t, "tool_run.sse")) // read at once, so buffered
	if e, err := s.Next(); err != nil || e.Type != wire.TypeRunStart {
		t.Fatalf("first event %s, %v", jsonOf(e), err)
	}
	for range 2 {
		if err := s.Close(); err != nil {
			t.Errorf("Close = %v", err)
		}
		if e, err := s.Next(); endsAs(err, agentclient.ErrClosed) != "" {
			t.Errorf("Next after Close = %s, %v; want ErrClosed", jsonOf(e), err)
		}
	}
}

// TestStreamCutBeforeItsEnd: a stream that ends, or breaks, before run_done
// or run_error gives ErrStreamTruncated after the events it had.
func TestStreamCutBeforeItsEnd(t *testing.T) {
	t.Parallel()
	events := "id: 0\nevent: run_start\ndata: {\"v\":1,\"type\":\"run_start\",\"seq\":0,\"run_id\":\"r\",\"conversation_id\":\"c\"}\n\n" +
		"id: 1\nevent: text_delta\ndata: {\"v\":1,\"type\":\"text_delta\",\"seq\":1,\"run_id\":\"r\",\"turn\":1,\"text\":\"Hel\"}\n\n"
	for _, tt := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"the response ends", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, events)
		}},
		{"the connection breaks", func(w http.ResponseWriter, _ *http.Request) {
			conn, buf, err := http.NewResponseController(w).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			fmt.Fprintf(buf, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n", len(events), events)
			_ = buf.Flush()
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ts := httptest.NewServer(tt.handler)
			defer ts.Close()
			c, err := agentclient.New(ts.URL, agentclient.WithHTTPClient(ts.Client()))
			if err != nil {
				t.Fatal(err)
			}
			s, err := c.Stream(t.Context(), agentclient.RunRequest{Message: "hi"})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			got := readAll(s)
			if len(got.events) != 2 || endsAs(got.err, agentclient.ErrStreamTruncated) != "" {
				t.Errorf("read %d events, then %v; want 2, then ErrStreamTruncated", len(got.events), got.err)
			}
			if s.RunID() != "r" || s.ConversationID() != "c" {
				t.Errorf("without the headers, the stream says run %q in %q, want run_start's r in c", s.RunID(), s.ConversationID())
			}
		})
	}
}

// sentinels are the errors.Is targets of the API's error codes.
var sentinels = map[string]error{
	wire.CodeInvalidRequest: agentclient.ErrInvalidRequest,
	wire.CodeUnauthorized:   agentclient.ErrUnauthorized,
	wire.CodeNotFound:       agentclient.ErrNotFound,
	wire.CodeTooLarge:       agentclient.ErrRequestTooLarge,
	wire.CodeBusy:           agentclient.ErrConversationBusy,
	wire.CodeInterrupted:    agentclient.ErrConversationInterrupted,
	wire.CodeAgentMismatch:  agentclient.ErrAgentMismatch,
	wire.CodeOverCapacity:   agentclient.ErrOverCapacity,
	wire.CodeShuttingDown:   agentclient.ErrShuttingDown,
}

// checkAPIError checks that err is the API error of code, with status and
// Retry-After, and matches the sentinel of its code and no other.
func checkAPIError(t *testing.T, err error, status int, code string, retryAfter time.Duration) {
	t.Helper()
	var ae *agentclient.APIError
	if !errors.As(err, &ae) {
		t.Fatalf("error %v (%T), want an *APIError", err, err)
	}
	if ae.Status != status || ae.Code != code || ae.RetryAfter != retryAfter || ae.Message == "" {
		t.Errorf("APIError %+v, want status %d, code %q, Retry-After %s, a message", ae, status, code, retryAfter)
	}
	for c, s := range sentinels {
		if errors.Is(err, s) != (c == code) {
			t.Errorf("errors.Is(%v, the sentinel of %s) = %t", err, c, c != code)
		}
	}
}

// TestAPIErrors: every HTTP error code of the API, produced by the real
// server, is an *APIError that matches its sentinel, with Retry-After on the
// 503s; from every method that can get it.
func TestAPIErrors(t *testing.T) {
	t.Parallel()
	// The subtests run in order, not in parallel, so that the last can check
	// that every code was produced.
	ctx := t.Context()
	seen := map[string]bool{}
	check := func(t *testing.T, err error, status int, code string, retryAfter time.Duration) {
		t.Helper()
		seen[code] = true
		checkAPIError(t, err, status, code, retryAfter)
	}
	// gated is a rig whose runs wait at the model until the test ends.
	gated := func(t *testing.T, api ...agentapi.Option) *rig {
		return newRig(t, &model{replies: []llmtest.Reply{llmtest.Text("a"), llmtest.Text("b")}, gate: make(chan struct{})}, api...)
	}
	// running starts a run that waits at the model.
	running := func(t *testing.T, r *rig) *agentclient.Stream {
		s, err := r.client.Stream(ctx, agentclient.RunRequest{Message: "wait"})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		until(t, s, wire.TypeRunStart)
		return s
	}
	t.Run("invalid_request", func(t *testing.T) {
		r := newRig(t, &model{})
		_, err := r.client.Stream(ctx, agentclient.RunRequest{Message: " "})
		check(t, err, http.StatusBadRequest, wire.CodeInvalidRequest, 0)
		_, err = r.client.Run(ctx, agentclient.RunRequest{})
		check(t, err, http.StatusBadRequest, wire.CodeInvalidRequest, 0)
	})
	t.Run("unauthorized", func(t *testing.T) {
		r := newRig(t, &model{})
		for _, c := range []*agentclient.Client{r.newClient(), r.newClient(agentclient.WithToken(testToken + "x"))} {
			_, err := c.Stream(ctx, agentclient.RunRequest{Message: "hi"})
			check(t, err, http.StatusUnauthorized, wire.CodeUnauthorized, 0)
			_, err = c.GetConversation(ctx, "conv_1")
			check(t, err, http.StatusUnauthorized, wire.CodeUnauthorized, 0)
		}
	})
	t.Run("not_found", func(t *testing.T) {
		r := newRig(t, &model{})
		_, err := r.client.GetRun(ctx, "run_nope")
		check(t, err, http.StatusNotFound, wire.CodeNotFound, 0)
		_, err = r.client.GetConversation(ctx, "../runs/x")
		check(t, err, http.StatusNotFound, wire.CodeNotFound, 0)
		_, err = r.client.Stream(ctx, agentclient.RunRequest{Message: "hi", ConversationID: "conv_nope"})
		check(t, err, http.StatusNotFound, wire.CodeNotFound, 0)
	})
	t.Run("too_large", func(t *testing.T) {
		r := newRig(t, &model{}, agentapi.WithMaxBodyBytes(100))
		_, err := r.client.Stream(ctx, agentclient.RunRequest{Message: strings.Repeat("x", 100)})
		check(t, err, http.StatusRequestEntityTooLarge, wire.CodeTooLarge, 0)
	})
	t.Run("conversation_busy", func(t *testing.T) {
		r := gated(t)
		s := running(t, r)
		_, err := r.client.Stream(ctx, agentclient.RunRequest{Message: "and", ConversationID: s.ConversationID()})
		check(t, err, http.StatusConflict, wire.CodeBusy, 0)
	})
	t.Run("conversation_interrupted", func(t *testing.T) {
		r := gated(t)
		s := running(t, r)
		_ = s.Close()
		r.waitEnded(s.RunID())
		_, err := r.client.Run(ctx, agentclient.RunRequest{Message: "and", ConversationID: s.ConversationID()})
		check(t, err, http.StatusConflict, wire.CodeInterrupted, 0)
	})
	t.Run("agent_mismatch", func(t *testing.T) {
		r := newRig(t, &model{replies: []llmtest.Reply{llmtest.Text("a")}})
		run, err := r.client.Run(ctx, agentclient.RunRequest{Message: "hi"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = r.client.Run(ctx, agentclient.RunRequest{Message: "and", ConversationID: run.ConversationID, Agent: "other"})
		check(t, err, http.StatusConflict, wire.CodeAgentMismatch, 0)
	})
	t.Run("over_capacity", func(t *testing.T) {
		r := gated(t, agentapi.WithMaxConcurrentRuns(1))
		running(t, r)
		_, err := r.client.Stream(ctx, agentclient.RunRequest{Message: "another"})
		check(t, err, http.StatusServiceUnavailable, wire.CodeOverCapacity, 2*time.Second)
		_, err = r.client.Run(ctx, agentclient.RunRequest{Message: "another"})
		check(t, err, http.StatusServiceUnavailable, wire.CodeOverCapacity, 2*time.Second)
	})
	t.Run("shutting_down", func(t *testing.T) {
		r := newRig(t, &model{})
		if err := r.srv.Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
		_, err := r.client.Stream(ctx, agentclient.RunRequest{Message: "hi"})
		check(t, err, http.StatusServiceUnavailable, wire.CodeShuttingDown, 2*time.Second)
	})
	t.Run("every code", func(t *testing.T) {
		for code := range sentinels {
			if !seen[code] {
				t.Errorf("code %s not produced", code)
			}
		}
	})
}

// TestErrorsFromElsewhere: error responses the server does not write (a
// proxy's page, a code from a later release, Retry-After as a date) are
// still *APIError, matching no sentinel but the right one.
func TestErrorsFromElsewhere(t *testing.T) {
	t.Parallel()
	date := "Mon, 12 Oct 2026 10:00:00 GMT"
	for _, tt := range []struct {
		name   string
		status int
		header http.Header
		body   string
		code   string
		retry  time.Duration
	}{
		{"a proxy's page", http.StatusBadGateway, http.Header{"Content-Type": {"text/html"}}, "<html>502 Bad Gateway</html>", "", 0},
		{"an unknown code", http.StatusTeapot, nil, `{"v":1,"error":{"code":"brand_new","message":"new"}}`, "brand_new", 0},
		{"Retry-After as a date", http.StatusServiceUnavailable, http.Header{"Date": {date}, "Retry-After": {"Mon, 12 Oct 2026 10:01:30 GMT"}}, `{"v":1,"error":{"code":"over_capacity","message":"busy"}}`, wire.CodeOverCapacity, 90 * time.Second},
		{"Retry-After in the past", http.StatusServiceUnavailable, http.Header{"Date": {date}, "Retry-After": {"Mon, 12 Oct 2026 09:00:00 GMT"}}, `{"v":1,"error":{"code":"shutting_down","message":"bye"}}`, wire.CodeShuttingDown, 0},
		{"Retry-After not a number", http.StatusServiceUnavailable, http.Header{"Retry-After": {"soon"}}, `{"v":1,"error":{"code":"shutting_down","message":"bye"}}`, wire.CodeShuttingDown, 0},
		{"Retry-After huge", http.StatusServiceUnavailable, http.Header{"Retry-After": {"99999999999999999"}}, `{"v":1,"error":{"code":"over_capacity","message":"x"}}`, wire.CodeOverCapacity, time.Duration(1<<63-1) / time.Second * time.Second},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			bs := &bodyServer{status: tt.status, header: tt.header, body: func() io.Reader { return strings.NewReader(tt.body) }}
			c, err := agentclient.New("http://agent.test", agentclient.WithHTTPClient(&http.Client{Transport: bs}))
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.Stream(t.Context(), agentclient.RunRequest{Message: "hi"})
			checkAPIError(t, err, tt.status, tt.code, tt.retry)
		})
	}
}

// TestRequests: what the client sends: the token, the User-Agent, the
// Accept that picks a stream or JSON, the body, under the base URL's path.
func TestRequests(t *testing.T) {
	t.Parallel()
	got := make(chan *http.Request, 16)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		got <- r
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"v":1,"error":{"code":"not_found","message":"x"}}`)
	}))
	defer ts.Close()
	c, err := agentclient.New(ts.URL+"/agent/", agentclient.WithHTTPClient(ts.Client()), agentclient.WithToken("tok"), agentclient.WithUserAgent("test-ua"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	_, _ = c.Stream(ctx, agentclient.RunRequest{Message: "hi", ConversationID: "conv_1"})
	_, _ = c.Run(ctx, agentclient.RunRequest{Message: "hi"})
	_, _ = c.GetRun(ctx, "run/1")
	_, _ = c.GetConversation(ctx, "conv 1")
	_, _ = c.Agents(ctx)
	_, _ = c.Agent(ctx, "a/b")
	_, _ = c.Conversations(ctx, agentclient.ConversationsQuery{})
	_, _ = c.Conversations(ctx, agentclient.ConversationsQuery{Agent: "a&b", Limit: 5, Cursor: "c+/="})
	_ = c.DeleteConversation(ctx, "conv/1")
	_, _ = c.Toolsets(ctx)
	_, _ = c.Tools(ctx, "web search")
	for _, want := range []struct{ method, path, accept, body string }{
		{"POST", "/agent/v1/runs", "text/event-stream", `{"message":"hi","conversation_id":"conv_1"}`},
		{"POST", "/agent/v1/runs", "application/json", `{"message":"hi"}`},
		{"GET", "/agent/v1/runs/run%2F1", "application/json", ""},
		{"GET", "/agent/v1/conversations/conv%201", "application/json", ""},
		{"GET", "/agent/v1/agents", "application/json", ""},
		{"GET", "/agent/v1/agents/a%2Fb", "application/json", ""},
		{"GET", "/agent/v1/conversations", "application/json", ""},
		{"GET", "/agent/v1/conversations?agent=a%26b&cursor=c%2B%2F%3D&limit=5", "application/json", ""},
		{"DELETE", "/agent/v1/conversations/conv%2F1", "application/json", ""},
		{"GET", "/agent/v1/toolsets", "application/json", ""},
		{"GET", "/agent/v1/toolsets/web%20search/tools", "application/json", ""},
	} {
		r := <-got
		body, _ := io.ReadAll(r.Body)
		if r.Method != want.method || r.URL.RequestURI() != want.path || r.Header.Get("Accept") != want.accept || string(body) != want.body ||
			r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("User-Agent") != "test-ua" {
			t.Errorf("%s %s Accept %q body %q, headers %v; want %+v", r.Method, r.URL.RequestURI(), r.Header.Get("Accept"), body, r.Header, want)
		}
	}
}

// TestNotTheAPI: a 200 that is not what was asked for is ErrBadResponse,
// and New refuses a base URL it cannot use.
func TestNotTheAPI(t *testing.T) {
	t.Parallel()
	bs := &bodyServer{header: http.Header{"Content-Type": {"text/html"}}, body: func() io.Reader { return strings.NewReader("<html>") }}
	c, err := agentclient.New("http://agent.test", agentclient.WithHTTPClient(&http.Client{Transport: bs}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Stream(t.Context(), agentclient.RunRequest{Message: "hi"}); !errors.Is(err, agentclient.ErrBadResponse) {
		t.Errorf("Stream of an HTML page = %v, want ErrBadResponse", err)
	}
	if _, err := c.Run(t.Context(), agentclient.RunRequest{Message: "hi"}); !errors.Is(err, agentclient.ErrBadResponse) {
		t.Errorf("Run of an HTML page = %v, want ErrBadResponse", err)
	}
	for _, u := range []string{"", "localhost:8080", "ftp://host", "http://", "http://host/?x=1", "http://host/#x"} {
		if _, err := agentclient.New(u); !errors.Is(err, agentclient.ErrInvalidConfig) {
			t.Errorf("New(%q) = %v, want ErrInvalidConfig", u, err)
		}
	}
}

// TestClientDoesNotImportTheServer: the client's own code pulls in neither
// the server nor the harness (the wire types are in their own package for
// that; its tests may use both).
func TestClientDoesNotImportTheServer(t *testing.T) {
	t.Parallel()
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		switch strings.TrimPrefix(sc.Text(), "github.com/bsamanvith/agentkit/") {
		case "pkg/agentapi", "pkg/harness":
			t.Errorf("the client depends on %s", sc.Text())
		}
	}
}
