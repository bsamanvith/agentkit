package a2a

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The JSON-RPC binding (spec section 9): POST /a2a/{agent}.

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

var nullID = json.RawMessage("null")

func (s *Server) serveJSONRPC(w http.ResponseWriter, r *http.Request, agent string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeRPCError(w, nil, &httpError{status: http.StatusMethodNotAllowed, msg: "JSON-RPC requests are POSTs"})
		return
	}
	body, err := s.readBody(r)
	if err != nil {
		writeRPCError(w, nil, err)
		return
	}
	if t := bytes.TrimSpace(body); len(t) > 0 && t[0] == '[' {
		writeRPCError(w, nil, ErrInvalidRequest.with("batch requests are not supported"))
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeRPCError(w, nil, ErrParse)
		return
	}
	if !validID(req.ID) {
		writeRPCError(w, nil, ErrInvalidRequest.with("the request needs an id: a string or a number"))
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		writeRPCError(w, req.ID, ErrInvalidRequest.with(`want "jsonrpc": "2.0" and a method`))
		return
	}
	ctx, _ := withCaller(r.Context(), r)
	if _, err := s.lookup(ctx, agent); err != nil {
		writeRPCError(w, req.ID, err)
		return
	}
	params := func(v any) error {
		if len(req.Params) == 0 || string(req.Params) == "null" {
			return ErrInvalidParams.with("params are required")
		}
		if err := json.Unmarshal(req.Params, v); err != nil {
			return ErrInvalidParams.with("params: " + jsonProblem(err))
		}
		return nil
	}
	reply := func(res any, err error) {
		if err != nil {
			writeRPCError(w, req.ID, err)
			return
		}
		writeJSON(w, http.StatusOK, rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: res})
	}
	switch req.Method {
	case "SendMessage":
		var p SendMessageRequest
		if err := params(&p); err != nil {
			reply(nil, err)
			return
		}
		t, err := s.sendMessage(ctx, agent, p)
		reply(SendMessageResponse{Task: &t}, err)
	case "SendStreamingMessage":
		var p SendMessageRequest
		if err := params(&p); err != nil {
			reply(nil, err)
			return
		}
		s.stream(w, req.ID, func(emit emitFunc) error { return s.streamMessage(ctx, agent, p, emit) })
	case "GetTask":
		var p GetTaskRequest
		if err := params(&p); err != nil {
			reply(nil, err)
			return
		}
		reply(s.getTask(ctx, agent, p))
	case "ListTasks":
		var p ListTasksRequest
		if len(req.Params) > 0 && string(req.Params) != "null" {
			if err := params(&p); err != nil {
				reply(nil, err)
				return
			}
		}
		reply(s.listTasks(ctx, agent, p))
	case "CancelTask":
		var p TaskIDRequest
		if err := params(&p); err != nil {
			reply(nil, err)
			return
		}
		reply(s.cancelTask(ctx, agent, p))
	case "SubscribeToTask":
		var p TaskIDRequest
		if err := params(&p); err != nil {
			reply(nil, err)
			return
		}
		s.stream(w, req.ID, func(emit emitFunc) error { return s.subscribe(ctx, agent, p, emit) })
	case "CreateTaskPushNotificationConfig", "GetTaskPushNotificationConfig",
		"ListTaskPushNotificationConfigs", "DeleteTaskPushNotificationConfig":
		reply(nil, ErrPushNotSupported)
	case "GetExtendedAgentCard":
		reply(nil, ErrUnsupported.with("this agent has no extended agent card"))
	default:
		reply(nil, ErrMethodNotFound.with("Method not found: "+quote(req.Method)))
	}
}

// validID reports whether id is a JSON-RPC request id this server answers:
// a string or a number (a request without one is a notification, which no
// A2A method is).
func validID(id json.RawMessage) bool {
	t := bytes.TrimSpace(id)
	if len(t) == 0 {
		return false
	}
	switch t[0] {
	case '"':
		var s string
		return json.Unmarshal(t, &s) == nil
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		var n json.Number
		return json.Unmarshal(t, &n) == nil
	}
	return false
}

func jsonProblem(err error) string {
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) && te.Field != "" {
		return te.Field + " has the wrong type"
	}
	return "not the method's parameters"
}

// writeRPCError answers a JSON-RPC request with err: an A2A error as the
// JSON-RPC error of its code, with HTTP status 200; a refused credential or
// an agent that is not there with its HTTP status.
func writeRPCError(w http.ResponseWriter, id json.RawMessage, err error) {
	if id == nil {
		id = nullID
	}
	var he *httpError
	if errors.As(err, &he) {
		setChallenge(w, he)
		code := ErrInvalidRequest.Code
		if he.status == http.StatusNotFound {
			code = ErrMethodNotFound.Code
		}
		writeJSON(w, he.status, rpcResponse{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: he.msg}})
		return
	}
	e := a2aError(err)
	setRetryAfter(w, e)
	writeJSON(w, http.StatusOK, rpcResponse{JSONRPC: "2.0", ID: id, Error: rpcErrorOf(e)})
}

func rpcErrorOf(e *Error) *rpcError {
	re := &rpcError{Code: e.Code, Message: e.Message}
	if d := e.details(); d != nil {
		re.Data = d
	}
	return re
}

// a2aError is err as an A2A error: a context error or any other is internal.
func a2aError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return ErrInternal
}

func setChallenge(w http.ResponseWriter, he *httpError) {
	if he.status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", cmp.Or(he.challenge, "Bearer"))
	}
}

func setRetryAfter(w http.ResponseWriter, e *Error) {
	if e.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int((e.RetryAfter+time.Second-1)/time.Second)))
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		status, b = http.StatusInternalServerError, []byte(`{"error":{"code":500,"status":"INTERNAL","message":"encoding the response failed"}}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(b, '\n'))
}

// The HTTP+JSON binding (spec section 11), under /a2a/{agent}.

func (s *Server) serveREST(w http.ResponseWriter, r *http.Request, agent, sub string) {
	route, id := restRoute(sub)
	method := r.Method
	allow := func(methods ...string) bool {
		for _, m := range methods {
			if method == m {
				return true
			}
		}
		w.Header().Set("Allow", strings.Join(methods, ", "))
		writeRESTError(w, &httpError{status: http.StatusMethodNotAllowed, msg: "use " + strings.Join(methods, " or ")})
		return false
	}
	if route == "" {
		writeRESTError(w, &httpError{status: http.StatusNotFound, msg: "no such endpoint"})
		return
	}
	ctx, _ := withCaller(r.Context(), r)
	if _, err := s.lookup(ctx, agent); err != nil {
		writeRESTError(w, err)
		return
	}
	reply := func(v any, err error) {
		if err != nil {
			writeRESTError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	}
	switch route {
	case "send", "stream":
		if !allow(http.MethodPost) {
			return
		}
		var p SendMessageRequest
		if err := s.decodeBody(r, &p); err != nil {
			reply(nil, err)
			return
		}
		if route == "send" {
			t, err := s.sendMessage(ctx, agent, p)
			reply(SendMessageResponse{Task: &t}, err)
			return
		}
		s.stream(w, nil, func(emit emitFunc) error { return s.streamMessage(ctx, agent, p, emit) })
	case "list":
		if !allow(http.MethodGet) {
			return
		}
		p, err := listQuery(r)
		if err != nil {
			reply(nil, err)
			return
		}
		reply(s.listTasks(ctx, agent, p))
	case "get":
		if !allow(http.MethodGet) {
			return
		}
		p := GetTaskRequest{ID: id}
		if v := r.URL.Query().Get("historyLength"); v != "" {
			n, err := queryInt(v, "historyLength")
			if err != nil {
				reply(nil, err)
				return
			}
			p.HistoryLength = &n
		}
		reply(s.getTask(ctx, agent, p))
	case "cancel":
		if !allow(http.MethodPost) {
			return
		}
		reply(s.cancelTask(ctx, agent, TaskIDRequest{ID: id}))
	case "subscribe":
		// The spec's text says POST, its proto GET: both are served.
		if !allow(http.MethodPost, http.MethodGet) {
			return
		}
		s.stream(w, nil, func(emit emitFunc) error { return s.subscribe(ctx, agent, TaskIDRequest{ID: id}, emit) })
	case "push":
		reply(nil, ErrPushNotSupported)
	case "extendedCard":
		reply(nil, ErrUnsupported.with("this agent has no extended agent card"))
	}
}

// restRoute names the endpoint of a path under /a2a/{agent}, and the task
// ID in it.
func restRoute(sub string) (route, id string) {
	switch sub {
	case "/message:send":
		return "send", ""
	case "/message:stream":
		return "stream", ""
	case "/tasks", "/tasks/":
		return "list", ""
	case "/extendedAgentCard":
		return "extendedCard", ""
	}
	rest, ok := strings.CutPrefix(sub, "/tasks/")
	if !ok || rest == "" {
		return "", ""
	}
	if task, _, ok := strings.Cut(rest, "/"); ok {
		if strings.HasPrefix(rest[len(task):], "/pushNotificationConfigs") {
			return "push", task
		}
		return "", ""
	}
	if task, ok := strings.CutSuffix(rest, ":cancel"); ok {
		return "cancel", task
	}
	if task, ok := strings.CutSuffix(rest, ":subscribe"); ok {
		return "subscribe", task
	}
	return "get", rest
}

func (s *Server) decodeBody(r *http.Request, v any) error {
	b, err := s.readBody(r)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return ErrInvalidParams.with("the body: " + jsonProblem(err))
	}
	return nil
}

func queryInt(v, name string) (int32, error) {
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil {
		return 0, ErrInvalidParams.with(name + " must be an integer")
	}
	return int32(n), nil
}

func listQuery(r *http.Request) (ListTasksRequest, error) {
	q := r.URL.Query()
	p := ListTasksRequest{ContextID: q.Get("contextId"), Status: q.Get("status"), PageToken: q.Get("pageToken")}
	for _, f := range []struct {
		name string
		dst  **int32
	}{{"pageSize", &p.PageSize}, {"historyLength", &p.HistoryLength}} {
		if v := q.Get(f.name); v != "" {
			n, err := queryInt(v, f.name)
			if err != nil {
				return p, err
			}
			*f.dst = &n
		}
	}
	if v := q.Get("statusTimestampAfter"); v != "" {
		t, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			return p, ErrInvalidParams.with("statusTimestampAfter must be an RFC 3339 time")
		}
		ts := Timestamp(t)
		p.StatusTimestampAfter = &ts
	}
	if v := q.Get("includeArtifacts"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return p, ErrInvalidParams.with("includeArtifacts must be true or false")
		}
		p.IncludeArtifacts = &b
	}
	return p, nil
}

type restStatus struct {
	Code    int         `json:"code"`
	Status  string      `json:"status"`
	Message string      `json:"message"`
	Details []errorInfo `json:"details,omitempty"`
}

// writeRESTError answers an HTTP+JSON request with err (AIP-193).
func writeRESTError(w http.ResponseWriter, err error) {
	var he *httpError
	if errors.As(err, &he) {
		setChallenge(w, he)
		writeJSON(w, he.status, map[string]restStatus{"error": {Code: he.status, Status: statusName(he.status), Message: he.msg}})
		return
	}
	e := a2aError(err)
	setRetryAfter(w, e)
	status := e.Status
	if e.RetryAfter > 0 {
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, map[string]restStatus{"error": restStatusOf(e, status)})
}

func restStatusOf(e *Error, status int) restStatus {
	return restStatus{Code: status, Status: statusName(status), Message: e.Message, Details: e.details()}
}

// Streams (both bindings): Server-Sent Events whose data is a
// StreamResponse, wrapped in a JSON-RPC response for JSON-RPC.

type sseOut struct {
	w   http.ResponseWriter
	rc  *http.ResponseController
	id  json.RawMessage // the JSON-RPC request's; nil for HTTP+JSON
	mu  sync.Mutex
	err error
}

// stream runs a streaming operation. An error before its first item is an
// ordinary error response; one after it is the stream's last event.
func (s *Server) stream(w http.ResponseWriter, id json.RawMessage, run func(emitFunc) error) {
	o := &sseOut{w: w, rc: http.NewResponseController(w), id: id}
	opened := false
	var stopKeepAlive func()
	emit := func(sr StreamResponse) error {
		if !opened {
			opened = true
			o.open()
			stopKeepAlive = s.keepAlive(o)
		}
		var v any = sr
		if id != nil {
			v = rpcResponse{JSONRPC: "2.0", ID: id, Result: sr}
		}
		return o.send(v)
	}
	err := run(emit)
	if stopKeepAlive != nil {
		stopKeepAlive()
	}
	switch {
	case err == nil:
	case !opened && id != nil:
		writeRPCError(w, id, err)
	case !opened:
		writeRESTError(w, err)
	case errors.Is(err, errClientGone) || errors.Is(err, context.Canceled):
	default:
		e := a2aError(err)
		var v any = map[string]restStatus{"error": restStatusOf(e, e.Status)}
		if id != nil {
			v = rpcResponse{JSONRPC: "2.0", ID: id, Error: rpcErrorOf(e)}
		}
		_ = o.send(v)
	}
	o.mu.Lock()
	_ = o.rc.SetWriteDeadline(time.Time{})
	o.mu.Unlock()
}

// errClientGone: a write to the stream's client failed.
var errClientGone = errors.New("the stream's client went away")

func (o *sseOut) open() {
	h := o.w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	o.w.WriteHeader(http.StatusOK)
	_ = o.rc.Flush()
}

func (o *sseOut) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return o.write(fmt.Sprintf("data: %s\n\n", b))
}

func (o *sseOut) write(msg string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return o.err
	}
	if err := o.rc.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		o.err = errClientGone
		return o.err
	}
	if _, err := o.w.Write([]byte(msg)); err != nil {
		o.err = errClientGone
		return o.err
	}
	if err := o.rc.Flush(); err != nil {
		o.err = errClientGone
	}
	return o.err
}

// keepAlive writes an SSE comment to o on every tick until the returned
// function is called, which returns once nothing more will be written.
func (s *Server) keepAlive(o *sseOut) func() {
	if s.cfg.keepAlive == 0 {
		return func() {}
	}
	t := s.cfg.clock.NewTicker(s.cfg.keepAlive)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-t.C():
				_ = o.write(": keep-alive\n\n")
			case <-stop:
				return
			}
		}
	}()
	return func() {
		t.Stop()
		close(stop)
		<-done
	}
}
