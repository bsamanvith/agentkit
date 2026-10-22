package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/bsamanvith/agentkit/pkg/a2a"
	"github.com/bsamanvith/agentkit/pkg/clock"
)

// Defaults of [Config].
const (
	DefaultMaxBodyBytes = 8 << 20
	DefaultPollInterval = 2 * time.Second
)

// Config is how a [Client] reaches its agent.
type Config struct {
	// Header is sent with every request: the agent's credential, such as
	// "Authorization: Bearer ...". It goes to the card's origin only.
	Header http.Header
	// HTTPClient sends the requests (default http.DefaultClient). Its
	// redirects are never followed.
	HTTPClient *http.Client
	// MaxBodyBytes caps a response and each event of a stream (default
	// DefaultMaxBodyBytes).
	MaxBodyBytes int64
	// Clock times the polls of Wait (default clock.Real).
	Clock clock.Clock
	// PollInterval is how long Wait waits between two polls of a task, or
	// before it subscribes again (default DefaultPollInterval).
	PollInterval time.Duration
	// NewID makes message IDs (default: 16 random bytes in hex).
	NewID func() string
}

// Client calls one remote agent, on the interface of its card that it
// picked. It is safe for concurrent use.
type Client struct {
	card  a2a.AgentCard
	iface iface
	t     *transport
	clk   clock.Clock
	poll  time.Duration
	newID func() string
	rpcID atomic.Int64
}

// Connect reads the card at cardURL ([FetchCard]) and returns a client of
// its agent.
func Connect(ctx context.Context, cardURL string, cfg Config) (*Client, error) {
	card, err := FetchCard(ctx, cardURL, cfg)
	if err != nil {
		return nil, err
	}
	return New(card, cardURL, cfg)
}

// New returns a client of the agent of card, read from cardURL. It fails
// with [ErrInvalidCard] when [ValidateCard] does.
func New(card *a2a.AgentCard, cardURL string, cfg Config) (*Client, error) {
	if err := ValidateCard(card, cardURL); err != nil {
		return nil, err
	}
	origin, _ := checkURL(cardURL)
	in, _ := pickInterface(card, origin)
	c := &Client{card: *card, iface: in, t: newTransport(cfg), clk: cfg.Clock, poll: cfg.PollInterval, newID: cfg.NewID}
	if c.clk == nil {
		c.clk = clock.Real()
	}
	if c.poll <= 0 {
		c.poll = DefaultPollInterval
	}
	if c.newID == nil {
		c.newID = randomID
	}
	return c, nil
}

func randomID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Card is the agent's card.
func (c *Client) Card() a2a.AgentCard { return c.card }

// Binding is the protocol binding the client uses.
func (c *Client) Binding() string { return c.iface.binding }

// UserText is a user's message of one text part, without an ID: the client
// gives a message without one a new ID.
func UserText(text string) a2a.Message {
	return a2a.Message{Role: a2a.RoleUser, Parts: []a2a.Part{a2a.TextPart(text)}}
}

func (c *Client) prepare(req *a2a.SendMessageRequest) error {
	if req.Message == nil {
		return fmt.Errorf("%w: no message", a2a.ErrInvalidParams)
	}
	m := *req.Message
	if m.MessageID == "" {
		m.MessageID = c.newID()
	}
	if m.Role == "" {
		m.Role = a2a.RoleUser
	}
	req.Message = &m
	return nil
}

// SendMessage sends a message (SendMessage). The agent answers with a task
// or, for a quick reply, a message.
func (c *Client) SendMessage(ctx context.Context, req a2a.SendMessageRequest) (a2a.SendMessageResponse, error) {
	var out a2a.SendMessageResponse
	if err := c.prepare(&req); err != nil {
		return out, err
	}
	if err := c.call(ctx, "SendMessage", http.MethodPost, "/message:send", req, &out); err != nil {
		return out, err
	}
	if out.Task == nil && out.Message == nil {
		return out, fmt.Errorf("%w: SendMessage answered neither a task nor a message", ErrProtocol)
	}
	return out, nil
}

// SendStreamingMessage sends a message (SendStreamingMessage) and yields
// the stream's items: the task, its updates and its last status. The stream
// ends when the agent closes it; an error ends it too.
func (c *Client) SendStreamingMessage(ctx context.Context, req a2a.SendMessageRequest) iter.Seq2[a2a.StreamResponse, error] {
	if err := c.prepare(&req); err != nil {
		return func(yield func(a2a.StreamResponse, error) bool) { yield(a2a.StreamResponse{}, err) }
	}
	return c.stream(ctx, "SendStreamingMessage", "/message:stream", req)
}

// GetTask reads a task (GetTask).
func (c *Client) GetTask(ctx context.Context, id string) (a2a.Task, error) {
	var t a2a.Task
	err := c.call(ctx, "GetTask", http.MethodGet, "/tasks/"+url.PathEscape(id), a2a.GetTaskRequest{ID: id}, &t)
	if err == nil && t.ID == "" {
		err = fmt.Errorf("%w: GetTask answered a task without an id", ErrProtocol)
	}
	return t, err
}

// CancelTask asks the agent to cancel a task (CancelTask) and returns it
// as the agent then has it.
func (c *Client) CancelTask(ctx context.Context, id string) (a2a.Task, error) {
	var t a2a.Task
	err := c.call(ctx, "CancelTask", http.MethodPost, "/tasks/"+url.PathEscape(id)+":cancel", a2a.TaskIDRequest{ID: id}, &t)
	return t, err
}

// SubscribeToTask follows a task that is not terminal (SubscribeToTask):
// the task, then its updates until its last status.
func (c *Client) SubscribeToTask(ctx context.Context, id string) iter.Seq2[a2a.StreamResponse, error] {
	return c.stream(ctx, "SubscribeToTask", "/tasks/"+url.PathEscape(id)+":subscribe", a2a.TaskIDRequest{ID: id})
}

// JSON-RPC envelopes.

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// errorInfo is the google.rpc.ErrorInfo in an error's details.
type errorInfo struct {
	Reason string `json:"reason"`
}

// remoteError is the A2A error an agent answered: its code (from the
// reason on HTTP+JSON), so that it matches the sentinel of package a2a.
func (c *Client) remoteError(code int, reason, msg string, status int) error {
	if code == 0 {
		code = codeOf(reason)
	}
	return &a2a.Error{Code: code, Reason: reason, Status: status, Message: cut(c.t.redactString(msg), maxMessageRunes)}
}

var sentinels = []*a2a.Error{
	a2a.ErrTaskNotFound, a2a.ErrTaskNotCancelable, a2a.ErrPushNotSupported, a2a.ErrUnsupported,
	a2a.ErrContentType, a2a.ErrExtendedCard, a2a.ErrVersionUnsupported,
}

func codeOf(reason string) int {
	for _, s := range sentinels {
		if s.Reason == reason {
			return s.Code
		}
	}
	return a2a.ErrInternal.Code
}

func (c *Client) newRequest(ctx context.Context, method, path string, body any) (*http.Request, error) {
	u := c.iface.url
	var b []byte
	if c.iface.binding == a2a.BindingJSONRPC {
		method = http.MethodPost
	} else {
		u += path
	}
	if body != nil && method == http.MethodPost {
		var err error
		if b, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, u, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	if b != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// call makes a unary call: the JSON-RPC method, or the HTTP+JSON endpoint,
// and decodes its result into out.
func (c *Client) call(ctx context.Context, method, httpMethod, path string, params, out any) error {
	body := params
	if c.iface.binding == a2a.BindingJSONRPC {
		body = rpcRequest{JSONRPC: "2.0", ID: c.rpcID.Add(1), Method: method, Params: params}
	} else if httpMethod == http.MethodPost && method == "CancelTask" {
		body = struct{}{}
	}
	req, err := c.newRequest(ctx, httpMethod, path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.t.send(req, c.decodeHTTPError)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := c.t.read(resp, c.t.maxBody)
	if err != nil {
		return err
	}
	if c.iface.binding == a2a.BindingJSONRPC {
		res, err := c.rpcResult(b)
		if err != nil {
			return err
		}
		b = res
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("%w: %s's result: %s", ErrProtocol, method, jsonProblem(err))
	}
	return nil
}

// rpcResult is the result of a JSON-RPC response, or its error.
func (c *Client) rpcResult(b []byte) (json.RawMessage, error) {
	var r rpcResponse
	if err := json.Unmarshal(b, &r); err != nil || r.JSONRPC != "2.0" {
		return nil, fmt.Errorf("%w: not a JSON-RPC 2.0 response", ErrProtocol)
	}
	if r.Error != nil {
		var details []errorInfo
		_ = json.Unmarshal(r.Error.Data, &details)
		reason := ""
		if len(details) > 0 {
			reason = details[0].Reason
		}
		return nil, c.remoteError(r.Error.Code, reason, r.Error.Message, 0)
	}
	if len(r.Result) == 0 || string(r.Result) == "null" {
		return nil, fmt.Errorf("%w: a JSON-RPC response with neither a result nor an error", ErrProtocol)
	}
	return r.Result, nil
}

// decodeHTTPError reads an error response: an A2A error in either
// binding's form, or nil for a plain HTTP failure. A refused credential is
// always a StatusError.
func (c *Client) decodeHTTPError(status int, body []byte) error {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return nil
	}
	var rest struct {
		Error *struct {
			Code    int         `json:"code"`
			Message string      `json:"message"`
			Details []errorInfo `json:"details"`
		} `json:"error"`
	}
	if c.iface.binding == a2a.BindingHTTPJSON && json.Unmarshal(body, &rest) == nil && rest.Error != nil {
		for _, d := range rest.Error.Details {
			if d.Reason != "" {
				return c.remoteError(0, d.Reason, rest.Error.Message, status)
			}
		}
	}
	return nil
}

// stream makes a streaming call and yields its items.
func (c *Client) stream(ctx context.Context, method, path string, params any) iter.Seq2[a2a.StreamResponse, error] {
	return func(yield func(a2a.StreamResponse, error) bool) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		body := params
		if c.iface.binding == a2a.BindingJSONRPC {
			body = rpcRequest{JSONRPC: "2.0", ID: c.rpcID.Add(1), Method: method, Params: params}
		}
		req, err := c.newRequest(ctx, http.MethodPost, path, body)
		if err != nil {
			yield(a2a.StreamResponse{}, err)
			return
		}
		req.Header.Set("Accept", "text/event-stream")
		resp, err := c.t.send(req, c.decodeHTTPError)
		if err != nil {
			yield(a2a.StreamResponse{}, err)
			return
		}
		defer resp.Body.Close()
		if isJSON(resp.Header.Get("Content-Type")) {
			// An error before the stream opened, as a plain response.
			b, err := c.t.read(resp, c.t.maxBody)
			if err == nil {
				err = c.unaryStreamError(b)
			}
			yield(a2a.StreamResponse{}, err)
			return
		}
		if ct := resp.Header.Get("Content-Type"); !isEventStream(ct) {
			yield(a2a.StreamResponse{}, fmt.Errorf("%w: %s answered %q, not an event stream", ErrProtocol, method, ct))
			return
		}
		for data, err := range readEvents(resp.Body, c.t.maxBody) {
			if err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					err = ctxErr
				}
				yield(a2a.StreamResponse{}, err)
				return
			}
			data = c.t.redact(data)
			if c.iface.binding == a2a.BindingJSONRPC {
				if data, err = c.rpcResult(data); err != nil {
					yield(a2a.StreamResponse{}, err)
					return
				}
			} else if err := c.restStreamError(data); err != nil {
				yield(a2a.StreamResponse{}, err)
				return
			}
			var sr a2a.StreamResponse
			if err := json.Unmarshal(data, &sr); err != nil {
				yield(a2a.StreamResponse{}, fmt.Errorf("%w: %s: an event is %s", ErrProtocol, method, jsonProblem(err)))
				return
			}
			if n := count(sr); n != 1 {
				yield(a2a.StreamResponse{}, fmt.Errorf("%w: %s: an event has %d of task, message, statusUpdate and artifactUpdate, not one", ErrProtocol, method, n))
				return
			}
			if !yield(sr, nil) {
				return
			}
		}
	}
}

func count(sr a2a.StreamResponse) int {
	n := 0
	for _, set := range []bool{sr.Task != nil, sr.Message != nil, sr.StatusUpdate != nil, sr.ArtifactUpdate != nil} {
		if set {
			n++
		}
	}
	return n
}

// unaryStreamError is the error of a streaming call answered with JSON.
func (c *Client) unaryStreamError(b []byte) error {
	if c.iface.binding == a2a.BindingJSONRPC {
		if _, err := c.rpcResult(b); err != nil {
			return err
		}
	} else if err := c.restStreamError(b); err != nil {
		return err
	}
	return fmt.Errorf("%w: a stream answered with a JSON body", ErrProtocol)
}

// restStreamError is the error an HTTP+JSON stream ends with, if data is
// one ({"error": {...}}).
func (c *Client) restStreamError(data []byte) error {
	var v struct {
		Error *struct {
			Code    int         `json:"code"`
			Message string      `json:"message"`
			Details []errorInfo `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &v) != nil || v.Error == nil {
		return nil
	}
	reason := ""
	for _, d := range v.Error.Details {
		if d.Reason != "" {
			reason = d.Reason
			break
		}
	}
	if reason == "" {
		return &StatusError{StatusCode: v.Error.Code, Message: cut(v.Error.Message, maxMessageRunes)}
	}
	return c.remoteError(0, reason, v.Error.Message, v.Error.Code)
}

// Start sends msg with returnImmediately, so the agent answers with the task
// as soon as it has one, and returns that task. An agent that answers with a
// message instead (a quick reply, no task) gives a completed task without an
// ID whose status message is the reply.
func (c *Client) Start(ctx context.Context, msg a2a.Message) (a2a.Task, error) {
	return c.start(ctx, a2a.SendMessageRequest{Message: &msg, Configuration: &a2a.SendMessageConfiguration{ReturnImmediately: true}})
}

func (c *Client) start(ctx context.Context, req a2a.SendMessageRequest) (a2a.Task, error) {
	res, err := c.SendMessage(ctx, req)
	if err != nil {
		return a2a.Task{}, err
	}
	if res.Task != nil {
		if res.Task.ID == "" {
			return a2a.Task{}, fmt.Errorf("%w: SendMessage answered a task without an id", ErrProtocol)
		}
		return *res.Task, nil
	}
	m := res.Message
	return a2a.Task{ContextID: m.ContextID, Status: a2a.TaskStatus{State: a2a.StateCompleted, Message: m}}, nil
}

// Settled reports whether a task in state s will not move without the
// caller: it is terminal, or it waits for input or authentication.
func Settled(s string) bool {
	return Terminal(s) || s == a2a.StateInputRequired || s == a2a.StateAuthRequired
}

// Terminal reports whether a task in state s can change no more.
func Terminal(s string) bool {
	switch s {
	case a2a.StateCompleted, a2a.StateFailed, a2a.StateCanceled, a2a.StateRejected:
		return true
	}
	return false
}

// Wait follows the task id until it is [Settled] and returns it as GetTask
// then has it, artifacts included. It subscribes to the task when the card
// says the agent streams, and polls it every PollInterval otherwise (or when
// a subscription ends before the task does). It returns ctx's error when ctx
// ends first.
func (c *Client) Wait(ctx context.Context, id string) (a2a.Task, error) {
	subscribe := c.card.Capabilities.Streaming
	for {
		t, err := c.GetTask(ctx, id)
		if err != nil {
			return t, err
		}
		if Settled(t.Status.State) {
			return t, nil
		}
		if subscribe {
			settled, err := c.untilSettled(ctx, id)
			switch {
			case ctx.Err() != nil:
				return a2a.Task{}, ctx.Err()
			case errors.Is(err, a2a.ErrUnsupported):
				continue // it settled between GetTask and the subscription
			case err != nil:
				subscribe = false // poll from now on
			case settled:
				continue
			}
		}
		timer := c.clk.NewTimer(c.poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return a2a.Task{}, ctx.Err()
		case <-timer.C():
		}
	}
}

// untilSettled reads a subscription until a status says the task settled
// (true), or the stream ends without one (false).
func (c *Client) untilSettled(ctx context.Context, id string) (bool, error) {
	for sr, err := range c.SubscribeToTask(ctx, id) {
		if err != nil {
			return false, err
		}
		switch {
		case sr.StatusUpdate != nil && Settled(sr.StatusUpdate.Status.State),
			sr.Task != nil && Settled(sr.Task.Status.State):
			return true, nil
		}
	}
	return false, nil
}

// Continue sends msg to the task id, which waits for input, and returns the
// task as the agent then has it.
func (c *Client) Continue(ctx context.Context, id string, msg a2a.Message) (a2a.Task, error) {
	msg.TaskID = id
	return c.start(ctx, a2a.SendMessageRequest{Message: &msg, Configuration: &a2a.SendMessageConfiguration{ReturnImmediately: true}})
}
