package agentclient_test

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
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// testToken is the bearer token every rig requires.
const testToken = "client-test-token-0123456789"

// model answers the requests with scripted replies, in order, and records
// them. With a gate, reply n (from 0) waits for the gate unless n < pass.
type model struct {
	tb      testing.TB
	replies []llmtest.Reply
	gate    chan struct{} // nil: answer at once
	pass    int           // replies that skip the gate

	mu   sync.Mutex
	reqs []llm.Request
}

func (m *model) Generate(ctx context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	m.mu.Lock()
	n := len(m.reqs)
	m.reqs = append(m.reqs, req.Clone())
	m.mu.Unlock()
	reply := llmtest.Fail(llm.ErrInvalidRequest)
	if n < len(m.replies) {
		reply = m.replies[n]
	} else {
		m.tb.Errorf("model call %d, but only %d replies were scripted", n+1, len(m.replies))
	}
	inner := llmtest.NewFake(m.tb, reply).Generate(ctx, req)
	return func(yield func(llm.Event, error) bool) {
		if m.gate != nil && n >= m.pass {
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

// rig is a real agent API (fake model, the demo tools, a token) on an
// httptest server, with a client of it. It records the bytes the server
// wrote for every streamed run, by run ID.
type rig struct {
	t      *testing.T
	srv    *agentapi.Server
	ts     *httptest.Server
	model  *model
	client *agentclient.Client

	mu    sync.Mutex
	wrote map[string]*bytes.Buffer
}

func newRig(t *testing.T, m *model, api ...agentapi.Option) *rig {
	t.Helper()
	m.tb = t
	catalog, err := toolset.NewCatalog(toolset.Demo())
	if err != nil {
		t.Fatal(err)
	}
	sets, _, err := toolset.Build(t.Context(), catalog, []toolset.Spec{
		{Name: "a", Type: toolset.DemoType, Settings: json.RawMessage(`{"echo_prefix":"[a] "}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	h, err := harness.New(m, sets["a"])
	if err != nil {
		t.Fatal(err)
	}
	var ids atomic.Int64
	opts := []agentapi.Option{
		agentapi.WithToken(testToken), agentapi.WithKeepAlive(0),
		agentapi.WithNewID(func(prefix string) string { return fmt.Sprintf("%s%d", prefix, ids.Add(1)) }),
	}
	srv, err := agentapi.New(h, append(opts, api...)...)
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, srv: srv, model: m, wrote: map[string]*bytes.Buffer{}}
	handler := srv.Handler()
	r.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			handler.ServeHTTP(w, req)
			return
		}
		tee := &teeWriter{ResponseWriter: w}
		handler.ServeHTTP(tee, req)
		if id := w.Header().Get("Agentkit-Run-Id"); id != "" {
			r.mu.Lock()
			r.wrote[id] = &tee.buf
			r.mu.Unlock()
		}
	}))
	t.Cleanup(r.ts.Close)
	t.Cleanup(func() { // ends the runs a test left waiting at the gate
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	r.client = r.newClient(agentclient.WithToken(testToken))
	return r
}

func (r *rig) newClient(opts ...agentclient.Option) *agentclient.Client {
	r.t.Helper()
	c, err := agentclient.New(r.ts.URL, append([]agentclient.Option{agentclient.WithHTTPClient(r.ts.Client())}, opts...)...)
	if err != nil {
		r.t.Fatal(err)
	}
	return c
}

// serverWrote is the body the server wrote for a streamed run, once its
// handler has returned (shortly after the client read the last event).
func (r *rig) serverWrote(runID string) []byte {
	r.t.Helper()
	var b *bytes.Buffer
	testkit.Eventually(r.t, 30*time.Second, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		b = r.wrote[runID]
		return b != nil
	})
	return b.Bytes()
}

// waitEnded waits until the run is no longer running and returns it.
func (r *rig) waitEnded(id string) wire.Run {
	r.t.Helper()
	var run wire.Run
	testkit.Eventually(r.t, 30*time.Second, func() bool {
		var err error
		if run, err = r.client.GetRun(r.t.Context(), id); err != nil {
			r.t.Fatal(err)
		}
		return run.Status != wire.StatusRunning
	})
	return run
}

// teeWriter keeps a copy of the body; http.ResponseController reaches the
// real writer through Unwrap to flush.
type teeWriter struct {
	http.ResponseWriter
	buf bytes.Buffer
}

func (w *teeWriter) Write(b []byte) (int, error) {
	w.buf.Write(b)
	return w.ResponseWriter.Write(b)
}

func (w *teeWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// read is what a client read from a stream: every event with its raw data,
// and the error that ended it.
type read struct {
	events []wire.Event
	raw    []string
	err    error
}

// readAll reads s until Next fails (io.EOF included).
func readAll(s *agentclient.Stream) read {
	var out read
	for {
		e, err := s.Next()
		if err != nil {
			out.err = err
			return out
		}
		out.events = append(out.events, e)
		out.raw = append(out.raw, string(s.RawData()))
	}
}

// until reads s up to and including the first event of type typ.
func until(t *testing.T, s *agentclient.Stream, typ string) wire.Event {
	t.Helper()
	for {
		e, err := s.Next()
		if err != nil {
			t.Fatalf("stream ended before a %s event: %v", typ, err)
		}
		if e.Type == typ {
			return e
		}
	}
}

// bodyServer is an HTTP client whose every request is answered with the
// given status, headers and body (no network). The body counts what is read
// from it.
type bodyServer struct {
	status int
	header http.Header
	body   func() io.Reader
	read   atomic.Int64
}

func (b *bodyServer) RoundTrip(req *http.Request) (*http.Response, error) {
	h := b.header.Clone()
	if h == nil {
		h = http.Header{"Content-Type": {"text/event-stream"}}
	}
	return &http.Response{
		StatusCode: cmpOr(b.status, http.StatusOK), Header: h, Request: req,
		Body: &countingBody{r: b.body(), n: &b.read, ctx: req.Context()},
	}, nil
}

func cmpOr(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

// countingBody counts the bytes read and, like a real response body, fails
// once the request's context is done or it is closed.
type countingBody struct {
	r      io.Reader
	n      *atomic.Int64
	ctx    context.Context
	closed atomic.Bool
}

func (c *countingBody) Read(p []byte) (int, error) {
	if c.closed.Load() {
		return 0, errors.New("read on closed body")
	}
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := c.r.Read(p)
	c.n.Add(int64(n))
	return n, err
}

func (c *countingBody) Close() error {
	c.closed.Store(true)
	return nil
}

// streamOf opens a stream whose body is body, through a client with the
// given options.
func streamOf(t *testing.T, body []byte, opts ...agentclient.Option) (*agentclient.Stream, *bodyServer) {
	t.Helper()
	return streamFrom(t, func() io.Reader { return bytes.NewReader(body) }, opts...)
}

func streamFrom(t *testing.T, body func() io.Reader, opts ...agentclient.Option) (*agentclient.Stream, *bodyServer) {
	t.Helper()
	bs := &bodyServer{body: body}
	c, err := agentclient.New("http://agent.test", append([]agentclient.Option{agentclient.WithHTTPClient(&http.Client{Transport: bs})}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.Stream(t.Context(), agentclient.RunRequest{Message: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, bs
}

// referenceSSE is the data of the events in an SSE body, by a direct reading
// of the HTML standard ("Interpreting an event stream") over the whole body:
// a leading BOM is dropped; lines end with CRLF, LF or CR, and a last line
// without an end is dropped; a line starting with a colon is a comment; a
// field's value drops one leading space; data lines are joined with LF; an
// empty line dispatches the data if any was given.
func referenceSSE(body []byte) []string {
	s := strings.TrimPrefix(string(body), "\uFEFF")
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\r':
			lines = append(lines, s[start:i])
			if i+1 < len(s) && s[i+1] == '\n' {
				i++
			}
			start = i + 1
		case '\n':
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	var out []string
	buf := ""
	for _, line := range lines {
		switch {
		case line == "":
			if buf != "" {
				out = append(out, strings.TrimSuffix(buf, "\n"))
			}
			buf = ""
		case line[0] == ':':
		default:
			name, value, _ := strings.Cut(line, ":")
			if name == "data" {
				buf += strings.TrimPrefix(value, " ") + "\n"
			}
		}
	}
	return out
}

// expectedRead is what the client must make of a body whose events have the
// given data, by the API's rules: each data must be an event (a JSON object
// with a type), else the stream fails with ErrBadResponse there; run_done and
// run_error end it (io.EOF), and a body that ends before them is truncated.
func expectedRead(datas []string) read {
	var out read
	for _, d := range datas {
		var e wire.Event
		if err := json.Unmarshal([]byte(d), &e); err != nil || e.Type == "" {
			out.err = agentclient.ErrBadResponse
			return out
		}
		out.events = append(out.events, e)
		out.raw = append(out.raw, d)
		if e.Type == wire.TypeRunDone || e.Type == wire.TypeRunError {
			out.err = io.EOF
			return out
		}
	}
	out.err = agentclient.ErrStreamTruncated
	return out
}

// sameRead reports how got differs from want: the same events (by their
// JSON), the same raw data, and the same way to end (endsAs).
func sameRead(got, want read) string {
	switch {
	case !jsonEqual(got.events, want.events):
		return fmt.Sprintf("events\n%s\nwant\n%s", jsonOf(got.events), jsonOf(want.events))
	case !slices.Equal(got.raw, want.raw):
		return fmt.Sprintf("raw data\n%q\nwant\n%q", got.raw, want.raw)
	}
	return endsAs(got.err, want.err)
}

// streamEnds are the ways Next ends a stream; each error it returns matches
// exactly one.
var streamEnds = []error{
	io.EOF, agentclient.ErrStreamTruncated, agentclient.ErrClosed, agentclient.ErrBadResponse,
	agentclient.ErrResponseTooLarge, context.Canceled, context.DeadlineExceeded,
}

// endsAs reports how err differs from the end want: it must match want and no
// other of streamEnds (a cancelled stream is not also truncated).
func endsAs(err, want error) string {
	if !errors.Is(err, want) {
		return fmt.Sprintf("ended with %v, want %v", err, want)
	}
	for _, other := range streamEnds {
		if !errors.Is(want, other) && errors.Is(err, other) {
			return fmt.Sprintf("ended with %v: %v, but also %v", err, want, other)
		}
	}
	return ""
}

func jsonOf(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(err.Error())
	}
	return b
}

func jsonEqual(a, b any) bool { return bytes.Equal(jsonOf(a), jsonOf(b)) }

func call(id, name, input string) llm.ToolUse {
	return llm.ToolUse{ID: id, Name: name, Input: json.RawMessage(input)}
}
