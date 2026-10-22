package wsapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
)

// csrfHeader is the header a session's CSRF token goes in (the login
// package's CSRFHeader, which a test compares it with): wsapi does not link
// the login package.
const csrfHeader = "X-Agentkit-CSRF"

// route is the HTTP request a command stands for.
type route struct {
	method string
	verb   string // the path after /v1/runs/{id}/; "" for POST /v1/runs
	run    bool   // the path names the run (RunID)
	task   bool   // the path is /v1/tasks/{id}/verb: a task's, by its run's ID
	stream bool   // the response is an event stream
}

func (rt route) path(runID string) string {
	if !rt.run {
		return "/v1/runs"
	}
	if rt.task {
		return "/v1/tasks/" + url.PathEscape(runID) + "/" + rt.verb
	}
	return "/v1/runs/" + url.PathEscape(runID) + "/" + rt.verb
}

// routeOf is the route of a command type.
func routeOf(typ string) (route, bool) {
	switch typ {
	case wire.WSSubscribe:
		return route{method: http.MethodGet, verb: "events", run: true, stream: true}, true
	case wire.WSSend:
		return route{method: http.MethodPost, stream: true}, true
	case wire.WSResume:
		return route{method: http.MethodPost, verb: "resume", run: true, stream: true}, true
	case wire.WSStop:
		return route{method: http.MethodPost, verb: "stop", run: true}, true
	case wire.WSCancel:
		return route{method: http.MethodPost, verb: "cancel", run: true}, true
	case wire.WSAnswer:
		return route{method: http.MethodPost, verb: "input", run: true}, true
	case wire.WSDecide:
		return route{method: http.MethodPost, verb: "decisions", run: true}, true
	case wire.WSSteer:
		return route{method: http.MethodPost, verb: "messages", run: true, task: true}, true
	case wire.WSPause:
		return route{method: http.MethodPost, verb: "pause", run: true, task: true}, true
	case wire.WSResumeTask:
		return route{method: http.MethodPost, verb: "resume", run: true, task: true}, true
	}
	return route{}, false
}

// creds are what a hello gave: a bearer token and a session's CSRF token.
// Empty, the upgrade request's own credentials (its Authorization header,
// its cookies) stand.
type creds struct{ token, csrf string }

// op is a command or stream going on.
type op struct {
	cancel context.CancelFunc
	stream bool
}

// conn is one WebSocket connection: a reader that takes the client's
// messages, a goroutine per command or stream that serves it through the
// API, and a writer that sends what they put in the outbox.
type conn struct {
	s      *Server
	ws     *websocket.Conn
	up     *http.Request // the upgrade request
	ctx    context.Context
	cancel context.CancelFunc
	out    *outbox
	authed atomic.Bool

	// Only the reader uses these.
	creds   creds
	closing bool // the connection closes: messages are not read any more

	mu       sync.Mutex
	ops      map[string]*op
	streams  int
	commands int
	wg       sync.WaitGroup // ops
}

func newConn(s *Server, ws *websocket.Conn, up *http.Request) *conn {
	c := &conn{s: s, ws: ws, up: up, ops: map[string]*op{}}
	c.ctx, c.cancel = context.WithCancel(context.WithoutCancel(up.Context()))
	c.out = newOutbox(s.cfg.queue, func() {
		c.cancelOps()
		if s.slow != nil {
			s.slow()
		}
	})
	return c
}

// run serves the connection until it closes.
func (c *conn) run() {
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		c.write()
	}()
	stopHeartbeat := c.heartbeat()
	stopHello := c.helloDeadline()
	c.read()
	// Reading ended: the client closed or went away, or answered the
	// close the writer sent. The connection is done; a Close still waiting
	// for its handshake would wait for the reader, which is gone.
	_ = c.ws.CloseNow()
	c.cancel() // the ops, the writer, the heartbeats and the hello deadline stop
	stopHello()
	stopHeartbeat()
	c.wg.Wait()
	<-writerDone
	_ = c.ws.CloseNow()
}

// read takes the client's messages until the connection fails or closes.
func (c *conn) read() {
	for {
		typ, data, err := c.ws.Read(c.ctx)
		if err != nil {
			return
		}
		if c.closing {
			continue
		}
		if typ != websocket.MessageText {
			c.closeWith(websocket.StatusUnsupportedData, "JSON text messages only")
			continue
		}
		c.handle(data)
	}
}

// write sends the outbox's messages, in order; once the outbox is shut and
// empty, it closes the connection with the outbox's code.
func (c *conn) write() {
	for {
		msg, code, reason, ok := c.out.next(c.ctx)
		if !ok {
			return
		}
		if msg == nil {
			ctx, cancel := context.WithTimeout(c.ctx, c.s.cfg.writeTimeout)
			go func() { // Close waits for the client's close
				<-ctx.Done()
				_ = c.ws.CloseNow()
			}()
			_ = c.ws.Close(code, reason)
			cancel()
			return
		}
		ctx, cancel := context.WithTimeout(c.ctx, c.s.cfg.writeTimeout)
		err := c.ws.Write(ctx, websocket.MessageText, msg)
		cancel()
		if err != nil {
			if c.ctx.Err() == nil {
				c.s.cfg.log.Info("websocket: writing failed; closing", "err", err)
			}
			_ = c.ws.CloseNow()
			return
		}
	}
}

// heartbeat pings the client every heartbeat, and closes a connection
// whose pong is late. The function it returns stops it.
func (c *conn) heartbeat() func() {
	if c.s.cfg.heartbeat == 0 {
		return func() {}
	}
	t := c.s.cfg.clock.NewTicker(c.s.cfg.heartbeat)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-t.C():
			case <-c.ctx.Done():
				return
			}
			ctx, cancel := context.WithTimeout(c.ctx, c.s.cfg.pongTimeout)
			err := c.ws.Ping(ctx)
			cancel()
			if err != nil {
				if c.ctx.Err() == nil {
					c.s.cfg.log.Info("websocket: no pong; closing", "err", err)
				}
				_ = c.ws.CloseNow()
				return
			}
		}
	}()
	return func() {
		t.Stop()
		<-done
	}
}

// helloDeadline closes the connection if no hello succeeded in time.
func (c *conn) helloDeadline() func() {
	t := c.s.cfg.clock.NewTimer(c.s.cfg.helloTimeout)
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-t.C():
			if !c.authed.Load() {
				c.out.shut(wire.WSCloseUnauthorized, "no hello")
			}
		case <-c.ctx.Done():
		}
	}()
	return func() {
		t.Stop()
		<-done
	}
}

// closeWith closes the connection with code, after what the outbox holds.
func (c *conn) closeWith(code websocket.StatusCode, reason string) {
	c.closing = true
	c.out.shut(code, reason)
}

// goAway closes the connection for the server's shutdown.
func (c *conn) goAway() {
	c.cancelOps()
	c.out.shut(websocket.StatusGoingAway, "server shutting down")
}

// cancelOps cancels every command and stream going on.
func (c *conn) cancelOps() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, o := range c.ops {
		o.cancel()
	}
}

// handle acts on one message of the client's.
func (c *conn) handle(data []byte) {
	var m wire.WSMessage
	if err := json.Unmarshal(data, &m); err != nil || m.Type == "" {
		c.fail(m.ID, http.StatusBadRequest, wire.CodeInvalidRequest, `a message is a JSON object with a "type"`)
		return
	}
	if m.Type == wire.WSHello {
		c.hello(m)
		return
	}
	if !c.authed.Load() {
		c.fail(m.ID, http.StatusUnauthorized, wire.CodeUnauthorized, "send a hello first")
		c.closeWith(wire.WSCloseUnauthorized, "hello first")
		return
	}
	if m.Type == wire.WSUnsubscribe {
		c.unsubscribe(m)
		return
	}
	rt, ok := routeOf(m.Type)
	switch {
	case !ok:
		c.fail(m.ID, http.StatusBadRequest, wire.CodeInvalidRequest, fmt.Sprintf("no message type %q", m.Type))
		return
	case m.ID == "":
		c.fail("", http.StatusBadRequest, wire.CodeInvalidRequest, m.Type+" needs an id")
		return
	case rt.run && m.RunID == "":
		c.fail(m.ID, http.StatusBadRequest, wire.CodeInvalidRequest, m.Type+" needs a run_id")
		return
	}
	ctx, cancel := context.WithCancel(c.ctx)
	c.mu.Lock()
	_, dup := c.ops[m.ID]
	full := rt.stream && c.streams >= c.s.cfg.maxStreams || !rt.stream && c.commands >= c.s.cfg.maxCommands
	if !dup && !full {
		c.ops[m.ID] = &op{cancel: cancel, stream: rt.stream}
		if rt.stream {
			c.streams++
		} else {
			c.commands++
		}
		c.wg.Add(1)
	}
	c.mu.Unlock()
	switch {
	case dup:
		cancel()
		c.fail(m.ID, http.StatusBadRequest, wire.CodeInvalidRequest, fmt.Sprintf("id %q is in use", m.ID))
		return
	case full:
		cancel()
		what := fmt.Sprintf("%d commands are waiting for their result", c.s.cfg.maxCommands)
		if rt.stream {
			what = fmt.Sprintf("%d streams are open", c.s.cfg.maxStreams)
		}
		c.out.put(wire.WSMessage{
			Type: wire.WSResult, ID: m.ID, Status: http.StatusServiceUnavailable, RetryAfter: "1",
			Body: errorBody(wire.CodeOverCapacity, what+" on this connection, the most it takes", true),
		})
		return
	}
	accept := "application/json"
	if rt.stream {
		accept = "text/event-stream"
	}
	req := c.request(ctx, rt.method, rt.path(m.RunID), m.Body, accept, m.After, c.creds)
	if m.Key != "" {
		req.Header.Set("Idempotency-Key", m.Key)
	}
	go func() {
		defer c.wg.Done()
		defer c.finished(m.ID)
		defer cancel()
		c.serveAPI(req, m.ID)
	}()
}

// hello authenticates the connection with m's credentials, by the API's
// GET /v1/auth/me, whose response is the result. A hello that fails closes
// the connection.
func (c *conn) hello(m wire.WSMessage) {
	cr := creds{token: m.Token, csrf: m.CSRF}
	rw := newResponse(c, m.ID)
	c.s.api.ServeHTTP(rw, c.request(c.ctx, http.MethodGet, "/v1/auth/me", nil, "application/json", "", cr))
	ok := rw.status == http.StatusOK
	if ok {
		c.creds = cr
		c.authed.Store(true)
	}
	rw.finish()
	if !ok {
		c.closeWith(wire.WSCloseUnauthorized, "hello refused")
	}
}

// unsubscribe closes the stream m.Sub; its end message follows.
func (c *conn) unsubscribe(m wire.WSMessage) {
	c.mu.Lock()
	o, ok := c.ops[m.Sub]
	if ok && o.stream {
		o.cancel()
	}
	c.mu.Unlock()
	if !ok || !o.stream {
		c.fail(m.ID, http.StatusNotFound, wire.CodeNotFound, fmt.Sprintf("no stream %q is open", m.Sub))
		return
	}
	if m.ID != "" {
		c.out.put(wire.WSMessage{Type: wire.WSResult, ID: m.ID, Status: http.StatusOK})
	}
}

// finished frees the ID of an op that has sent its last message.
func (c *conn) finished(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if o, ok := c.ops[id]; ok {
		delete(c.ops, id)
		if o.stream {
			c.streams--
		} else {
			c.commands--
		}
	}
}

// serveAPI serves req by the API, and sends its response as id's result,
// or as its stream.
func (c *conn) serveAPI(req *http.Request, id string) {
	rw := newResponse(c, id)
	defer func() {
		if p := recover(); p != nil && p != http.ErrAbortHandler { //nolint:errorlint // ErrAbortHandler is panicked as is
			c.s.cfg.log.Error("websocket: the API handler panicked", "method", req.Method, "path", req.URL.Path, "panic", fmt.Sprint(p))
			if rw.status == 0 {
				rw.reset(http.StatusInternalServerError, errorBody(wire.CodeInternal, "serving the command failed; see the server log", false))
			}
		}
		rw.finish()
	}()
	c.s.api.ServeHTTP(rw, req)
}

// request is the HTTP request a command stands for: the upgrade request's
// headers (its cookies, its Authorization, any tenant header a proxy set),
// except the WebSocket ones, with cr's credentials over them.
func (c *conn) request(ctx context.Context, method, path string, body []byte, accept, after string, cr creds) *http.Request {
	u, err := url.ParseRequestURI(path)
	if err != nil { // cannot happen: the run ID is escaped
		u = &url.URL{Path: path}
	}
	r := &http.Request{
		Method: method, URL: u,
		Proto: c.up.Proto, ProtoMajor: c.up.ProtoMajor, ProtoMinor: c.up.ProtoMinor,
		Header: http.Header{}, Host: c.up.Host, RemoteAddr: c.up.RemoteAddr, TLS: c.up.TLS,
		RequestURI: path, ContentLength: int64(len(body)), Body: http.NoBody,
	}
	for k, vs := range c.up.Header {
		switch http.CanonicalHeaderKey(k) {
		case "Upgrade", "Connection", "Sec-Websocket-Key", "Sec-Websocket-Version", "Sec-Websocket-Extensions",
			"Sec-Websocket-Protocol", "Content-Length", "Content-Type", "Accept", "Accept-Encoding",
			"Last-Event-Id", "Idempotency-Key", "Transfer-Encoding", "Expect":
			continue
		}
		r.Header[k] = vs
	}
	r.Header.Set("Accept", accept)
	if len(body) > 0 {
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	if after != "" {
		r.Header.Set("Last-Event-Id", after)
	}
	if cr.token != "" {
		r.Header.Set("Authorization", "Bearer "+cr.token)
	}
	if cr.csrf != "" {
		r.Header.Set(csrfHeader, cr.csrf)
	}
	return r.WithContext(ctx)
}

// fail answers the message id with an API error.
func (c *conn) fail(id string, status int, code, message string) {
	c.out.put(wire.WSMessage{Type: wire.WSResult, ID: id, Status: status, Body: errorBody(code, message, false)})
}

// errorBody is the API's error body.
func errorBody(code, message string, retryable bool) json.RawMessage {
	b, _ := json.Marshal(wire.ErrorResponse{V: wire.Version, Error: &wire.Error{Code: code, Message: message, Retryable: retryable}})
	return b
}

// writeError answers a plain HTTP request with the API's error body.
func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(append(errorBody(code, message, false), '\n'))
}
