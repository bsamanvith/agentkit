package wsapi_test

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wsapi"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/agentclient/wsclient"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/store/scoped"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// testToken is the bearer token a rig requires.
const testToken = "wsapi-test-token-0123456789"

// model answers with scripted replies, in order. With a gate, each reply
// waits for the gate to be closed.
type model struct {
	tb      testing.TB
	replies []llmtest.Reply
	gate    chan struct{}
	calls   atomic.Int64
}

func (m *model) Generate(ctx context.Context, req llm.Request) iter.Seq2[llm.Event, error] {
	n := int(m.calls.Add(1)) - 1
	reply := llmtest.Fail(llm.ErrInvalidRequest)
	if n < len(m.replies) {
		reply = m.replies[n]
	} else {
		m.tb.Errorf("model call %d, but only %d replies were scripted", n+1, len(m.replies))
	}
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

func call(id, name, input string) llm.ToolUse {
	return llm.ToolUse{ID: id, Name: name, Input: json.RawMessage(input)}
}

// rig is a real agent API (fake model, the demo toolset) and its WebSocket
// binding on one httptest server, as serve mounts them. It records the
// requests that reached the API over HTTP, and its listener can hold the
// server's writes to a connection.
type rig struct {
	t   *testing.T
	api *agentapi.Server
	ws  *wsapi.Server
	ts  *httptest.Server
	ln  *gateListener

	mu     sync.Mutex
	http   []string // "METHOD /path" of the requests served over HTTP
	served []string // "METHOD /path" of the requests the binding served
}

type rigConfig struct {
	harness []harness.Option
	api     []agentapi.Option
	ws      []wsapi.Option
	noToken bool // the API's auth comes from api
}

func newRig(t *testing.T, m *model, rc rigConfig) *rig {
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
	h, err := harness.New(m, sets["a"], rc.harness...)
	if err != nil {
		t.Fatal(err)
	}
	var ids atomic.Int64
	opts := []agentapi.Option{
		agentapi.WithStore(scoped.Memory(nil)), agentapi.WithKeepAlive(0),
		agentapi.WithNewID(func(prefix string) string { return fmt.Sprintf("%s%d", prefix, ids.Add(1)) }),
	}
	if !rc.noToken {
		opts = append(opts, agentapi.WithToken(testToken))
	}
	api, err := agentapi.New(h, append(opts, rc.api...)...)
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{t: t, api: api}
	apiHandler := api.Handler()
	ws, err := wsapi.New(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.served = append(r.served, req.Method+" "+req.URL.Path)
		r.mu.Unlock()
		apiHandler.ServeHTTP(w, req)
	}), append([]wsapi.Option{wsapi.WithHeartbeat(0, 0)}, rc.ws...)...)
	if err != nil {
		t.Fatal(err)
	}
	r.ws = ws
	mux := http.NewServeMux()
	mux.Handle(wire.WSPath, ws.Handler())
	mux.Handle("/v1/", http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.mu.Lock()
		r.http = append(r.http, req.Method+" "+req.URL.Path)
		r.mu.Unlock()
		apiHandler.ServeHTTP(w, req)
	}))
	r.ts = httptest.NewUnstartedServer(mux)
	r.ln = &gateListener{Listener: r.ts.Listener}
	r.ts.Listener = r.ln
	r.ts.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = ws.Shutdown(ctx)
		_ = api.Shutdown(ctx)
		r.ts.Close()
	})
	return r
}

// httpRequests are the requests that reached the API over HTTP so far.
func (r *rig) httpRequests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.http...)
}

// wsRequests are the requests the binding served by the API so far.
func (r *rig) wsRequests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.served...)
}

// httpClient is a client of the API over HTTP and SSE.
func (r *rig) httpClient(token string) *agentclient.Client {
	r.t.Helper()
	c, err := agentclient.New(r.ts.URL, agentclient.WithToken(token), agentclient.WithHTTPClient(r.ts.Client()))
	if err != nil {
		r.t.Fatal(err)
	}
	return c
}

// wsClient is a client of the API whose streams and commands go over the
// WebSocket binding.
func (r *rig) wsClient(token string) *agentclient.Client {
	r.t.Helper()
	tr, err := wsclient.New(r.ts.URL, wsclient.WithToken(token), wsclient.WithFallback(r.ts.Client().Transport), wsclient.WithDialClient(r.ts.Client()))
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { _ = tr.Close() })
	c, err := agentclient.New(r.ts.URL, agentclient.WithToken(token), agentclient.WithHTTPClient(&http.Client{Transport: tr}))
	if err != nil {
		r.t.Fatal(err)
	}
	return c
}

// raw is a WebSocket connection to the binding, spoken to message by
// message; conn is the server's end of it.
type raw struct {
	t    *testing.T
	c    *websocket.Conn
	conn *gateConn
}

func (r *rig) dial() *raw {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(r.t.Context(), 10*time.Second)
	defer cancel()
	n := r.ln.count()
	c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(r.ts.URL, "http")+wire.WSPath, nil)
	if err != nil {
		r.t.Fatal(err)
	}
	c.SetReadLimit(16 << 20)
	r.t.Cleanup(func() { _ = c.CloseNow() })
	return &raw{t: r.t, c: c, conn: r.ln.conn(n)}
}

func (w *raw) send(m wire.WSMessage) {
	w.t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		w.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(w.t.Context(), 10*time.Second)
	defer cancel()
	if err := w.c.Write(ctx, websocket.MessageText, data); err != nil {
		w.t.Fatal(err)
	}
}

// recv reads the next message; at a close, it fails the test unless
// closed is given, which then gets the close code.
func (w *raw) recv() (wire.WSMessage, error) {
	ctx, cancel := context.WithTimeout(w.t.Context(), 20*time.Second)
	defer cancel()
	_, data, err := w.c.Read(ctx)
	if err != nil {
		return wire.WSMessage{}, err
	}
	var m wire.WSMessage
	if err := json.Unmarshal(data, &m); err != nil {
		w.t.Fatalf("message %s: %v", data, err)
	}
	return m, nil
}

func (w *raw) must() wire.WSMessage {
	w.t.Helper()
	m, err := w.recv()
	if err != nil {
		w.t.Fatalf("reading a message: %v", err)
	}
	return m
}

// closed reads until the connection closes and returns its close code.
func (w *raw) closed() websocket.StatusCode {
	w.t.Helper()
	for {
		m, err := w.recv()
		if err != nil {
			return websocket.CloseStatus(err)
		}
		w.t.Logf("before the close: %+v", m)
	}
}

func (w *raw) hello(token string) wire.WSMessage {
	w.t.Helper()
	w.send(wire.WSMessage{Type: wire.WSHello, ID: "hello", Token: token})
	m := w.must()
	if m.Type != wire.WSResult || m.ID != "hello" {
		w.t.Fatalf("hello answered %+v", m)
	}
	return m
}

func codeOf(t *testing.T, body json.RawMessage) string {
	t.Helper()
	var e wire.ErrorResponse
	if err := json.Unmarshal(body, &e); err != nil || e.Error == nil {
		t.Fatalf("body %s is not an error: %v", body, err)
	}
	return e.Error.Code
}

// gateListener hands out connections whose writes the test can hold.
type gateListener struct {
	net.Listener
	mu    sync.Mutex
	conns []*gateConn
}

func (l *gateListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	g := &gateConn{Conn: c, open: closedChan(), done: make(chan struct{}), blocked: make(chan struct{}, 1)}
	l.mu.Lock()
	l.conns = append(l.conns, g)
	l.mu.Unlock()
	return g, nil
}

func (l *gateListener) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.conns)
}

// conn is the connection accepted after the first n.
func (l *gateListener) conn(n int) *gateConn {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.conns) <= n {
		return nil
	}
	return l.conns[n]
}

func closedChan() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

// gateConn is a server-side connection whose writes wait while it is held.
type gateConn struct {
	net.Conn
	mu       sync.Mutex
	open     chan struct{}
	blocked  chan struct{} // a write waits at the gate
	done     chan struct{}
	doneOnce sync.Once
}

func (g *gateConn) hold() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.open = make(chan struct{})
}

func (g *gateConn) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	select {
	case <-g.open:
	default:
		close(g.open)
	}
}

func (g *gateConn) Write(p []byte) (int, error) {
	g.mu.Lock()
	ch := g.open
	g.mu.Unlock()
	select {
	case <-ch:
	default:
		select { // a write waits: tell the test
		case g.blocked <- struct{}{}:
		default:
		}
		select {
		case <-ch:
		case <-g.done:
			return 0, net.ErrClosed
		}
	}
	return g.Conn.Write(p)
}

// waitBlocked waits until a write of the server's waits at the gate.
func (g *gateConn) waitBlocked(t *testing.T) {
	t.Helper()
	select {
	case <-g.blocked:
	case <-time.After(20 * time.Second):
		t.Fatal("the server never wrote to the held connection")
	}
}

func (g *gateConn) Close() error {
	g.doneOnce.Do(func() { close(g.done) })
	return g.Conn.Close()
}

func (g *gateConn) isClosed() bool {
	select {
	case <-g.done:
		return true
	default:
		return false
	}
}
