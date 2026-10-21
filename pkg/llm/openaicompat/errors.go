package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/llm"
)

// Error is an error the server reported: an error response, or an error
// event inside the stream. Generate's errors wrap it with the llm sentinel it
// maps to; errors.As reaches it.
type Error struct {
	// StatusCode is the HTTP status of an error response. For an error
	// inside a stream it is the numeric code vLLM puts there, or zero.
	StatusCode int
	// Code, Type and Message are the error's fields, as OpenAI and the
	// servers that imitate it write them, nested under "error" or (vLLM)
	// at the top level. Code is empty when the server gave none or gave a
	// number.
	Code, Type, Message string
	// RequestID is the response's X-Request-Id header, if any.
	RequestID string
	// InStream is true for an error sent inside the stream.
	InStream bool
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("openaicompat: ")
	if e.InStream {
		b.WriteString("error in stream")
	} else {
		fmt.Fprintf(&b, "HTTP %d", e.StatusCode)
	}
	if e.Type != "" || e.Code != "" {
		fmt.Fprintf(&b, " (%s)", strings.Trim(e.Type+" "+e.Code, " "))
	}
	if e.Message != "" {
		b.WriteString(": " + e.Message)
	}
	if e.RequestID != "" {
		b.WriteString(" [request " + e.RequestID + "]")
	}
	return b.String()
}

// errorBody is the body of an error: {"error":{...}}, {"error":"..."}
// (Ollama, some gateways) or vLLM's top-level {"object":"error",...}.
type errorBody struct {
	Error   json.RawMessage `json:"error"`
	Code    json.RawMessage `json:"code"`
	Type    string          `json:"type"`
	Message string          `json:"message"`
}

type errorFields struct {
	Code    json.RawMessage `json:"code"`
	Type    string          `json:"type"`
	Message string          `json:"message"`
}

// read fills e from an error body; what it cannot read stays empty.
// It returns the numeric code, if the error had one instead of a string.
func (e *Error) read(data []byte) (numericCode int) {
	var body errorBody
	if json.Unmarshal(data, &body) != nil {
		return 0
	}
	f := errorFields{Code: body.Code, Type: body.Type, Message: body.Message}
	var nested errorFields
	var text string
	switch {
	case len(body.Error) == 0 || string(body.Error) == "null":
	case json.Unmarshal(body.Error, &nested) == nil:
		f = nested
	case json.Unmarshal(body.Error, &text) == nil:
		f.Message = text
	}
	e.Type, e.Message = f.Type, f.Message
	if json.Unmarshal(f.Code, &e.Code) != nil {
		_ = json.Unmarshal(f.Code, &numericCode)
	}
	return numericCode
}

// responseError reads an error response.
func responseError(resp *http.Response, body []byte) *Error {
	e := &Error{StatusCode: resp.StatusCode, RequestID: resp.Header.Get("X-Request-Id")}
	e.read(body)
	return e
}

// streamError reads an error sent inside the stream, {"error":{...}}. Its
// code is a string (OpenAI) or the HTTP status (vLLM), which then counts as
// the status.
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
		return fmt.Errorf("%w: %w", sentinel(apiErr.Code, apiErr.Type, apiErr.Message, apiErr.StatusCode), err)
	}
	// A dropped connection, a stream cut short or garbled: transient.
	return fmt.Errorf("%w: %w", llm.ErrOverloaded, err)
}

// sentinel maps an API error by its code where OpenAI's codes say more than
// the status (a 429 for an exhausted quota, a 400 for a prompt too long),
// then by its HTTP status, which every server sets. An error inside a stream
// has no status (zero) and is mapped by its type.
func sentinel(code, typ, msg string, status int) error {
	switch code {
	case "context_length_exceeded", "string_above_max_length":
		return llm.ErrContextTooLong
	case "insufficient_quota", "invalid_api_key", "invalid_organization":
		return llm.ErrAuth
	case "rate_limit_exceeded":
		return llm.ErrRateLimited
	case "model_not_found":
		return llm.ErrInvalidRequest
	}
	if status == 0 {
		switch typ {
		case "rate_limit_error", "rate_limit_exceeded", "tokens", "requests":
			return llm.ErrRateLimited
		case "authentication_error", "permission_error", "insufficient_quota":
			return llm.ErrAuth
		case "invalid_request_error", "not_found_error", "BadRequestError", "NotFoundError":
			if contextTooLong(msg) {
				return llm.ErrContextTooLong
			}
			return llm.ErrInvalidRequest
		}
		return llm.ErrOverloaded // server_error, api_error, or nothing known
	}
	switch s := status; {
	case s == http.StatusTooManyRequests:
		return llm.ErrRateLimited
	case s == http.StatusUnauthorized, s == http.StatusPaymentRequired, s == http.StatusForbidden:
		return llm.ErrAuth
	case s == http.StatusRequestEntityTooLarge:
		return llm.ErrContextTooLong
	case s >= 400 && s < 500 && s != http.StatusRequestTimeout && s != http.StatusConflict:
		if contextTooLong(msg) {
			return llm.ErrContextTooLong
		}
		return llm.ErrInvalidRequest
	default: // 408, 409, 5xx and anything unexpected
		return llm.ErrOverloaded
	}
}

// contextTooLong reports whether an error message says the prompt does not
// fit the context window, for servers without context_length_exceeded
// ("This model's maximum context length is 8192 tokens", vLLM and llama.cpp
// "exceeds the available context size").
func contextTooLong(msg string) bool {
	msg = strings.ToLower(msg)
	return strings.Contains(msg, "maximum context length") || strings.Contains(msg, "context length exceeded") ||
		strings.Contains(msg, "context size") || strings.Contains(msg, "context window")
}
