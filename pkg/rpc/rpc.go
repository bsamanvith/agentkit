package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
)

// PathPrefix is where a server's methods are served: a method "svc/Name" is
// at PathPrefix + "svc/Name". A component serving service svc mounts its
// [Server] at PathPrefix + "svc/".
const PathPrefix = "/rpc/"

// Conn carries calls to a server: [Dial] over HTTP, [Pipe] in process.
//
// Call sends req (JSON) to the named method and returns the JSON of each
// message it answers, in order. The sequence ends after the last message, or
// with one error: an [*Error] the server sent (its codes not yet matched to
// errors; the method's Call does that), the context's error, or a transport
// failure matching [ErrUnavailable]. Breaking out of the loop cancels the
// call. Implementations must be safe for concurrent use.
type Conn interface {
	Call(ctx context.Context, method string, req json.RawMessage) iter.Seq2[json.RawMessage, error]
}

// Unary is a method that answers one Resp to each Req.
type Unary[Req, Resp any] struct {
	// Name is "service/Method"; each part matches ^[A-Za-z0-9_.-]+$.
	Name string
	// Errors are the errors the method's handler may return that the caller
	// can tell apart (errors.Is) on the other side.
	Errors []Code
}

// Handle serves the method on s with f. It panics if Name is invalid or
// already served by s: methods are declared in code, so either is a bug.
func (m Unary[Req, Resp]) Handle(s *Server, f func(ctx context.Context, req Req) (Resp, error)) {
	s.handle(m.Name, m.Errors, func(ctx context.Context, raw json.RawMessage, send func(any) error) error {
		var req Req
		if err := decode(raw, &req); err != nil {
			return err
		}
		resp, err := f(ctx, req)
		if err != nil {
			return err
		}
		return send(resp)
	})
}

// Call calls the method through c.
func (m Unary[Req, Resp]) Call(ctx context.Context, c Conn, req Req) (Resp, error) {
	var resp Resp
	raw, err := json.Marshal(req)
	if err != nil {
		return resp, fmt.Errorf("rpc %s: request: %w", m.Name, err)
	}
	got := false
	for data, err := range c.Call(ctx, m.Name, raw) {
		if err != nil {
			return resp, callError(m.Name, m.Errors, err)
		}
		if got {
			return resp, fmt.Errorf("rpc %s: %w: more than one response", m.Name, ErrUnavailable)
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			return resp, fmt.Errorf("rpc %s: %w: response: %w", m.Name, ErrUnavailable, err)
		}
		got = true
	}
	if !got {
		return resp, fmt.Errorf("rpc %s: %w: no response", m.Name, ErrUnavailable)
	}
	return resp, nil
}

// Stream is a method that answers each Req with a sequence of Msg.
type Stream[Req, Msg any] struct {
	// Name and Errors are as in [Unary].
	Name   string
	Errors []Code
}

// Handle serves the method on s with f: each message f yields is sent as it
// comes; an error ends the call with that error. The handler's context is
// done when the caller stops listening.
func (m Stream[Req, Msg]) Handle(s *Server, f func(ctx context.Context, req Req) iter.Seq2[Msg, error]) {
	s.handle(m.Name, m.Errors, func(ctx context.Context, raw json.RawMessage, send func(any) error) error {
		var req Req
		if err := decode(raw, &req); err != nil {
			return err
		}
		for msg, err := range f(ctx, req) {
			if err != nil {
				return err
			}
			if err := send(msg); err != nil {
				return err
			}
		}
		return nil
	})
}

// Call calls the method through c and returns its messages as they come.
// The sequence ends after the last message, or with an error.
func (m Stream[Req, Msg]) Call(ctx context.Context, c Conn, req Req) iter.Seq2[Msg, error] {
	return func(yield func(Msg, error) bool) {
		var zero Msg
		raw, err := json.Marshal(req)
		if err != nil {
			yield(zero, fmt.Errorf("rpc %s: request: %w", m.Name, err))
			return
		}
		for data, err := range c.Call(ctx, m.Name, raw) {
			if err != nil {
				yield(zero, callError(m.Name, m.Errors, err))
				return
			}
			var msg Msg
			if err := json.Unmarshal(data, &msg); err != nil {
				yield(zero, fmt.Errorf("rpc %s: %w: message: %w", m.Name, ErrUnavailable, err))
				return
			}
			if !yield(msg, nil) {
				return
			}
		}
	}
}

// decode decodes a request. Unknown fields are ignored: a newer caller may
// send more.
func decode(raw json.RawMessage, v any) error {
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("%w: %w", ErrBadRequest, err)
	}
	return nil
}
