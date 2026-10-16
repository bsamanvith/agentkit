package common

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
)

// maxMessageChars caps text taken from Google's error bodies.
const maxMessageChars = 300

// InputError is an error the model can fix by changing its input. A tool turns
// it into [tool.ErrInvalidInput]: use [AsToolError] in the tool's function.
type InputError struct{ msg string }

func (e *InputError) Error() string { return e.msg }

// Invalid returns an [InputError].
func Invalid(format string, args ...any) error {
	return &InputError{msg: fmt.Sprintf(format, args...)}
}

// apiError is the body Google's APIs use for errors:
//
//	{"error": {"code": 403, "message": "...", "status": "PERMISSION_DENIED",
//	           "errors": [{"reason": "rateLimitExceeded", "message": "..."}]}}
type apiError struct {
	Error struct {
		Message string `json:"message"`
		Errors  []struct {
			Reason string `json:"reason"`
		} `json:"errors"`
		Details []struct {
			Reason string `json:"reason"`
		} `json:"details"`
	} `json:"error"`
}

func parseError(body []byte) (message string, reasons []string) {
	var e apiError
	if json.Unmarshal(body, &e) != nil {
		return "", nil
	}
	for _, r := range e.Error.Errors {
		reasons = append(reasons, r.Reason)
	}
	for _, d := range e.Error.Details {
		reasons = append(reasons, d.Reason)
	}
	return e.Error.Message, reasons
}

// rateReasons are the 403 reasons that mean "slow down", not "not allowed".
var rateReasons = []string{
	"rateLimitExceeded", "userRateLimitExceeded", "RATE_LIMIT_EXCEEDED",
	"quotaExceeded", "dailyLimitExceeded", "sharingRateLimitExceeded",
}

func isRateLimited(status int, reasons []string) bool {
	if status == http.StatusTooManyRequests {
		return true
	}
	return status == http.StatusForbidden && slices.ContainsFunc(reasons, func(r string) bool { return slices.Contains(rateReasons, r) })
}

// statusError maps a non-2xx response to the error the model sees. service
// names the API in messages ("gmail").
func (c *Client) statusError(status int, body []byte, token string) error {
	msg, reasons := parseError(body)
	msg = clean(msg, token, maxMessageChars)
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == http.StatusBadRequest || status == http.StatusUnprocessableEntity:
		if msg == "" {
			msg = "no details"
		}
		return Invalid("%s rejected the request (status %d): %s", c.service, status, msg)
	case status == http.StatusUnauthorized:
		return fmt.Errorf("%s rejected the access token (status %d); the Google account must be reconnected", c.service, status)
	case isRateLimited(status, reasons):
		return fmt.Errorf("%s rate limited the request", c.service)
	case status == http.StatusForbidden:
		// Google's reason is what tells "scope not granted" from "not your
		// file", and holds nothing secret.
		return fmt.Errorf("%s denied access (status %d)%s", c.service, status, detail(msg))
	case status == http.StatusNotFound:
		return fmt.Errorf("%s: not found (status %d)%s", c.service, status, detail(msg))
	default:
		return fmt.Errorf("%s returned status %d", c.service, status)
	}
}

func detail(msg string) string {
	if msg == "" {
		return ""
	}
	return ": " + strings.TrimSpace(msg)
}
