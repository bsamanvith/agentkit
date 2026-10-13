package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"

	"github.com/bsamanvith/agentkit/pkg/llm"
)

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
	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) {
		// A dropped connection, a stream cut short or garbled: transient.
		return fmt.Errorf("%w: %w", llm.ErrOverloaded, err)
	}
	return fmt.Errorf("%w: %w", sentinel(apiErr), err)
}

// sentinel maps an API error by its type, or by its HTTP status when the type
// is unknown. Errors reported inside a stream have status 200 and a type.
func sentinel(e *anthropic.Error) error {
	switch e.Type() {
	case "rate_limit_error":
		return llm.ErrRateLimited
	case "overloaded_error", "api_error", "timeout_error":
		return llm.ErrOverloaded
	case "authentication_error", "permission_error", "billing_error":
		return llm.ErrAuth
	case "request_too_large":
		return llm.ErrContextTooLong
	case "invalid_request_error":
		if promptTooLong(e) {
			return llm.ErrContextTooLong
		}
		return llm.ErrInvalidRequest
	case "not_found_error":
		return llm.ErrInvalidRequest
	}
	switch s := e.StatusCode; {
	case s == http.StatusTooManyRequests:
		return llm.ErrRateLimited
	case s == http.StatusUnauthorized, s == http.StatusPaymentRequired, s == http.StatusForbidden:
		return llm.ErrAuth
	case s == http.StatusRequestEntityTooLarge:
		return llm.ErrContextTooLong
	case s >= 400 && s < 500 && s != http.StatusRequestTimeout && s != http.StatusConflict:
		if promptTooLong(e) {
			return llm.ErrContextTooLong
		}
		return llm.ErrInvalidRequest
	default: // 408, 409, 5xx (529 among them) and anything unexpected
		return llm.ErrOverloaded
	}
}

// promptTooLong reports whether a 400 says the prompt does not fit the
// context window. The API has no error type of its own for it, only the
// message ("prompt is too long: 1050000 tokens > 1000000 maximum").
func promptTooLong(e *anthropic.Error) bool {
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(e.RawJSON()), &body) != nil {
		return false
	}
	msg := strings.ToLower(body.Error.Message)
	return strings.Contains(msg, "prompt is too long") || strings.Contains(msg, "context window") ||
		strings.Contains(msg, "context limit")
}
