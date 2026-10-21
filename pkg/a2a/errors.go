package a2a

import (
	"errors"
	"net/http"
	"time"
)

// Error is an A2A error: its JSON-RPC code, its reason (the spec's error name
// in UPPER_SNAKE_CASE without "Error", empty for JSON-RPC's own errors), the
// HTTP status of the HTTP+JSON binding, and a message for people. It matches
// the sentinel of its code with errors.Is.
type Error struct {
	Code    int
	Reason  string
	Status  int
	Message string
	// Metadata goes into the error's google.rpc.ErrorInfo.
	Metadata map[string]string
	// RetryAfter, when set, is sent as Retry-After: the server is busy.
	RetryAfter time.Duration
}

func (e *Error) Error() string { return e.Message }

// Is reports whether target is an *Error of the same code.
func (e *Error) Is(target error) bool {
	var t *Error
	return errors.As(target, &t) && t.Code == e.Code
}

// with returns a copy of e with the message and metadata.
func (e *Error) with(msg string, kv ...string) *Error {
	c := *e
	c.Message = msg
	if len(kv) > 0 {
		c.Metadata = map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			c.Metadata[kv[i]] = kv[i+1]
		}
	}
	return &c
}

// The errors of the spec (sections 5.4 and 9.5), as sentinels to match with
// errors.Is.
var (
	ErrParse              = &Error{Code: -32700, Status: http.StatusBadRequest, Message: "Invalid JSON payload"}
	ErrInvalidRequest     = &Error{Code: -32600, Status: http.StatusBadRequest, Message: "Request payload validation error"}
	ErrMethodNotFound     = &Error{Code: -32601, Status: http.StatusNotFound, Message: "Method not found"}
	ErrInvalidParams      = &Error{Code: -32602, Status: http.StatusBadRequest, Message: "Invalid parameters"}
	ErrInternal           = &Error{Code: -32603, Status: http.StatusInternalServerError, Message: "Internal error"}
	ErrTaskNotFound       = &Error{Code: -32001, Reason: "TASK_NOT_FOUND", Status: http.StatusNotFound, Message: "Task not found"}
	ErrTaskNotCancelable  = &Error{Code: -32002, Reason: "TASK_NOT_CANCELABLE", Status: http.StatusConflict, Message: "Task cannot be canceled"}
	ErrPushNotSupported   = &Error{Code: -32003, Reason: "PUSH_NOTIFICATION_NOT_SUPPORTED", Status: http.StatusBadRequest, Message: "Push Notification is not supported"}
	ErrUnsupported        = &Error{Code: -32004, Reason: "UNSUPPORTED_OPERATION", Status: http.StatusBadRequest, Message: "This operation is not supported"}
	ErrContentType        = &Error{Code: -32005, Reason: "CONTENT_TYPE_NOT_SUPPORTED", Status: http.StatusUnsupportedMediaType, Message: "Incompatible content types"}
	ErrExtendedCard       = &Error{Code: -32007, Reason: "EXTENDED_AGENT_CARD_NOT_CONFIGURED", Status: http.StatusBadRequest, Message: "Extended agent card is not configured"}
	ErrVersionUnsupported = &Error{Code: -32009, Reason: "VERSION_NOT_SUPPORTED", Status: http.StatusBadRequest, Message: "Protocol version is not supported"}
)

// errorInfo is the google.rpc.ErrorInfo every A2A error carries.
type errorInfo struct {
	Type     string            `json:"@type"`
	Reason   string            `json:"reason"`
	Domain   string            `json:"domain"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

func (e *Error) details() []errorInfo {
	if e.Reason == "" {
		return nil
	}
	return []errorInfo{{Type: "type.googleapis.com/google.rpc.ErrorInfo", Reason: e.Reason, Domain: "a2a-protocol.org", Metadata: e.Metadata}}
}

// statusName is the google.rpc.Code name of an HTTP status, for the
// HTTP+JSON binding's error body (AIP-193).
func statusName(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "INVALID_ARGUMENT"
	case http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case http.StatusForbidden:
		return "PERMISSION_DENIED"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusConflict:
		return "FAILED_PRECONDITION"
	case http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType:
		return "INVALID_ARGUMENT"
	case http.StatusServiceUnavailable:
		return "UNAVAILABLE"
	case http.StatusNotImplemented:
		return "UNIMPLEMENTED"
	}
	return "INTERNAL"
}

// httpError is a failure answered with an HTTP status in either binding:
// the agent API refused the credential (401, 403), or the agent is not
// there for the caller (404).
type httpError struct {
	status    int
	challenge string // WWW-Authenticate of a 401
	msg       string
}

func (e *httpError) Error() string { return e.msg }
