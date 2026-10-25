package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/llm"
)

// Error is an error the API reported: an error response, or an error event
// inside the stream. Generate's errors wrap it with the llm sentinel it maps
// to; errors.As reaches it.
type Error struct {
	// StatusCode is the HTTP status of an error response, or the code of an
	// error inside the stream.
	StatusCode int
	// Status is Google's status name ("RESOURCE_EXHAUSTED",
	// "INVALID_ARGUMENT"), and Message its message.
	Status, Message string
	// Reason is the reason of the error's ErrorInfo detail ("API_KEY_INVALID"),
	// if it has one.
	Reason string
	// Delay is the error's RetryInfo delay, if it has one: how long the API
	// asks to wait before trying again.
	Delay time.Duration
	// InStream is true for an error sent inside the stream.
	InStream bool
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("gemini: ")
	if e.InStream {
		b.WriteString("error in stream")
	} else {
		fmt.Fprintf(&b, "HTTP %d", e.StatusCode)
	}
	if e.Status != "" || e.Reason != "" {
		fmt.Fprintf(&b, " (%s)", strings.Trim(e.Status+" "+e.Reason, " "))
	}
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	return b.String()
}

// RetryDelay is the RetryInfo delay, for the retries before the stream.
func (e *Error) RetryDelay() time.Duration { return e.Delay }

// errorBody is the body of an error, {"error":{...}}.
type errorBody struct {
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Status  string `json:"status"`
		Details []struct {
			Type       string `json:"@type"`
			Reason     string `json:"reason"`
			RetryDelay string `json:"retryDelay"`
		} `json:"details"`
	} `json:"error"`
}

// read fills e from an error body; what it cannot read stays empty. It
// returns the body's code.
func (e *Error) read(data []byte) (code int) {
	var body errorBody
	if json.Unmarshal(data, &body) != nil || body.Error == nil {
		return 0
	}
	f := body.Error
	e.Status, e.Message = f.Status, f.Message
	for _, d := range f.Details {
		switch {
		case strings.HasSuffix(d.Type, "google.rpc.ErrorInfo"):
			e.Reason = d.Reason
		case strings.HasSuffix(d.Type, "google.rpc.RetryInfo"):
			if delay, err := time.ParseDuration(d.RetryDelay); err == nil && delay > 0 {
				e.Delay = delay
			}
		}
	}
	return f.Code
}

// responseError reads an error response.
func responseError(resp *http.Response, body []byte) *Error {
	e := &Error{StatusCode: resp.StatusCode}
	e.read(body)
	return e
}

// streamError reads an error sent inside the stream; its code is the
// status.
func streamError(data []byte) *Error {
	e := &Error{InStream: true}
	e.StatusCode = e.read(data)
	return e
}

// classify wraps err with the llm sentinel it maps to. A done context wins:
// the caller gets the context's own error.
func classify(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		// A stalled stream is cancelled by Generate's own watchdog, with
		// errStalled as the cause: transient, like a dropped connection.
		if cause := context.Cause(ctx); errors.Is(cause, errStalled) {
			return fmt.Errorf("%w: %w", llm.ErrOverloaded, cause)
		}
		return ctxErr
	}
	var apiErr *Error
	if errors.As(err, &apiErr) {
		return fmt.Errorf("%w: %w", sentinel(apiErr), err)
	}
	// A dropped connection, a stream cut short or garbled: transient.
	return fmt.Errorf("%w: %w", llm.ErrOverloaded, err)
}

// sentinel maps an API error by its reason and status name, which say more
// than the HTTP status (a 400 for a bad key, or for a prompt too long), then
// by the HTTP status.
func sentinel(e *Error) error {
	switch e.Reason {
	case "API_KEY_INVALID", "API_KEY_EXPIRED", "API_KEY_SERVICE_BLOCKED":
		return llm.ErrAuth
	}
	switch e.Status {
	case "RESOURCE_EXHAUSTED":
		return llm.ErrRateLimited
	case "UNAUTHENTICATED", "PERMISSION_DENIED":
		return llm.ErrAuth
	case "UNAVAILABLE", "INTERNAL", "DEADLINE_EXCEEDED", "ABORTED", "UNKNOWN":
		return llm.ErrOverloaded
	case "INVALID_ARGUMENT", "FAILED_PRECONDITION", "NOT_FOUND", "OUT_OF_RANGE", "UNIMPLEMENTED":
		if contextTooLong(e.Message) {
			return llm.ErrContextTooLong
		}
		return llm.ErrInvalidRequest
	}
	switch s := e.StatusCode; {
	case s == http.StatusTooManyRequests:
		return llm.ErrRateLimited
	case s == http.StatusUnauthorized, s == http.StatusForbidden:
		return llm.ErrAuth
	case s == http.StatusRequestEntityTooLarge:
		return llm.ErrContextTooLong
	case s >= 400 && s < 500 && s != http.StatusRequestTimeout && s != http.StatusConflict:
		if contextTooLong(e.Message) {
			return llm.ErrContextTooLong
		}
		return llm.ErrInvalidRequest
	default: // 408, 409, 5xx, an in-stream error without a code, anything unexpected
		return llm.ErrOverloaded
	}
}

// contextTooLong reports whether an error message says the prompt does not
// fit the context window ("The input token count (1200000) exceeds the
// maximum number of tokens allowed (1048576).").
func contextTooLong(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "exceeds the maximum number of tokens") || strings.Contains(msg, "input token count") ||
		strings.Contains(msg, "context window") || strings.Contains(msg, "too many tokens")
}
