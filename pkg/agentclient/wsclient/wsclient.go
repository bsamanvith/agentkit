// Package wsclient carries the agent API's run commands and event streams
// over one WebSocket connection (T091, [wire.WSPath]), behind the same
// client: [Transport] is an [http.RoundTripper] for
// [github.com/bsamanvith/agentkit/pkg/agentclient], so every method of its
// Client works as it does over HTTP, with the same results and errors.
//
//	t, err := wsclient.New(baseURL, wsclient.WithToken(token))
//	defer t.Close()
//	c, err := agentclient.New(baseURL, agentclient.WithHTTPClient(&http.Client{Transport: t}))
//
// A run's stream (Stream, Events, Resume), the commands to a run (Stop,
// Cancel, Answer, Decide) and to a task (SteerTask, PauseTask, ResumeTask)
// go over the connection, which the transport dials
// on first use and dials again after it was lost; every other request (the
// lists, the resources) goes over HTTP ([WithFallback]). A stream the
// server cut off because the client read too slowly ends with an error
// matching [ErrGap], whose [*GapError] names the event to follow it from.
//
// It is apart from agentclient so that a program that does not use it does
// not link a WebSocket library.
package wsclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
)

var (
	// ErrInvalidConfig reports an unusable argument to [New].
	ErrInvalidConfig = errors.New("invalid WebSocket transport configuration")
	// ErrClosed is returned once [Transport.Close] was called.
	ErrClosed = errors.New("websocket transport closed")
	// ErrConnectionLost ends the streams and commands of a connection that
	// broke or that the server closed; the next request dials again.
	ErrConnectionLost = errors.New("websocket connection lost")
	// ErrGap ends a stream the server cut off because the client read too
	// slowly. See [GapError].
	ErrGap = errors.New("stream cut off: the client read too slowly")
)

// GapError is the error of a stream the server cut off ([wire.WSGap]): After
// is the ID of the last event the server sent on it, to follow the run
// from with agentclient's Events. It matches [ErrGap].
type GapError struct{ After string }

func (e *GapError) Error() string {
	return fmt.Sprintf("%v; follow the run again after event %q", ErrGap, e.After)
}

// Is reports whether target is [ErrGap].
func (e *GapError) Is(target error) bool { return target == ErrGap }

// Option configures [New].
type Option func(*Transport)

// WithToken authenticates the connection with this bearer token (its
// hello). [Transport.Renew] replaces it.
func WithToken(token string) Option { return func(t *Transport) { t.token = token } }

// WithFallback sets the transport of the requests that do not go over the
// WebSocket (default [http.DefaultTransport]).
func WithFallback(rt http.RoundTripper) Option { return func(t *Transport) { t.fallback = rt } }

// WithDialClient sets the HTTP client the WebSocket handshake is made with
// (default: one on [http.DefaultTransport]).
func WithDialClient(hc *http.Client) Option { return func(t *Transport) { t.dialClient = hc } }

// WithHeader adds headers to the handshake request, such as an Origin and
// a session cookie.
func WithHeader(h http.Header) Option { return func(t *Transport) { t.header = h.Clone() } }

// WithMaxMessageBytes sets the largest message the transport reads (default
// 16 MiB, agentclient's default response limit).
func WithMaxMessageBytes(n int64) Option { return func(t *Transport) { t.maxMessage = n } }

// Transport is an [http.RoundTripper] that sends a run's streams and
// commands over one WebSocket connection. It is safe for concurrent use.
type Transport struct {
	wsURL      string
	basePath   string // the base URL's path, without a trailing slash
	token      string
	fallback   http.RoundTripper
	dialClient *http.Client
	header     http.Header
	maxMessage int64

	mu     sync.Mutex
	sess   *session
	dialed chan struct{} // closed when a dial going on ends; nil when none is
	closed bool
}

// New returns a transport for the agent API at baseURL, the URL its /v1 is
// under (as agentclient.New takes it). It dials nothing yet.
func New(baseURL string, opts ...Option) (*Transport, error) {
	u, err := url.Parse(baseURL)
	switch {
	case err != nil:
		return nil, fmt.Errorf("%w: base URL: %w", ErrInvalidConfig, err)
	case u.Scheme != "http" && u.Scheme != "https", u.Host == "":
		return nil, fmt.Errorf("%w: base URL %q is not an absolute http or https URL", ErrInvalidConfig, baseURL)
	case u.RawQuery != "" || u.Fragment != "":
		return nil, fmt.Errorf("%w: base URL %q has a query or fragment", ErrInvalidConfig, baseURL)
	}
	t := &Transport{basePath: strings.TrimRight(u.Path, "/"), fallback: http.DefaultTransport, maxMessage: 16 << 20}
	for _, o := range opts {
		o(t)
	}
	switch {
	case t.fallback == nil:
		return nil, fmt.Errorf("%w: nil fallback transport", ErrInvalidConfig)
	case t.maxMessage < 1:
		return nil, fmt.Errorf("%w: the message limit must be at least 1 byte", ErrInvalidConfig)
	}
	ws := *u
	ws.Scheme = map[string]string{"http": "ws", "https": "wss"}[u.Scheme]
	ws.Path = t.basePath + wire.WSPath
	t.wsURL = ws.String()
	return t, nil
}

// Close closes the connection; the streams and commands on it end with
// [ErrConnectionLost], and later requests that would use it fail with
// [ErrClosed].
func (t *Transport) Close() error {
	t.mu.Lock()
	t.closed = true
	s := t.sess
	t.sess = nil
	t.mu.Unlock()
	if s != nil {
		s.close(websocket.StatusNormalClosure)
	}
	return nil
}

// Renew authenticates the connection with token from now on (a hello),
// and the connections dialed later. A token the server refuses closes the
// connection and is an error.
func (t *Transport) Renew(ctx context.Context, token string) error {
	t.mu.Lock()
	t.token = token
	s := t.sess
	t.mu.Unlock()
	if s == nil {
		return nil
	}
	res, err := s.call(ctx, wire.WSMessage{Type: wire.WSHello, Token: token}, nil)
	if err != nil {
		return err
	}
	if res.Status != http.StatusOK {
		return fmt.Errorf("the server refused the token: status %d: %s", res.Status, res.Body)
	}
	return nil
}

// command is the message a request stands for.
func (t *Transport) command(req *http.Request) (wire.WSMessage, bool) {
	p := strings.TrimPrefix(req.URL.EscapedPath(), t.basePath)
	if strings.HasPrefix(p, "/v1/tasks/") && req.Method == http.MethodPost {
		return taskCommand(req, strings.TrimPrefix(p, "/v1/tasks/"))
	}
	if !strings.HasPrefix(p, "/v1/runs") {
		return wire.WSMessage{}, false
	}
	parts := strings.Split(strings.TrimPrefix(p, "/v1/runs"), "/")
	stream := strings.Contains(req.Header.Get("Accept"), "text/event-stream")
	if len(parts) == 1 && parts[0] == "" {
		if req.Method != http.MethodPost || !stream {
			return wire.WSMessage{}, false
		}
		return wire.WSMessage{Type: wire.WSSend}, true
	}
	if len(parts) != 3 || parts[0] != "" || parts[1] == "" {
		return wire.WSMessage{}, false
	}
	id, err := url.PathUnescape(parts[1])
	if err != nil {
		return wire.WSMessage{}, false
	}
	m := wire.WSMessage{RunID: id}
	switch {
	case req.Method == http.MethodGet && parts[2] == "events":
		m.Type, m.After = wire.WSSubscribe, req.Header.Get("Last-Event-Id")
	case req.Method == http.MethodPost && parts[2] == "resume":
		m.Type = wire.WSResume
	case req.Method == http.MethodPost && parts[2] == "stop":
		m.Type = wire.WSStop
	case req.Method == http.MethodPost && parts[2] == "cancel":
		m.Type = wire.WSCancel
	case req.Method == http.MethodPost && parts[2] == "input":
		m.Type = wire.WSAnswer
	case req.Method == http.MethodPost && parts[2] == "decisions":
		m.Type = wire.WSDecide
	default:
		return wire.WSMessage{}, false
	}
	return m, true
}

// taskCommand is the message of a POST /v1/tasks/{id}/{verb}; p is the
// path after /v1/tasks/.
func taskCommand(req *http.Request, p string) (wire.WSMessage, bool) {
	parts := strings.Split(p, "/")
	if len(parts) != 2 || parts[0] == "" {
		return wire.WSMessage{}, false
	}
	id, err := url.PathUnescape(parts[0])
	if err != nil {
		return wire.WSMessage{}, false
	}
	m := wire.WSMessage{RunID: id, Key: req.Header.Get("Idempotency-Key")}
	switch parts[1] {
	case "messages":
		m.Type = wire.WSSteer
	case "pause":
		m.Type = wire.WSPause
	case "resume":
		m.Type = wire.WSResumeTask
	default:
		return wire.WSMessage{}, false
	}
	return m, true
}

// streams reports whether a message's result opens a stream.
func streams(typ string) bool {
	return typ == wire.WSSend || typ == wire.WSSubscribe || typ == wire.WSResume
}

// RoundTrip implements [http.RoundTripper]: a run's stream or command goes
// over the connection, anything else to the fallback.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	m, ok := t.command(req)
	if !ok {
		return t.fallback.RoundTrip(req)
	}
	if req.Body != nil {
		body, err := io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
		if body = bytes.TrimSpace(body); len(body) > 0 {
			if !json.Valid(body) { // the API's 400 comes from the API
				return t.fallback.RoundTrip(withBody(req, body))
			}
			m.Body = body
		}
	}
	s, refused, err := t.session(req.Context())
	if err != nil {
		return nil, err
	}
	if refused != nil {
		return response(req, *refused, nil), nil
	}
	var sb *streamBody
	if streams(m.Type) {
		sb = newStreamBody()
	}
	res, err := s.call(req.Context(), m, sb)
	if err != nil {
		return nil, err
	}
	if sb != nil && res.Status == http.StatusOK {
		sb.onClose = func() { s.unsubscribe(res.ID, sb) }
		stop := context.AfterFunc(req.Context(), func() { _ = sb.Close() })
		sb.stop = stop
		return response(req, res, sb), nil
	}
	return response(req, res, nil), nil
}

func withBody(req *http.Request, body []byte) *http.Request {
	r := req.Clone(req.Context())
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	return r
}

// response is the HTTP response a result stands for; sb is the body of a
// stream's.
func response(req *http.Request, res wire.WSMessage, sb *streamBody) *http.Response {
	resp := &http.Response{
		Status: strconv.Itoa(res.Status) + " " + http.StatusText(res.Status), StatusCode: res.Status,
		Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
		Header: http.Header{}, Request: req, ContentLength: -1,
	}
	if sb != nil {
		resp.Header.Set("Content-Type", "text/event-stream")
		resp.Header.Set("Agentkit-Run-Id", res.RunID)
		resp.Header.Set("Agentkit-Conversation-Id", res.ConversationID)
		resp.Body = sb
		return resp
	}
	if res.RetryAfter != "" {
		resp.Header.Set("Retry-After", res.RetryAfter)
	}
	body := []byte(res.Body)
	if len(body) > 0 {
		resp.Header.Set("Content-Type", "application/json")
		body = append(body, '\n')
	}
	resp.ContentLength = int64(len(body))
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp
}

// session is the connection, dialed and said hello on; refused is the
// hello's result when the server refused it.
func (t *Transport) session(ctx context.Context) (*session, *wire.WSMessage, error) {
	for {
		t.mu.Lock()
		switch {
		case t.closed:
			t.mu.Unlock()
			return nil, nil, ErrClosed
		case t.sess != nil && !t.sess.isDone():
			s := t.sess
			t.mu.Unlock()
			return s, nil, nil
		case t.dialed != nil: // another request dials: wait for it
			ch := t.dialed
			t.mu.Unlock()
			select {
			case <-ch:
				continue
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
		}
		ch := make(chan struct{})
		t.dialed = ch
		token := t.token
		t.mu.Unlock()
		s, refused, err := t.dial(ctx, token)
		t.mu.Lock()
		t.dialed = nil
		close(ch)
		if err == nil && refused == nil {
			if t.closed {
				t.mu.Unlock()
				s.close(websocket.StatusNormalClosure)
				return nil, nil, ErrClosed
			}
			t.sess = s
		}
		t.mu.Unlock()
		return s, refused, err
	}
}

func (t *Transport) dial(ctx context.Context, token string) (*session, *wire.WSMessage, error) {
	ws, _, err := websocket.Dial(ctx, t.wsURL, &websocket.DialOptions{HTTPClient: t.dialClient, HTTPHeader: t.header})
	if err != nil {
		return nil, nil, fmt.Errorf("dialing %s: %w", t.wsURL, err)
	}
	ws.SetReadLimit(t.maxMessage)
	s := newSession(ws)
	res, err := s.call(ctx, wire.WSMessage{Type: wire.WSHello, Token: token}, nil)
	if err != nil {
		s.close(websocket.StatusNormalClosure)
		return nil, nil, err
	}
	if res.Status != http.StatusOK {
		s.close(websocket.StatusNormalClosure)
		return nil, &res, nil
	}
	return s, nil, nil
}

// session is one connection: a reader that hands results to their callers
// and events to their streams.
type session struct {
	ws     *websocket.Conn
	ids    atomic.Int64
	done   chan struct{}
	dying  atomic.Bool // the server sent a gap: it closes the connection next
	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	waiting map[string]chan wire.WSMessage
	streams map[string]*streamBody
	err     error // why the connection ended
}

func newSession(ws *websocket.Conn) *session {
	s := &session{ws: ws, done: make(chan struct{}), waiting: map[string]chan wire.WSMessage{}, streams: map[string]*streamBody{}}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	go s.read()
	return s
}

func (s *session) isDone() bool {
	if s.dying.Load() {
		return true
	}
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// close closes the connection: it sends the close, and once the reader has
// the server's answer (or after writeTimeout), drops the connection. (A
// Close waits for the reader's lock, which the reader holds.)
func (s *session) close(code websocket.StatusCode) {
	go func() { _ = s.ws.Close(code, "") }()
	t := time.NewTimer(writeTimeout)
	defer t.Stop()
	select {
	case <-s.done:
	case <-t.C:
	}
	_ = s.ws.CloseNow()
	s.cancel()
	<-s.done
}

// call sends m with a new ID and waits for its result. sb, for a message
// that opens a stream, gets the stream's events.
func (s *session) call(ctx context.Context, m wire.WSMessage, sb *streamBody) (wire.WSMessage, error) {
	m.ID = strconv.FormatInt(s.ids.Add(1), 10)
	ch := make(chan wire.WSMessage, 1)
	s.mu.Lock()
	if s.err != nil {
		err := s.err
		s.mu.Unlock()
		return wire.WSMessage{}, err
	}
	s.waiting[m.ID] = ch
	if sb != nil {
		s.streams[m.ID] = sb
	}
	s.mu.Unlock()
	if err := s.send(ctx, m); err != nil {
		s.forget(m.ID)
		return wire.WSMessage{}, err
	}
	select {
	case res, ok := <-ch:
		if !ok {
			return wire.WSMessage{}, s.lost()
		}
		if sb != nil && res.Status != http.StatusOK {
			s.forget(m.ID)
		}
		return res, nil
	case <-ctx.Done():
		s.forget(m.ID)
		if sb != nil { // the stream may have opened: close it
			_ = s.send(context.WithoutCancel(ctx), wire.WSMessage{Type: wire.WSUnsubscribe, Sub: m.ID})
		}
		return wire.WSMessage{}, ctx.Err()
	}
}

func (s *session) send(ctx context.Context, m wire.WSMessage) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := s.ws.Write(ctx, websocket.MessageText, data); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("%w: %w", ErrConnectionLost, err)
	}
	return nil
}

func (s *session) forget(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.waiting, id)
	delete(s.streams, id)
}

func (s *session) lost() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// unsubscribe closes stream id, which the client closed before its end.
func (s *session) unsubscribe(id string, sb *streamBody) {
	s.mu.Lock()
	open := s.streams[id] == sb
	delete(s.streams, id)
	s.mu.Unlock()
	if open {
		ctx, cancel := context.WithTimeout(s.ctx, writeTimeout)
		defer cancel()
		_ = s.send(ctx, wire.WSMessage{Type: wire.WSUnsubscribe, Sub: id})
	}
}

// writeTimeout bounds the unsubscribe of a stream the client closed.
const writeTimeout = 10 * time.Second

// read hands each message the server sends to whom it is for, until the
// connection ends.
func (s *session) read() {
	defer close(s.done)
	var err error
	for {
		var data []byte
		if _, data, err = s.ws.Read(s.ctx); err != nil {
			break
		}
		var m wire.WSMessage
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		s.dispatch(m)
	}
	lost := fmt.Errorf("%w: %w", ErrConnectionLost, err)
	if code := websocket.CloseStatus(err); code != -1 {
		lost = fmt.Errorf("%w: closed with %d", ErrConnectionLost, code)
	}
	s.mu.Lock()
	s.err = lost
	waiting, streams := s.waiting, s.streams
	s.waiting, s.streams = map[string]chan wire.WSMessage{}, map[string]*streamBody{}
	s.mu.Unlock()
	for _, ch := range waiting {
		close(ch)
	}
	for _, sb := range streams {
		sb.finish(lost)
	}
	s.cancel()
}

func (s *session) dispatch(m wire.WSMessage) {
	switch m.Type {
	case wire.WSResult:
		s.mu.Lock()
		ch, ok := s.waiting[m.ID]
		delete(s.waiting, m.ID)
		s.mu.Unlock()
		if ok {
			ch <- m
		}
	case wire.WSEvent:
		s.mu.Lock()
		sb := s.streams[m.Sub]
		s.mu.Unlock()
		if sb != nil {
			sb.push(sseFrame(m.Event))
		}
	case wire.WSEnd, wire.WSGap:
		if m.Type == wire.WSGap {
			s.dying.Store(true) // before the stream sees its end
		}
		s.mu.Lock()
		sb := s.streams[m.Sub]
		delete(s.streams, m.Sub)
		s.mu.Unlock()
		if sb == nil {
			return
		}
		if m.Type == wire.WSGap {
			sb.finish(&GapError{After: m.After})
		} else {
			sb.finish(io.EOF)
		}
	}
}

// sseFrame is an event as the SSE stream has it.
func sseFrame(event json.RawMessage) []byte {
	var head struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	_ = json.Unmarshal(event, &head)
	var b bytes.Buffer
	fmt.Fprintf(&b, "id: %s\nevent: %s\ndata: ", head.ID, head.Type)
	b.Write(event)
	b.WriteString("\n\n")
	return b.Bytes()
}

// streamBody is a stream's response body: its events as SSE, as they come.
// The session's reader waits for a reader of the body that is behind: the
// server sees a slow client, as it would over HTTP.
type streamBody struct {
	frames  chan []byte
	ended   chan struct{}
	closed  chan struct{}
	err     error // after ended
	cur     []byte
	endOnce sync.Once
	once    sync.Once
	onClose func()
	stop    func() bool
}

func newStreamBody() *streamBody {
	return &streamBody{frames: make(chan []byte, 64), ended: make(chan struct{}), closed: make(chan struct{})}
}

func (b *streamBody) push(frame []byte) {
	select {
	case b.frames <- frame:
	case <-b.closed:
	}
}

func (b *streamBody) finish(err error) {
	b.endOnce.Do(func() {
		b.err = err
		close(b.ended)
	})
}

func (b *streamBody) Read(p []byte) (int, error) {
	for len(b.cur) == 0 {
		select {
		case f := <-b.frames:
			b.cur = f
			continue
		default:
		}
		select {
		case f := <-b.frames:
			b.cur = f
		case <-b.ended:
			select {
			case f := <-b.frames:
				b.cur = f
			default:
				return 0, b.err
			}
		case <-b.closed:
			return 0, ErrClosed
		}
	}
	n := copy(p, b.cur)
	b.cur = b.cur[n:]
	return n, nil
}

// Close closes the stream; before its end, the server's stream too.
func (b *streamBody) Close() error {
	b.once.Do(func() {
		close(b.closed)
		if b.stop != nil {
			b.stop()
		}
		select {
		case <-b.ended:
		default:
			if b.onClose != nil {
				b.onClose()
			}
		}
	})
	return nil
}
