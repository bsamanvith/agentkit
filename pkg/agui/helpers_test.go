package agui_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/a2ui"
	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agui"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/policy"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// The keys of the rig's agent API.
const (
	keyA    = "agui-test-key-tenant-a-0123456789" // tenant a, every scope
	keyB    = "agui-test-key-tenant-b-0123456789" // tenant b, every scope
	keyRead = "agui-test-key-read-only-012345678" // tenant a, agent:read only
)

// surface is the ui_render input of the fake model: a surface "news" with a
// choice and a button that sends the action "search".
const surface = `{"messages": [
  {"version": "v0.9", "createSurface": {"surfaceId": "news", "catalogId": "` + a2ui.BasicCatalogID + `"}},
  {"version": "v0.9", "updateComponents": {"surfaceId": "news", "components": [
    {"id": "root", "component": "Card", "child": "col"},
    {"id": "col", "component": "Column", "children": ["title", "topic", "go"]},
    {"id": "title", "component": "Text", "text": "Narrow the news search", "variant": "h3"},
    {"id": "topic", "component": "ChoicePicker", "options": [{"label": "Go", "value": "go"}, {"label": "Rust", "value": "rust"}], "value": {"path": "/topic"}},
    {"id": "go_label", "component": "Text", "text": "Search"},
    {"id": "go", "component": "Button", "child": "go_label", "action": {"event": {"name": "search", "context": {"topic": {"path": "/topic"}}}}}
  ]}},
  {"version": "v0.9", "updateDataModel": {"surfaceId": "news", "path": "/topic", "value": ["go"]}}
]}`

// model is a fake model that answers from the conversation:
//
//   - "show ...": says "Let me check." and calls echo; on its result, calls
//     ui_render with surface; on that result, says "Pick a topic above.";
//   - an A2UI action: "You chose <its context>.";
//   - "ask": calls ask (a question), "note": calls note (held for approval),
//     "both": says "One moment." and calls both at once; on their results,
//     says what they returned;
//   - "wait ...": waits until the rig's release is closed (or the run is
//     stopped), then answers "released";
//   - "fail": fails as a provider that rejects the request;
//   - anything else: "echo: <prompt>".
type model struct {
	tb      testing.TB
	release chan struct{}
	waiting chan struct{} // gets a value each time a "wait" reply starts waiting
	ids     atomic.Int64
}

func (m *model) call(name, input string) llm.ToolUse {
	return llm.ToolUse{ID: fmt.Sprintf("call_%d", m.ids.Add(1)), Name: name, Input: json.RawMessage(input)}
}

func (m *model) Generate(ctx context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	last := req.Messages[len(req.Messages)-1]
	names := map[string]string{} // tool_use ID to tool
	for _, msg := range req.Messages {
		for _, b := range msg.Content {
			if u, ok := b.(llm.ToolUse); ok {
				names[u.ID] = u.Name
			}
		}
	}
	var results []string
	var tools []string
	for _, b := range last.Content {
		if r, ok := b.(llm.ToolResult); ok {
			results = append(results, r.Content)
			tools = append(tools, names[r.ToolUseID])
		}
	}
	prompt := last.Text()
	wait := false
	var reply llmtest.Reply
	switch {
	case slices.Contains(tools, "echo"):
		reply = llmtest.ToolCalls(m.call(a2ui.ToolName, surface))
	case slices.Contains(tools, a2ui.ToolName):
		reply = llmtest.Text("Pick a topic above.")
	case len(results) > 0:
		reply = llmtest.Text("They said " + strings.Join(results, " and ") + ".")
	case strings.HasPrefix(prompt, "show"):
		reply = llmtest.Reply{Response: llm.Response{
			Message: llm.Assistant(llm.Text{Text: "Let me check."}, m.call("echo", `{"text":"hi"}`)),
			Stop:    llm.StopReason{Kind: llm.StopToolUse},
		}}
	case strings.HasPrefix(prompt, "<ui_action>"):
		a, ok := a2ui.ParseActionText(prompt)
		if !ok {
			m.tb.Errorf("the action message %q does not parse", prompt)
		}
		reply = llmtest.Text("You chose " + string(a.Context) + ".")
	case prompt == "ask":
		reply = llmtest.ToolCalls(m.call("ask", `{}`))
	case prompt == "note":
		reply = llmtest.ToolCalls(m.call("note", `{"s":"hi"}`))
	case prompt == "both":
		reply = llmtest.Reply{Response: llm.Response{
			Message: llm.Assistant(llm.Text{Text: "One moment."}, m.call("ask", `{}`), m.call("note", `{"s":"hi"}`)),
			Stop:    llm.StopReason{Kind: llm.StopToolUse},
		}}
	case strings.HasPrefix(prompt, "wait"):
		wait = true
		reply = llmtest.Text("released")
	case prompt == "fail":
		reply = llmtest.Fail(fmt.Errorf("%w: no", llm.ErrInvalidRequest))
	default:
		reply = llmtest.Text("echo: " + prompt)
	}
	inner := llmtest.NewFake(m.tb, reply).Generate(ctx, req)
	return func(yield func(llm.Event, error) bool) {
		if wait {
			m.waiting <- struct{}{}
			select {
			case <-m.release:
			case <-ctx.Done():
				yield(llm.Event{}, ctx.Err())
				return
			}
		}
		for ev, err := range inner {
			if !yield(ev, err) {
				return
			}
		}
	}
}

// rig is an agent API with three agents on the fake model, API keys of two
// tenants, and the AG-UI server over it, on one httptest server:
//
//   - "painter" shows UI (ui_render) and has the tool echo;
//   - "asker" is interactive, with ask (a question: red or blue) and note,
//     which its policy holds for approval;
//   - "plain" has no tools.
type rig struct {
	t       testing.TB
	ts      *httptest.Server
	srv     *agui.Server
	model   *model
	release func()
	notes   atomic.Int64 // calls of note that ran

	mu      sync.Mutex
	streams []recorded // every stream a test read, for the invariants
}

// recorded is a stream and the request it answered.
type recorded struct {
	in     map[string]any
	events []agui.Event
	raw    []json.RawMessage
}

func newRig(t testing.TB, opts ...agui.Option) *rig {
	t.Helper()
	m := &model{tb: t, release: make(chan struct{}), waiting: make(chan struct{}, 16)}
	r := &rig{t: t, model: m}
	echo, err := tool.Func("echo", "Echo the text.", func(_ context.Context, in struct {
		Text string `json:"text"`
	},
	) (string, error) {
		return in.Text, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	ask, err := tool.Func("ask", "Ask the user.", func(context.Context, struct{}) (string, error) {
		return "", tool.Interrupt("which color?", "red", "blue")
	}, tool.WithMetadata(tool.Metadata{Kind: tool.KindRead, Risk: tool.RiskLow, Interrupts: true}))
	if err != nil {
		t.Fatal(err)
	}
	note, err := tool.Func("note", "Write a note.", func(_ context.Context, in struct {
		S string `json:"s"`
	},
	) (string, error) {
		r.notes.Add(1)
		return "noted " + in.S, nil
	}, tool.WithMetadata(tool.Metadata{Kind: tool.KindWrite, Risk: tool.RiskLow}))
	if err != nil {
		t.Fatal(err)
	}
	reg := func(tools ...tool.Tool) tool.Registry {
		t.Helper()
		rg, err := tool.NewRegistry(tools)
		if err != nil {
			t.Fatal(err)
		}
		return rg
	}
	painter, err := harness.New(m, reg(echo, a2ui.Tool()))
	if err != nil {
		t.Fatal(err)
	}
	asker, err := harness.New(m, reg(ask, note),
		harness.WithInteraction(harness.Interactive),
		harness.WithToolPolicy(policy.Policy{Name: "agent", Rules: []policy.Rule{
			{Name: "hold-notes", Match: policy.Match{Tools: []string{"note"}}, Action: policy.Approve, Reason: "notes need a yes"},
		}, Default: policy.Allow}, nil))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := harness.New(m, reg())
	if err != nil {
		t.Fatal(err)
	}
	var keys []auth.APIKey
	for _, k := range []struct {
		id, key, tenant string
		scopes          []string
	}{
		{"a", keyA, "tenant-a", []string{"*"}},
		{"b", keyB, "tenant-b", []string{"*"}},
		{"read", keyRead, "tenant-a", []string{agentapi.ScopeRead}},
	} {
		e, err := auth.KeyEntry(auth.APIKey{ID: k.id, Tenant: k.tenant, Scopes: k.scopes, Toolsets: []string{"*"}}, k.key)
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, e)
	}
	authn, err := auth.NewAPIKeys(keys)
	if err != nil {
		t.Fatal(err)
	}
	var ids atomic.Int64
	api, err := agentapi.NewAgents([]agentapi.Agent{
		{Name: "painter", Description: "Shows UI.", UI: a2ui.UIMode, Harness: painter},
		{Name: "asker", Description: "Asks first.", Harness: asker},
		{Name: "plain", Description: "Answers.", Harness: plain},
	},
		agentapi.WithStore(scoped.Memory(nil)), agentapi.WithAuthenticator(authn), agentapi.WithKeepAlive(0),
		agentapi.WithNewID(func(prefix string) string { return fmt.Sprintf("%s%d", prefix, ids.Add(1)) }),
	)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := agui.New(api.Handler(), append([]agui.Option{agui.WithKeepAlive(0)}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	r.srv = srv
	mux := http.NewServeMux()
	mux.Handle("/v1/", api.Handler())
	mux.Handle(agui.PathPrefix, srv.Handler())
	r.ts = httptest.NewServer(mux)
	var once sync.Once
	r.release = func() { once.Do(func() { close(m.release) }) }
	t.Cleanup(func() {
		r.release()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Errorf("agui shutdown: %v", err)
		}
		r.checkInvariants()
		r.ts.Close()
		_ = api.Shutdown(ctx)
	})
	return r
}

// url is the rig's URL of path.
func (r *rig) url(path string) string { return r.ts.URL + path }

// input is a RunAgentInput of a thread with messages (each a message
// object), as a map so that tests can set any field.
func input(thread string, msgs ...map[string]any) map[string]any {
	if msgs == nil {
		msgs = []map[string]any{}
	}
	return map[string]any{"threadId": thread, "runId": "client-run-" + thread, "messages": msgs, "tools": []any{}, "context": []any{}, "protocolVersion": "1.0"}
}

// user is a user message.
func user(id, text string) map[string]any {
	return map[string]any{"id": id, "role": "user", "content": text}
}

// post sends a RunAgentInput to /agui/{agent} with key ("" for none). A
// stream is decoded and recorded for the invariants; any other answer is
// returned with its status and body.
func (r *rig) post(agent, key string, in map[string]any) (int, []agui.Event, []byte) {
	r.t.Helper()
	status, raw, body := r.postRaw(agent, key, in)
	if raw == nil {
		return status, nil, body
	}
	evs := decode(r.t, raw)
	r.mu.Lock()
	r.streams = append(r.streams, recorded{in: in, events: evs, raw: raw})
	r.mu.Unlock()
	return status, evs, nil
}

// run posts in and returns the stream, which must be one.
func (r *rig) run(agent, key string, in map[string]any) []agui.Event {
	r.t.Helper()
	status, evs, body := r.post(agent, key, in)
	if evs == nil {
		r.t.Fatalf("POST /agui/%s = %d %s, want a stream", agent, status, body)
	}
	return evs
}

// postRaw posts in (a map, a string or bytes) and reads the SSE stream's
// data lines, or (raw nil) the body of another answer.
func (r *rig) postRaw(agent, key string, in any) (int, []json.RawMessage, []byte) {
	r.t.Helper()
	var body []byte
	switch v := in.(type) {
	case string:
		body = []byte(v)
	case []byte:
		body = v
	default:
		b, err := json.Marshal(v)
		if err != nil {
			r.t.Fatal(err)
		}
		body = b
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url(agui.PathPrefix+agent), bytes.NewReader(body))
	if err != nil {
		r.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := r.ts.Client().Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, nil, b
	}
	return resp.StatusCode, readSSE(r.t, resp.Body), nil
}

// readSSE reads the data lines of a stream as an AG-UI client does: events
// end at a blank line, comments are skipped.
func readSSE(tb testing.TB, body io.Reader) []json.RawMessage {
	tb.Helper()
	raw := []json.RawMessage{}
	sc := bufio.NewScanner(body)
	sc.Buffer(nil, 4<<20)
	var data []string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if data != nil {
				raw = append(raw, json.RawMessage(strings.Join(data, "\n")))
				data = nil
			}
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		case strings.HasPrefix(line, ":"):
		default:
			tb.Errorf("a stream line that is neither data nor a comment: %q", line)
		}
	}
	if data != nil {
		tb.Errorf("the stream ends in the middle of an event")
	}
	return raw
}

func decode(tb testing.TB, raw []json.RawMessage) []agui.Event {
	tb.Helper()
	evs := make([]agui.Event, len(raw))
	for i, b := range raw {
		if err := json.Unmarshal(b, &evs[i]); err != nil {
			tb.Fatalf("event %d %s: %v", i, b, err)
		}
	}
	return evs
}

// types are the events' types.
func types(evs []agui.Event) []agui.EventType {
	out := make([]agui.EventType, len(evs))
	for i, e := range evs {
		out[i] = e.Type
	}
	return out
}

// last is the stream's last event.
func last(evs []agui.Event) agui.Event { return evs[len(evs)-1] }

// bound is the conversation and run RUN_STARTED names.
func bound(tb testing.TB, evs []agui.Event) (conv, run string) {
	tb.Helper()
	meta, _ := evs[0].Metadata[agui.MetadataKey].(map[string]any)
	conv, _ = meta["conversationId"].(string)
	run, _ = meta["runId"].(string)
	if conv == "" || run == "" {
		tb.Fatalf("RUN_STARTED %+v names no conversation and run", evs[0])
	}
	return conv, run
}

// getJSON reads path of the agent API with key into v.
func (r *rig) getJSON(path, key string, v any) int {
	r.t.Helper()
	req, err := http.NewRequest(http.MethodGet, r.url(path), nil)
	if err != nil {
		r.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := r.ts.Client().Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK && v != nil {
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			r.t.Fatal(err)
		}
	}
	return resp.StatusCode
}

// apiRun reads a run of the agent API.
func (r *rig) apiRun(key, id string) wire.Run {
	r.t.Helper()
	var run wire.Run
	if code := r.getJSON("/v1/runs/"+id, key, &run); code != http.StatusOK {
		r.t.Fatalf("GET /v1/runs/%s = %d", id, code)
	}
	return run
}

// conversation reads a conversation of the agent API.
func (r *rig) conversation(key, id string) wire.Conversation {
	r.t.Helper()
	var c wire.Conversation
	if code := r.getJSON("/v1/conversations/"+id, key, &c); code != http.StatusOK {
		r.t.Fatalf("GET /v1/conversations/%s = %d", id, code)
	}
	return c
}

func (r *rig) checkInvariants() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, s := range r.streams {
		checkStream(r.t, fmt.Sprintf("stream %d", i), s)
	}
}
