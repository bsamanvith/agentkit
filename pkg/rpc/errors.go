package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

// Errors of the transport itself, which every method knows.
var (
	// ErrUnavailable means the call did not complete: the server could not
	// be reached, answered something that is not this protocol, or the
	// stream was cut short. Whether the handler ran is unknown.
	ErrUnavailable = errors.New("rpc: unavailable")
	// ErrUnknownMethod means the server serves no method of that name.
	ErrUnknownMethod = errors.New("rpc: unknown method")
	// ErrBadRequest means the server could not read the request.
	ErrBadRequest = errors.New("rpc: bad request")
	// ErrUnauthenticated means the call's token was missing or wrong.
	ErrUnauthenticated = errors.New("rpc: unauthenticated")
	// ErrInternal means the handler panicked.
	ErrInternal = errors.New("rpc: internal error")
)

// Code names an error on the wire: an error that matches Err (errors.Is)
// crosses as Name, and the caller's error matches Err. Name must be unique
// among a method's codes.
type Code struct {
	Name string
	Err  error
}

// builtin are the codes of every method, after its own.
var builtin = []Code{
	{"canceled", context.Canceled},
	{"deadline_exceeded", context.DeadlineExceeded},
	{"rpc.unknown_method", ErrUnknownMethod},
	{"rpc.bad_request", ErrBadRequest},
	{"rpc.unauthenticated", ErrUnauthenticated},
	{"rpc.unavailable", ErrUnavailable},
	{"rpc.internal", ErrInternal},
}

// Error is an error that crossed the wire. It matches (errors.Is) the errors
// its codes name among the method's [Code]s.
type Error struct {
	// Codes name the errors the handler's error matched, in the method's
	// order. An error that matched none has no codes.
	Codes []string `json:"codes,omitempty"`
	// Message is the handler's error's text.
	Message string `json:"message"`
	// RawData is what [WithData] attached, as JSON; nil if nothing.
	RawData json.RawMessage `json:"data,omitempty"`

	errs []error
}

func (e *Error) Error() string { return e.Message }

// Unwrap returns the errors the codes name, so errors.Is matches them.
func (e *Error) Unwrap() []error { return e.errs }

// Data decodes the value attached with [WithData] into v. It returns false
// when there is none or it does not decode into v.
func (e *Error) Data(v any) bool {
	return len(e.RawData) > 0 && json.Unmarshal(e.RawData, v) == nil
}

// WithData returns err with data attached: the caller's [*Error] carries it
// as JSON. err keeps its identity on both sides.
func WithData(err error, data any) error { return &dataError{err: err, data: data} }

type dataError struct {
	err  error
	data any
}

func (e *dataError) Error() string { return e.err.Error() }
func (e *dataError) Unwrap() error { return e.err }

// encodeError is err's wire form, by the method's codes.
func encodeError(err error, codes []Code) *Error {
	e := &Error{Message: err.Error()}
	for _, c := range slices.Concat(codes, builtin) {
		if errors.Is(err, c.Err) && !slices.Contains(e.Codes, c.Name) {
			e.Codes = append(e.Codes, c.Name)
		}
	}
	if de, ok := errors.AsType[*dataError](err); ok {
		if raw, merr := json.Marshal(de.data); merr == nil {
			e.RawData = raw
		}
	}
	return e
}

// callError is the error a method's Call returns for err from its Conn: a
// wire error gets the errors its codes name.
func callError(method string, codes []Code, err error) error {
	we, ok := errors.AsType[*Error](err)
	if !ok {
		return fmt.Errorf("rpc %s: %w", method, err)
	}
	out := &Error{Codes: we.Codes, Message: we.Message, RawData: we.RawData}
	all := slices.Concat(codes, builtin)
	for _, name := range we.Codes {
		if i := slices.IndexFunc(all, func(c Code) bool { return c.Name == name }); i >= 0 {
			out.errs = append(out.errs, all[i].Err)
		}
	}
	return out
}
