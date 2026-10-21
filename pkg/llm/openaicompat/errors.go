package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/ssestream"

	"github.com/bsamanvith/agentkit/pkg/llm"
)

// classify wraps err with the llm sentinel it maps to. A done context wins:
// the caller gets the context's own error. status is the HTTP status of the
// last response, or zero if unknown.
func classify(ctx context.Context, err error, status int) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		// A stalled stream is cancelled by Generate's own watchdog, with
		// errStalled as the cause: transient, like a dropped connection.
		if cause := context.Cause(ctx); errors.Is(cause, errStalled) {
			return fmt.Errorf("%w: %w", llm.ErrOverloaded, cause)
		}
		return ctxErr
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		msg := apiErr.Message
		if msg == "" {
			msg = topLevelMessage(apiErr.Response)
		}
		return fmt.Errorf("%w: %w", sentinel(apiErr.Code, apiErr.Type, msg, apiErr.StatusCode), err)
	}
	var streamErr *ssestream.StreamError
	if errors.As(err, &streamErr) {
		code, typ, msg, st := streamError(streamErr.Event.Data)
		return fmt.Errorf("%w: %w", sentinel(code, typ, msg, st), err)
	}
	if status >= 400 {
		// An error response the SDK could not read as an OpenAI error,
		// such as vLLM's {"object":"error",...}: its status says enough.
		return fmt.Errorf("%w: HTTP %d: %w", sentinel("", "", "", status), status, err)
	}
	var urlErr *url.Error
	if status == 0 && !errors.As(err, &urlErr) && !errors.Is(err, errGarbled) && !errors.Is(err, errIncomplete) {
		// No response, and not for a transport failure (the HTTP client
		// reports those as *url.Error): the SDK refused to send the
		// request, for example an API key over plain HTTP to a host that
		// is not loopback. The same request fails the same way again.
		return fmt.Errorf("%w: request not sent: %w", llm.ErrInvalidRequest, err)
	}
	// A dropped connection, a stream cut short or garbled: transient.
	return fmt.Errorf("%w: %w", llm.ErrOverloaded, err)
}

// topLevelMessage reads the message of an error body that is not nested
// under "error", as vLLM writes it: {"object":"error","message":...}.
func topLevelMessage(resp *http.Response) string {
	if resp == nil || resp.Body == nil {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return ""
	}
	var body struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(data, &body)
	return body.Message
}

// streamError reads an error sent inside the stream, {"error":{...}}. Its
// code is a string (OpenAI) or the HTTP status (vLLM).
func streamError(data []byte) (code, typ, msg string, status int) {
	var body struct {
		Error struct {
			Code    json.RawMessage `json:"code"`
			Type    string          `json:"type"`
			Message string          `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(data, &body) != nil {
		return "", "", "", 0
	}
	e := body.Error
	if json.Unmarshal(e.Code, &code) != nil {
		_ = json.Unmarshal(e.Code, &status)
	}
	return code, e.Type, e.Message, status
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
