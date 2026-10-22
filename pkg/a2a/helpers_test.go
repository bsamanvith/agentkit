package a2a_test

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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/a2a"
	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/auth"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// The keys of the rig's agent API.
const (
	keyA       = "a2a-test-key-tenant-a-0123456789" // tenant a, every scope and toolset
	keyB       = "a2a-test-key-tenant-b-0123456789" // tenant b, every scope and toolset
	keyRead    = "a2a-test-key-read-only-012345678" // tenant a, agent:read only
	keyNoTools = "a2a-test-key-no-toolsets-0123456" // tenant a, no toolsets: only the agent "second"
)

// model is a fake model that answers from the prompt:
//
//   - "wait ...": waits until the rig's release is closed (or the run is
//     stopped), then answers "released";
//   - "tool ...": says "Let me check." and calls the demo echo tool, then
//     answers "the echo said <result>";
//   - anything else: "echo: <prompt>".
type model struct {
	tb      testing.TB
	release chan struct{}
	calls   atomic.Int64
}

func (m *model) Generate(ctx context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	m.calls.Add(1)
	last := req.Messages[len(req.Messages)-1]
	var reply llmtest.Reply
	var result *llm.ToolResult
	for _, b := range last.Content {
		if tr, ok := b.(llm.ToolResult); ok {
			result = &tr
		}
	}
	prompt := last.Text()
	wait := false
	switch {
	case result != nil:
		reply = llmtest.Text("the echo said " + result.Content)
	case strings.HasPrefix(prompt, "tool"):
		reply = llmtest.Reply{Response: llm.Response{
			Message: llm.Assistant(llm.Text{Text: "Let me check."}, llm.ToolUse{ID: "call_1", Name: "echo", Input: json.RawMessage(`{"text":"hi"}`)}),
			Stop:    llm.StopReason{Kind: llm.StopToolUse},
		}}
	case strings.HasPrefix(prompt, "wait"):
		wait = true
		reply = llmtest.Text("released")
	default:
		reply = llmtest.Text("echo: " + prompt)
	}
	inner := llmtest.NewFake(m.tb, reply).Generate(ctx, req)
	return func(yield func(llm.Event, error) bool) {
		if wait {
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

// rig is an agent API (two agents on the fake model, API keys of two
// tenants) and the A2A server over it, on one httptest server.
type rig struct {
	t       testing.TB
	ts      *httptest.Server
	api     *agentapi.Server
	srv     *a2a.Server
	model   *model
	release func() // lets "wait" runs answer

	mu      sync.Mutex
	streams [][]a2a.StreamResponse // every stream a test read, for the invariants
	open    atomic.Int64           // A2A event streams whose first item was sent
}

// subscribers is how many A2A streams have sent their first item, so far.
func (r *rig) subscribers() int64 { return r.open.Load() }

// countingWriter counts the event streams that send their first item.
type countingWriter struct {
	http.ResponseWriter
	open    *atomic.Int64
	counted bool
}

func (w *countingWriter) Write(b []byte) (int, error) {
	n, err := w.ResponseWriter.Write(b)
	if !w.counted && bytes.HasPrefix(b, []byte("data: ")) {
		w.counted = true
		defer w.open.Add(1) // after the flush below has sent it
		_ = http.NewResponseController(w.ResponseWriter).Flush()
	}
	return n, err
}

func (w *countingWriter) Flush() { _ = http.NewResponseController(w.ResponseWriter).Flush() }

func (w *countingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func newRig(t testing.TB, opts ...a2a.Option) *rig {
	t.Helper()
	return newRigOn(t, nil, opts...)
}

// newRigOn is newRig with its agents on live (the fake model when nil).
func newRigOn(t testing.TB, live llm.Model, opts ...a2a.Option) *rig {
	t.Helper()
	m := &model{tb: t, release: make(chan struct{})}
	var lm llm.Model = m
	if live != nil {
		lm = live
	}
	catalog, err := toolset.NewCatalog(toolset.Demo())
	if err != nil {
		t.Fatal(err)
	}
	sets, _, err := toolset.Build(t.Context(), catalog, []toolset.Spec{{Name: "demo", Type: toolset.DemoType}})
	if err != nil {
		t.Fatal(err)
	}
	h1, err := harness.New(lm, sets["demo"])
	if err != nil {
		t.Fatal(err)
	}
	none, err := tool.NewRegistry(nil)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := harness.New(lm, none)
	if err != nil {
		t.Fatal(err)
	}
	var keys []auth.APIKey
	for _, k := range []struct {
		id, key, tenant  string
		scopes, toolsets []string
	}{
		{"a", keyA, "tenant-a", []string{"*"}, []string{"*"}},
		{"b", keyB, "tenant-b", []string{"*"}, []string{"*"}},
		{"read", keyRead, "tenant-a", []string{agentapi.ScopeRead}, []string{"*"}},
		{"notools", keyNoTools, "tenant-a", []string{"*"}, nil},
	} {
		e, err := auth.KeyEntry(auth.APIKey{ID: k.id, Tenant: k.tenant, Scopes: k.scopes, Toolsets: k.toolsets}, k.key)
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
		{Name: agentapi.DefaultAgent, Description: "Answers questions with the demo tools.", Harness: h1, Toolsets: []string{"demo"}, System: "canary system prompt"},
		{Name: "second", Description: "Answers without tools.", Harness: h2},
	},
		agentapi.WithStore(scoped.Memory(nil)), agentapi.WithAuthenticator(authn), agentapi.WithKeepAlive(0),
		agentapi.WithToolsets(agentapi.Toolset{Name: "demo", Type: toolset.DemoType, Tools: sets["demo"]}),
		agentapi.WithNewID(func(prefix string) string { return fmt.Sprintf("%s%d", prefix, ids.Add(1)) }),
	)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := a2a.New(api.Handler(), append([]a2a.Option{
		a2a.WithKeepAlive(0), a2a.WithVersion("1.2.3"),
		a2a.WithSecuritySchemes(map[string]a2a.SecurityScheme{"bearer": {HTTPAuth: &a2a.HTTPAuthSecurityScheme{Scheme: "Bearer"}}}),
	}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, api: api, srv: srv, model: m}
	mux := http.NewServeMux()
	mux.Handle("/v1/", api.Handler())
	counted := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		srv.Handler().ServeHTTP(&countingWriter{ResponseWriter: w, open: &r.open}, req)
	})
	mux.Handle(a2a.PathPrefix, counted)
	mux.Handle(a2a.WellKnownPath, counted)
	r.ts = httptest.NewServer(mux)
	var once sync.Once
	r.release = func() { once.Do(func() { close(m.release) }) }
	t.Cleanup(func() {
		r.release()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			t.Errorf("a2a shutdown: %v", err)
		}
		r.checkInvariants()
		r.ts.Close()
		_ = api.Shutdown(ctx)
	})
	return r
}

// url is the rig's URL of path.
func (r *rig) url(path string) string { return r.ts.URL + path }

// do sends a request with key as the bearer token ("" for none).
func (r *rig) do(method, path, key string, body any) (int, http.Header, []byte) {
	r.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	case string:
		rd = strings.NewReader(b)
	default:
		data, err := json.Marshal(b)
		if err != nil {
			r.t.Fatal(err)
		}
		rd = bytes.NewReader(data)
	}
	// Not the test's context: the invariants read runs in its cleanup.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, r.url(path), rd)
	if err != nil {
		r.t.Fatal(err)
	}
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := r.ts.Client().Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		r.t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, data
}

// rpcResp is a JSON-RPC response.
type rpcResp struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int               `json:"code"`
		Message string            `json:"message"`
		Data    []json.RawMessage `json:"data"`
	} `json:"error"`
}

// rpc calls a JSON-RPC method of agent and decodes the response.
func (r *rig) rpc(agent, key, method string, params any) rpcResp {
	r.t.Helper()
	status, _, body := r.do(http.MethodPost, a2a.PathPrefix+agent, key, map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if status != http.StatusOK {
		r.t.Fatalf("%s: HTTP %d: %s", method, status, body)
	}
	var resp rpcResp
	if err := json.Unmarshal(body, &resp); err != nil {
		r.t.Fatalf("%s: %v: %s", method, err, body)
	}
	if resp.JSONRPC != "2.0" || string(resp.ID) != "1" {
		r.t.Fatalf("%s: not a response to the request: %s", method, body)
	}
	return resp
}

// result decodes a successful JSON-RPC result into v.
func (r *rig) result(resp rpcResp, v any) {
	r.t.Helper()
	if resp.Error != nil {
		r.t.Fatalf("JSON-RPC error %d: %s", resp.Error.Code, resp.Error.Message)
	}
	if err := json.Unmarshal(resp.Result, v); err != nil {
		r.t.Fatal(err)
	}
}

// errCode is the JSON-RPC error code of resp (0 for a result).
func errCode(resp rpcResp) int {
	if resp.Error == nil {
		return 0
	}
	return resp.Error.Code
}

// sse posts body to path and reads the SSE stream: each data line's JSON.
// It stops after n items when n > 0 (closing the stream).
func (r *rig) sse(method, path, key string, body any, n int) (int, []json.RawMessage) {
	r.t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		r.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(r.t.Context())
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.url(path), rd)
	if err != nil {
		r.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := r.ts.Client().Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, []json.RawMessage{b}
	}
	var items []json.RawMessage
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		if d, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			items = append(items, json.RawMessage(d))
			if n > 0 && len(items) == n {
				return resp.StatusCode, items
			}
		}
	}
	return resp.StatusCode, items
}

// stream reads a JSON-RPC stream of method and returns its StreamResponses,
// recorded for the invariants. A JSON-RPC error in the stream fails the
// test.
func (r *rig) stream(agent, key, method string, params any) []a2a.StreamResponse {
	r.t.Helper()
	status, items := r.sse(http.MethodPost, a2a.PathPrefix+agent, key, map[string]any{"jsonrpc": "2.0", "id": "s", "method": method, "params": params}, 0)
	if status != http.StatusOK {
		r.t.Fatalf("%s: HTTP %d", method, status)
	}
	var out []a2a.StreamResponse
	for _, it := range items {
		var resp rpcResp
		if err := json.Unmarshal(it, &resp); err != nil {
			r.t.Fatalf("%s: %v: %s", method, err, it)
		}
		r.result(resp, &struct{}{})
		var sr a2a.StreamResponse
		r.result(resp, &sr)
		out = append(out, sr)
	}
	r.record(out)
	return out
}

func (r *rig) record(s []a2a.StreamResponse) {
	r.mu.Lock()
	r.streams = append(r.streams, s)
	r.mu.Unlock()
}

// message is a user message of one text part.
func message(text string) map[string]any {
	return map[string]any{"role": "ROLE_USER", "messageId": "m-" + text, "parts": []any{map[string]any{"text": text}}}
}

// send sends text over JSON-RPC and returns the task.
func (r *rig) send(agent, key string, msg map[string]any, config map[string]any) a2a.Task {
	r.t.Helper()
	params := map[string]any{"message": msg}
	if config != nil {
		params["configuration"] = config
	}
	var res a2a.SendMessageResponse
	r.result(r.rpc(agent, key, "SendMessage", params), &res)
	if res.Task == nil {
		r.t.Fatal("SendMessage: no task")
	}
	return *res.Task
}

// sendStarted is send with returnImmediately, returning once the run's model
// call shows it started. Until then the agent API refuses to cancel it (409
// run_leased, "about to start; retry"), and the model has not counted it: a
// test that does either would fail on a busy machine.
func (r *rig) sendStarted(agent, key string, msg map[string]any) a2a.Task {
	r.t.Helper()
	before := r.model.calls.Load()
	task := r.send(agent, key, msg, map[string]any{"returnImmediately": true})
	testkit.Eventually(r.t, 10*time.Second, func() bool { return r.model.calls.Load() > before })
	return task
}

// run reads a run from the agent API.
func (r *rig) run(key, id string) wire.Run {
	r.t.Helper()
	status, _, body := r.do(http.MethodGet, "/v1/runs/"+id, key, nil)
	if status != http.StatusOK {
		r.t.Fatalf("GET /v1/runs/%s: %d %s", id, status, body)
	}
	var run wire.Run
	if err := json.Unmarshal(body, &run); err != nil {
		r.t.Fatal(err)
	}
	return run
}

// checkInvariants checks every stream the test read:
//   - it begins with the task and has no other task;
//   - every update is of that task and its context;
//   - it ends with one status update in a terminal state, and has no other
//     terminal status;
//   - the answer artifact, rebuilt from its chunks (append or replace),
//     equals its last chunk, and the run's answer in the agent API.
func (r *rig) checkInvariants() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i, s := range r.streams {
		if len(s) == 0 || s[0].Task == nil {
			r.t.Errorf("stream %d: does not begin with the task", i)
			continue
		}
		task := s[0].Task
		var answer string
		var last *a2a.TaskArtifactUpdateEvent
		for j, item := range s[1:] {
			switch {
			case item.Task != nil || item.Message != nil:
				r.t.Errorf("stream %d item %d: a task or message after the first item", i, j+1)
			case item.ArtifactUpdate != nil:
				u := item.ArtifactUpdate
				if u.TaskID != task.ID || u.ContextID != task.ContextID {
					r.t.Errorf("stream %d item %d: an update of another task", i, j+1)
				}
				text := ""
				for _, p := range u.Artifact.Parts {
					if p.Text != nil {
						text += *p.Text
					}
				}
				if u.Append {
					answer += text
				} else {
					answer = text
				}
				last = u
			case item.StatusUpdate != nil:
				u := item.StatusUpdate
				if u.TaskID != task.ID || u.ContextID != task.ContextID {
					r.t.Errorf("stream %d item %d: an update of another task", i, j+1)
				}
				isLast := j+1 == len(s)-1
				if terminalState(u.Status.State) != isLast {
					r.t.Errorf("stream %d item %d: state %s; last item: %v", i, j+1, u.Status.State, isLast)
				}
			default:
				r.t.Errorf("stream %d item %d: empty", i, j+1)
			}
		}
		if end := s[len(s)-1]; end.StatusUpdate == nil {
			r.t.Errorf("stream %d: does not end with a status update", i)
		}
		if last != nil && !last.LastChunk {
			r.t.Errorf("stream %d: the answer's last chunk is not marked", i)
		}
		if last != nil {
			if run := r.run(keyA, task.ID); run.Status == wire.StatusDone && run.Answer != answer {
				r.t.Errorf("stream %d: streamed answer %q, the run's %q", i, answer, run.Answer)
			}
		}
	}
}

func terminalState(s string) bool {
	switch s {
	case a2a.StateCompleted, a2a.StateFailed, a2a.StateCanceled, a2a.StateRejected:
		return true
	}
	return false
}

// whenStreamOpens calls f, in a goroutine, once one more A2A stream than now
// has sent its first item.
func (r *rig) whenStreamOpens(f func()) {
	before := r.subscribers()
	go func() {
		deadline := time.Now().Add(10 * time.Second)
		for r.subscribers() == before {
			if time.Now().After(deadline) {
				r.t.Error("no stream opened")
				return
			}
			time.Sleep(time.Millisecond) // polls a counter, as testkit.Eventually does
		}
		f()
	}()
}

// cancelRun cancels a run through the agent API; it may be called from any
// goroutine.
func (r *rig) cancelRun(key, id string) {
	req, err := http.NewRequest(http.MethodPost, r.url("/v1/runs/"+id+"/cancel"), nil)
	if err != nil {
		r.t.Error(err)
		return
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := r.ts.Client().Do(req)
	if err != nil {
		r.t.Error(err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		r.t.Errorf("cancel %s: HTTP %d", id, resp.StatusCode)
	}
}

// text is the text of an artifact or a message's parts.
func text(parts []a2a.Part) string {
	var b strings.Builder
	for _, p := range parts {
		if p.Text != nil {
			b.WriteString(*p.Text)
		}
	}
	return b.String()
}
