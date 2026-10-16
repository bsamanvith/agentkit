package agentapi_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// testToken is the bearer token of rigs that ask for auth.
const testToken = "test-token-0123456789"

// model answers each request with the reply fn picks for it, records every
// request, and can hold each reply until the test lets it go.
type model struct {
	tb   testing.TB
	fn   func(req llm.Request) llmtest.Reply
	gate chan struct{} // nil: answer at once; else each reply takes one value (or waits for ctx)

	mu   sync.Mutex
	reqs []llm.Request
}

func newModel(tb testing.TB, fn func(req llm.Request) llmtest.Reply) *model {
	return &model{tb: tb, fn: fn}
}

// script answers the requests with replies, in order.
func script(tb testing.TB, replies ...llmtest.Reply) *model {
	var n atomic.Int32
	return newModel(tb, func(llm.Request) llmtest.Reply {
		i := int(n.Add(1)) - 1
		if i >= len(replies) {
			tb.Errorf("model call %d, but only %d replies were scripted", i+1, len(replies))
			return llmtest.Fail(llm.ErrInvalidRequest)
		}
		return replies[i]
	})
}

// echo answers every request with "echo: " and the text of its last message.
func echo(tb testing.TB) *model {
	return newModel(tb, func(req llm.Request) llmtest.Reply {
		return llmtest.Text("echo: " + req.Messages[len(req.Messages)-1].Text())
	})
}

func (m *model) Generate(ctx context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	req = req.Clone()
	m.mu.Lock()
	m.reqs = append(m.reqs, req)
	m.mu.Unlock()
	reply := m.fn(req)
	inner := llmtest.NewFake(m.tb, reply).Generate(ctx, req)
	return func(yield func(llm.Event, error) bool) {
		if m.gate != nil {
			select {
			case <-m.gate:
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

func (m *model) requests() []llm.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.reqs)
}

// rig is an agent API on an httptest server, with a client that records every
// stream it reads. When the test ends, check verifies the invariants on
// everything the test did.
type rig struct {
	t      *testing.T
	srv    *agentapi.Server
	ts     *httptest.Server
	model  *model
	models []*model // every model the server's agents run on; default model
	clock  *clocktest.Fake
	token  string
	url    string // where runs are posted: the httptest server, unless a test serves elsewhere
	client *http.Client
	shared *shared      // the store, bus and clock of replicas; nil for a lone rig
	subs   *countingBus // b's run subscriptions, for a replica that serves runs

	mu      sync.Mutex
	streams []*stream       // every stream read to its end, in the order they ended
	runs    []agentapi.Run  // runs posted for JSON
	convs   []string        // conversations of every stream, in the order seen
	partial map[string]bool // conversations with a stream the client abandoned
}

type rigConfig struct {
	// agents, if set, are the server's agents (NewAgents) instead of one
	// agent with the rig's model and tools; their harnesses are the test's.
	agents  []agentapi.Agent
	tools   tool.Registry
	harness []harness.Option
	api     []agentapi.Option
	token   string
	clock   *clocktest.Fake
	now     func() time.Time // the harness's clock; default clock.Now
	store   agentapi.Store   // default: testStore
}

// testStore is the store of a rig: in memory, or the backend
// AGENTAPI_TEST_STORE names ("sqlite" for a file of the test's own, or a
// postgres:// URL, shared by every rig), so the suite runs on each; and the
// rig's tenant (rigTenant).
func testStore(t *testing.T, clk *clocktest.Fake) (agentapi.Store, []agentapi.Option) {
	t.Helper()
	spec := os.Getenv("AGENTAPI_TEST_STORE")
	switch spec {
	case "", "memory":
		return scoped.Memory(clk), []agentapi.Option{rigTenant()}
	case "sqlite":
		spec = "sqlite:" + filepath.Join(t.TempDir(), "agentkit.db")
	}
	s, closeFn, err := scoped.Open(t.Context(), spec, clk)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closeFn() })
	return s, []agentapi.Option{rigTenant()}
}

// rigTenant gives a rig a tenant of its own, and its requests' Test-Tenant
// header one of the rig's: tests on a shared store do not meet, nor do runs
// of the suite.
func rigTenant() agentapi.Option {
	tenant := fmt.Sprintf("rig-%d-%d", time.Now().UnixNano(), rigs.Add(1))
	return agentapi.WithTenant(func(r *http.Request) string {
		if h := r.Header.Get("Test-Tenant"); h != "" {
			return tenant + "-" + h
		}
		return tenant
	})
}

var rigs atomic.Int64

func newRig(t *testing.T, m *model, cfg rigConfig) *rig {
	t.Helper()
	if cfg.tools == nil {
		var err error
		if cfg.tools, err = tool.NewRegistry(nil); err != nil {
			t.Fatal(err)
		}
	}
	if cfg.clock == nil {
		cfg.clock = clocktest.NewFake(time.Time{})
	}
	if cfg.now == nil {
		cfg.now = cfg.clock.Now
	}
	h, err := harness.New(m, cfg.tools, append([]harness.Option{harness.WithNow(cfg.now)}, cfg.harness...)...)
	if err != nil {
		t.Fatal(err)
	}
	var tenantOpts []agentapi.Option
	if cfg.store == nil {
		cfg.store, tenantOpts = testStore(t, cfg.clock)
	}
	var ids atomic.Int64
	opts := []agentapi.Option{
		agentapi.WithStore(cfg.store), agentapi.WithClock(cfg.clock), agentapi.WithKeepAlive(0), agentapi.WithRunTimeout(0),
		// The rig's clock stands still, so lists order by ID: make it the
		// order of creation.
		agentapi.WithNewID(func(prefix string) string { return fmt.Sprintf("%s%03d", prefix, ids.Add(1)) }),
		// A retry waits on the clock; a test that wants one turns it on.
		agentapi.WithRunnerOptions(runner.WithInteractiveRetry(runner.RetryPolicy{MaxAttempts: 1, BaseDelay: time.Second, MaxDelay: time.Second})),
	}
	opts = append(opts, tenantOpts...) // before the test's: a test's WithTenant replaces it
	if cfg.token != "" {
		opts = append(opts, agentapi.WithToken(cfg.token))
	}
	var srv *agentapi.Server
	if cfg.agents != nil {
		srv, err = agentapi.NewAgents(cfg.agents, append(opts, cfg.api...)...)
	} else {
		srv, err = agentapi.New(h, append(opts, cfg.api...)...)
	}
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, srv: srv, model: m, models: []*model{m}, clock: cfg.clock, token: cfg.token}
	mux := http.NewServeMux()
	mux.Handle("/v1/", srv.Handler())
	r.ts = httptest.NewServer(mux)
	r.url = r.ts.URL
	// A request that never ends (a broken server) fails the test in a
	// minute instead of hanging it; no correct run here takes that long.
	r.client = &http.Client{Transport: r.ts.Client().Transport, Timeout: time.Minute}
	t.Cleanup(r.ts.Close)
	t.Cleanup(func() { // a run a broken server never ended would hold Close for ever
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	t.Cleanup(r.check) // runs before the server closes
	return r
}

// do sends a request with the rig's token and the given headers ("Name",
// "value", ...), and returns the response, its body read.
func (r *rig) do(method, path, body string, header ...string) (*http.Response, []byte) {
	r.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.WithoutCancel(r.t.Context()), method, r.ts.URL+path, rd)
	if err != nil {
		r.t.Fatal(err)
	}
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := r.client.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		r.t.Fatal(err)
	}
	return resp, b
}

// startAgent posts a run naming an agent and returns its open stream.
func (r *rig) startAgent(ctx context.Context, message, conversationID, agent string) *stream {
	r.t.Helper()
	b, _ := json.Marshal(agentapi.RunRequest{Message: message, ConversationID: conversationID, Agent: agent})
	return r.open(r.post(ctx, string(b), ""))
}

// post sends POST /v1/runs with the given body and Accept header.
func (r *rig) post(ctx context.Context, body, accept string) *http.Response {
	r.t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url+"/v1/runs", strings.NewReader(body))
	if err != nil {
		r.t.Fatal(err)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	return resp
}

func runBody(message, conversationID string) string {
	b, _ := json.Marshal(agentapi.RunRequest{Message: message, ConversationID: conversationID})
	return string(b)
}

// start posts a run and returns its open event stream. The response must be
// a stream.
func (r *rig) start(ctx context.Context, message, conversationID string) *stream {
	r.t.Helper()
	return r.open(r.post(ctx, runBody(message, conversationID), ""))
}

// open checks that resp is an event stream and returns it.
func (r *rig) open(resp *http.Response) *stream {
	r.t.Helper()
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		r.t.Fatalf("POST /v1/runs = %d %s: %s", resp.StatusCode, resp.Header.Get("Content-Type"), b)
	}
	s := &stream{r: r, resp: resp, sc: bufio.NewScanner(resp.Body)}
	s.sc.Buffer(nil, 1<<20)
	return s
}

// run posts a run and reads its stream to the end.
func (r *rig) run(message, conversationID string) *stream {
	r.t.Helper()
	s := r.start(r.t.Context(), message, conversationID)
	s.all()
	return s
}

// runJSON posts a run asking for JSON and returns the run.
func (r *rig) runJSON(message, conversationID string) agentapi.Run {
	r.t.Helper()
	resp := r.post(r.t.Context(), runBody(message, conversationID), "application/json")
	defer resp.Body.Close()
	var run agentapi.Run
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&run) != nil {
		r.t.Fatalf("POST /v1/runs (JSON) = %d", resp.StatusCode)
	}
	r.mu.Lock()
	r.runs = append(r.runs, run)
	r.mu.Unlock()
	return run
}

// get sends a GET to path and decodes the body into v; it returns the status.
func (r *rig) get(path string, v any) int {
	r.t.Helper()
	req, err := http.NewRequestWithContext(context.WithoutCancel(r.t.Context()), http.MethodGet, r.ts.URL+path, nil)
	if err != nil {
		r.t.Fatal(err)
	}
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		r.t.Fatalf("GET %s = %d: %v", path, resp.StatusCode, err)
	}
	return resp.StatusCode
}

func (r *rig) getRun(id string) agentapi.Run {
	r.t.Helper()
	var run agentapi.Run
	if code := r.get("/v1/runs/"+id, &run); code != http.StatusOK {
		r.t.Fatalf("GET /v1/runs/%s = %d", id, code)
	}
	return run
}

func (r *rig) getConversation(id string) agentapi.Conversation {
	r.t.Helper()
	var c agentapi.Conversation
	if code := r.get("/v1/conversations/"+id, &c); code != http.StatusOK {
		r.t.Fatalf("GET /v1/conversations/%s = %d", id, code)
	}
	return c
}

// events reads GET /v1/runs/{id}/events to its end, from after lastEventID
// if it is not empty.
func (r *rig) events(id, lastEventID string) []agentapi.Event {
	r.t.Helper()
	s := r.follow(id, lastEventID)
	s.done = true // the whole run, which check reads itself
	return s.all()
}

// follow opens GET /v1/runs/{id}/events, from after lastEventID if it is not
// empty. Once the response has come, the server counts the client.
func (r *rig) follow(id, lastEventID string) *stream {
	r.t.Helper()
	return r.followCtx(context.WithoutCancel(r.t.Context()), id, lastEventID)
}

// followCtx is follow, for a request that ends with ctx.
func (r *rig) followCtx(ctx context.Context, id, lastEventID string) *stream {
	r.t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.ts.URL+"/v1/runs/"+id+"/events", nil)
	if err != nil {
		r.t.Fatal(err)
	}
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		r.t.Fatalf("GET /v1/runs/%s/events = %d", id, resp.StatusCode)
	}
	s := &stream{r: r, resp: resp, sc: bufio.NewScanner(resp.Body)}
	s.sc.Buffer(nil, 1<<20)
	return s
}

// postPath sends a POST to path with the rig's token and returns the
// response, its body unread.
func (r *rig) postPath(ctx context.Context, path, body string) *http.Response {
	r.t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.url+path, strings.NewReader(body))
	if err != nil {
		r.t.Fatal(err)
	}
	if r.token != "" {
		req.Header.Set("Authorization", "Bearer "+r.token)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		r.t.Fatal(err)
	}
	return resp
}

// endStatus is the status of a run whose stream ended with a run_error of
// code: a chat run that stopped is interrupted.
func endStatus(code string) string {
	switch code {
	case agentapi.CodeRunInterrupted, agentapi.CodeTimeout, agentapi.CodeShutdown:
		return agentapi.StatusInterrupted
	case agentapi.CodeCancelled:
		return agentapi.StatusCanceled
	}
	return agentapi.StatusFailed
}

// waitEnded waits until the run is no longer running and returns it.
func (r *rig) waitEnded(id string) agentapi.Run {
	r.t.Helper()
	var run agentapi.Run
	testkit.Eventually(r.t, 30*time.Second, func() bool {
		run = r.getRun(id)
		return run.Status != agentapi.StatusRunning
	})
	return run
}

// stream is one SSE response, read event by event.
type stream struct {
	r      *rig
	resp   *http.Response
	sc     *bufio.Scanner
	events []agentapi.Event
	done   bool
}

// next returns the next event, or false at the end of the stream. It checks
// the SSE framing: the id is the event's, the event name is the type.
func (s *stream) next() (agentapi.Event, bool) {
	s.r.t.Helper()
	var id, name string
	var data []string
	for s.sc.Scan() {
		line := s.sc.Text()
		switch {
		case line == "":
			if data == nil {
				continue
			}
			var e agentapi.Event
			if err := json.Unmarshal([]byte(strings.Join(data, "\n")), &e); err != nil {
				s.r.t.Fatalf("event %q: %v", data, err)
			}
			if seq, _, ok := wire.ParseID(e.ID); !ok || id != e.ID || seq != uint64(e.Seq) || name != e.Type {
				s.r.t.Errorf("SSE id %q and event %q, want the event's id %q (of seq %d) and %q", id, name, e.ID, e.Seq, e.Type)
			}
			s.events = append(s.events, e)
			if e.Type == agentapi.TypeRunStart && !s.done { // not a read of check's own
				s.r.mu.Lock()
				if !slices.Contains(s.r.convs, e.ConversationID) {
					s.r.convs = append(s.r.convs, e.ConversationID)
				}
				s.r.mu.Unlock()
			}
			return e, true
		case strings.HasPrefix(line, ":"): // comment
		case strings.HasPrefix(line, "id: "):
			id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			data = append(data, strings.TrimPrefix(line, "data: "))
		default:
			s.r.t.Errorf("unexpected SSE line %q", line)
		}
	}
	s.resp.Body.Close()
	if !s.done {
		s.done = true
		s.r.mu.Lock()
		s.r.streams = append(s.r.streams, s)
		s.r.mu.Unlock()
	}
	return agentapi.Event{}, false
}

// until reads events until one of type typ (and, if turn > 0, of that turn).
func (s *stream) until(typ string, turn int) agentapi.Event {
	s.r.t.Helper()
	for {
		e, ok := s.next()
		if !ok {
			s.r.t.Fatalf("stream ended before a %s event", typ)
		}
		if e.Type == typ && (turn == 0 || e.Turn == turn) {
			return e
		}
	}
}

// all reads the stream to its end.
func (s *stream) all() []agentapi.Event {
	for {
		if _, ok := s.next(); !ok {
			return s.events
		}
	}
}

// abandon closes the stream without reading it to the end (the client goes
// away); it is not checked as a whole.
func (s *stream) abandon(cancel context.CancelFunc) {
	s.r.mu.Lock()
	if s.r.partial == nil {
		s.r.partial = map[string]bool{}
	}
	s.r.partial[s.conversationID()] = true
	s.r.mu.Unlock()
	cancel()
	s.resp.Body.Close()
}

// superseded reports whether a stream's last event, a run_error, was
// replaced by now, the event of that seq once the run went on: resumed (an
// interrupted or failed run continues), or canceled by a new message to its
// conversation.
func superseded(end, now agentapi.Event) bool {
	switch {
	case now.Type != agentapi.TypeRunError:
		return endStatus(end.Error.Code) != agentapi.StatusCanceled // resumed
	case now.Error.Code == agentapi.CodeCancelled:
		return end.Error.Code != agentapi.CodeCancelled
	}
	return false
}

func (s *stream) last() agentapi.Event { return s.events[len(s.events)-1] }

func (s *stream) runID() string { return s.events[0].RunID }

func (s *stream) conversationID() string { return s.events[0].ConversationID }

// check verifies what must hold for everything the test did:
//
//  1. every stream read to its end is well formed (checkStream), and agrees
//     with the run GET /v1/runs/{id} returns;
//  2. every conversation the test touched holds exactly the messages its
//     streams announced (run_start's prompt, turn_done's messages), in order;
//  3. every request the model got is a prefix of its conversation (so a
//     follow-up carried the earlier turns unchanged) and ends with a user
//     message.
func (r *rig) check() {
	t := r.t
	if t.Failed() {
		return
	}
	r.mu.Lock()
	streams, runs, convs := slices.Clone(r.streams), slices.Clone(r.runs), slices.Clone(r.convs)
	partial := maps.Clone(r.partial)
	r.mu.Unlock()

	// Each run's whole stream, from GET /v1/runs/{id}/events: a stream that
	// resumed a run is its tail.
	whole := map[string][]agentapi.Event{}
	var order []string // runs, in the order their streams ended
	for _, s := range streams {
		id := s.events[0].RunID
		full, ok := whole[id]
		if !ok {
			full = r.events(id, "")
			whole[id], order = full, append(order, id)
		}
		got := s.events
		if end := s.last(); end.Type == agentapi.TypeRunError && indexOf(full, end.ID) < 0 {
			// The run went on: the ending was streamed, not stored, and
			// the run's events go on after it.
			if i := after(full, end.ID); i < len(full) && superseded(end, full[i]) {
				got = got[:len(got)-1]
			}
		}
		if !sameRun(got, full[from(full, s.events[0].ID):]) {
			t.Errorf("run %s streamed\n%s\nbut its events are\n%s", id, jsonOf(s.events), jsonOf(full))
		}
	}
	announced := map[string][]llm.Message{} // conversation → messages, from events
	for _, id := range order {
		events := whole[id]
		checkStream(t, events)
		if t.Failed() {
			return
		}
		run := r.getRun(id)
		end := events[len(events)-1]
		switch {
		case run.ConversationID != events[0].ConversationID, run.Turns != end.Turns:
			t.Errorf("run %s = %+v, disagrees with its stream", run.ID, run)
		case end.Type == agentapi.TypeRunDone && (run.Status != agentapi.StatusDone || run.Answer != end.Answer || run.Usage != *end.Usage):
			t.Errorf("run %s = %+v, want done with the stream's answer %q and usage", run.ID, run, end.Answer)
		case end.Type == agentapi.TypeRunError && (run.Status != endStatus(end.Error.Code) || run.Error == nil || run.Error.Code != end.Error.Code):
			t.Errorf("run %s = %+v, want %s with code %q", run.ID, run, endStatus(end.Error.Code), end.Error.Code)
		}
		conv := events[0].ConversationID
		for _, e := range events {
			switch {
			case e.Type == agentapi.TypeRunStart && e.Prompt != nil:
				announced[conv] = append(announced[conv], e.Prompt.Message)
			case e.Type == agentapi.TypeTurnDone:
				for _, m := range e.Record.Messages {
					announced[conv] = append(announced[conv], m.Message)
				}
			}
		}
	}
	for _, run := range runs {
		if run.Status == agentapi.StatusDone && (run.Record == nil || run.Answer == "" && run.Turns > 0) {
			t.Errorf("JSON run %s = %+v, want an answer and a record", run.ID, run)
		}
	}
	listed := r.listAll()
	transcripts := map[string][]llm.Message{}
	for _, id := range convs {
		c := r.getConversation(id)
		if sum, ok := listed[id]; !ok || sum.Agent != c.Agent || sum.Status != c.Status || sum.MessageCount != len(c.Messages) {
			t.Errorf("conversation %s (agent %s, %s, %d messages) is listed as %+v", id, c.Agent, c.Status, len(c.Messages), sum)
		}
		var got, said []llm.Message
		for _, m := range c.Messages {
			got = append(got, m.Message)
			if m.Message.Role != llm.RoleAssistant || m.Message.Text() != runner.CanceledText {
				said = append(said, m.Message) // a run replaced before it answered gets a marker no stream announces
			}
		}
		if !partial[id] && !jsonEqual(said, announced[id]) {
			t.Errorf("conversation %s holds\n%s\nbut its streams announced\n%s", id, jsonOf(got), jsonOf(announced[id]))
		}
		if err := harness.Transcript(got).Validate(); err != nil {
			t.Errorf("conversation %s: %v", id, err)
		}
		transcripts[id] = got
	}
	if len(runs) > 0 {
		return // JSON runs announce nothing; the requests cannot be matched to them
	}
	var requests []llm.Request
	for _, m := range r.models {
		requests = append(requests, m.requests()...)
	}
	for i, req := range requests {
		if n := len(req.Messages); n == 0 || req.Messages[n-1].Role != llm.RoleUser {
			t.Errorf("model request %d does not end with a user message", i+1)
		}
		found := false
		for _, msgs := range transcripts {
			if len(req.Messages) <= len(msgs) && jsonEqual(req.Messages, msgs[:len(req.Messages)]) {
				found = true
			}
		}
		if !found && len(transcripts) > 0 {
			t.Errorf("model request %d is not a prefix of any conversation: %s", i+1, jsonOf(req.Messages))
		}
	}
}

// listAll pages through GET /v1/conversations (the default tenant's),
// checking each page is newest first and no conversation is listed twice.
func (r *rig) listAll() map[string]wire.ConversationSummary {
	r.t.Helper()
	all := map[string]wire.ConversationSummary{}
	var last *wire.ConversationSummary
	for cursor := ""; ; {
		var page wire.ConversationList
		if code := r.get("/v1/conversations?limit=7&cursor="+cursor, &page); code != http.StatusOK {
			r.t.Fatalf("GET /v1/conversations = %d", code)
		}
		for _, c := range page.Conversations {
			if _, dup := all[c.ID]; dup {
				r.t.Errorf("conversation %s listed twice", c.ID)
			}
			if last != nil && c.CreatedAt.After(last.CreatedAt) {
				r.t.Errorf("conversation %s (%v) listed after %s (%v), not newest first", c.ID, c.CreatedAt, last.ID, last.CreatedAt)
			}
			all[c.ID] = c
			last = &c
		}
		if cursor != "" && len(page.Conversations) == 0 {
			r.t.Errorf("a next_cursor led to an empty page")
		}
		if page.NextCursor != "" && len(page.Conversations) < 7 {
			r.t.Errorf("a page of %d with limit=7 has a next_cursor", len(page.Conversations))
		}
		if cursor = page.NextCursor; cursor == "" {
			break
		}
	}
	// A page that holds every conversation exactly has no next one.
	if n := len(all); n > 0 && n <= agentapi.MaxListLimit {
		var page wire.ConversationList
		if code := r.get("/v1/conversations?limit="+strconv.Itoa(n), &page); code != http.StatusOK || len(page.Conversations) != n || page.NextCursor != "" {
			r.t.Errorf("GET /v1/conversations?limit=%d = %d, %d conversations, next_cursor %q; want all %d and none", n, code, len(page.Conversations), page.NextCursor, n)
		}
	}
	return all
}

// checkStream checks one run's events: framing and header, the order, exactly
// one terminal event at the end, and that the text deltas add up to each
// turn's text and the last turn's to the answer.
func checkStream(t *testing.T, events []agentapi.Event) {
	t.Helper()
	if len(events) < 2 || events[0].Type != agentapi.TypeRunStart || events[0].ConversationID == "" {
		t.Errorf("stream %v does not start with run_start and end after it", types(events))
		return
	}
	last := events[len(events)-1]
	terminal := 0
	turn := 0
	deltas := map[int]string{} // turn → text deltas
	calls := map[[2]int]string{}
	var usage llm.Usage
	stored := 0
	for i, e := range events {
		seq, n, ok := wire.ParseID(e.ID)
		if n == 0 {
			stored++
		}
		if !ok || e.V != agentapi.WireVersion || seq != uint64(stored) || e.Seq != stored || e.RunID != events[0].RunID ||
			(i > 0 && (e.At.Before(events[i-1].At) || wire.CompareIDs(events[i-1].ID, e.ID) >= 0)) {
			t.Errorf("event %d %+v: want v 1, an ID after the last's of stored event %d, run %s, time not before the last", i, e, stored, events[0].RunID)
		}
		switch e.Type {
		case agentapi.TypeRunStart:
			if i != 0 {
				t.Errorf("run_start at %d", i)
			}
		case agentapi.TypeTextDelta, agentapi.TypeToolCall, agentapi.TypeToolResult, agentapi.TypeTurnDone:
			if e.Turn != turn+1 {
				t.Errorf("event %d (%s) of turn %d after turn %d was done", i, e.Type, e.Turn, turn)
			}
		case agentapi.TypeRunDone, agentapi.TypeRunError:
			terminal++
		case agentapi.TypeRewind:
			if e.Turns > turn {
				t.Errorf("rewind to %d turns after %d", e.Turns, turn)
			}
			for k := turn + 1; k > e.Turns; k-- {
				delete(deltas, k)
			}
			for k := range calls {
				if k[0] > e.Turns {
					delete(calls, k)
				}
			}
			turn = e.Turns
		case agentapi.TypeRetrying:
			if e.Attempt < 2 || e.Error == nil {
				t.Errorf("retrying %+v: want an attempt from 2 and the error", e)
			}
			delete(deltas, turn+1) // the turn in progress starts again
			for k := range calls {
				if k[0] == turn+1 {
					delete(calls, k)
				}
			}
		default:
			t.Errorf("event %d of unknown type %q", i, e.Type)
		}
		switch e.Type {
		case agentapi.TypeTextDelta:
			deltas[e.Turn] += e.Text
		case agentapi.TypeToolCall:
			if _, dup := calls[[2]int{e.Turn, e.Call}]; dup {
				t.Errorf("tool_call %d/%d twice", e.Turn, e.Call)
			}
			calls[[2]int{e.Turn, e.Call}] = e.ToolUseID
		case agentapi.TypeToolResult:
			id, ok := calls[[2]int{e.Turn, e.Call}]
			if !ok || id != e.ToolUseID {
				t.Errorf("tool_result %d/%d without its tool_call", e.Turn, e.Call)
			}
			calls[[2]int{e.Turn, e.Call}] = "done"
		case agentapi.TypeTurnDone:
			turn++
			for k, v := range calls {
				if k[0] == turn && v != "done" {
					t.Errorf("turn %d done with call %d unfinished", turn, k[1])
				}
			}
			if e.Record == nil || e.Record.Turn != turn || len(e.Record.Messages) == 0 {
				t.Errorf("turn_done %d has record %+v", turn, e.Record)
				continue
			}
			if got := e.Record.Messages[0].Message.Text(); got != deltas[turn] {
				t.Errorf("turn %d text deltas %q, but its reply says %q", turn, deltas[turn], got)
			}
			usage = usage.Add(e.Record.Usage)
		}
	}
	if terminal != 1 || (last.Type != agentapi.TypeRunDone && last.Type != agentapi.TypeRunError) {
		t.Errorf("stream %v: want exactly one run_done or run_error, last", types(events))
		return
	}
	if last.Usage == nil {
		t.Errorf("%s without usage", last.Type)
		return
	}
	switch last.Type {
	case agentapi.TypeRunDone:
		if last.Final == nil || last.Answer != last.Final.Text() || last.Answer != deltas[turn] || last.Turns != turn || *last.Usage != usage {
			t.Errorf("run_done %+v: want the answer %q (the last turn's deltas), %d turns and usage %+v", last, deltas[turn], turn, usage)
		}
	case agentapi.TypeRunError:
		if last.Error == nil || last.Error.Code == "" || last.Turns < turn {
			t.Errorf("run_error %+v: want a typed error and at least %d turns", last, turn)
		}
	}
}

func types(events []agentapi.Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.Type
	}
	return out
}

func jsonOf(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(err.Error())
	}
	return b
}

func jsonEqual(a, b any) bool { return bytes.Equal(jsonOf(a), jsonOf(b)) }

// indexOf returns the index of the event of ID id in evs, or -1.
func indexOf(evs []agentapi.Event, id string) int {
	return slices.IndexFunc(evs, func(e agentapi.Event) bool { return e.ID == id })
}

// after returns the index of the first event of evs after the ID id.
func after(evs []agentapi.Event, id string) int {
	i := slices.IndexFunc(evs, func(e agentapi.Event) bool { return wire.CompareIDs(e.ID, id) > 0 })
	if i < 0 {
		return len(evs)
	}
	return i
}

// from returns the index of the first event of evs at or after the ID id.
func from(evs []agentapi.Event, id string) int {
	i := slices.IndexFunc(evs, func(e agentapi.Event) bool { return wire.CompareIDs(e.ID, id) >= 0 })
	if i < 0 {
		return len(evs)
	}
	return i
}

// sameRun reports whether got is a prefix of want, the events of one run, as
// a client sees them: the same stored events, and each turn the same text,
// however its text_delta events cut it (a run read from the store streams
// each text block as one).
func sameRun(got, want []agentapi.Event) bool {
	norm := func(evs []agentapi.Event) []agentapi.Event {
		var out []agentapi.Event
		for _, e := range evs {
			if e.Type == agentapi.TypeTextDelta {
				if n := len(out); n > 0 && out[n-1].Type == agentapi.TypeTextDelta && out[n-1].Turn == e.Turn {
					out[n-1].Text += e.Text
					continue
				}
				e.ID, e.Block, e.At = "", 0, time.Time{}
			}
			out = append(out, e)
		}
		return out
	}
	g, w := norm(got), norm(want)
	if len(g) > len(w) {
		return false
	}
	if n := len(g); n > 0 && g[n-1].Type == agentapi.TypeTextDelta && n < len(w) {
		// a stream cut in a turn's text has some of it
		w = slices.Clone(w[:n])
		w[n-1].Text = w[n-1].Text[:min(len(w[n-1].Text), len(g[n-1].Text))]
	}
	return jsonEqual(g, w[:len(g)])
}
