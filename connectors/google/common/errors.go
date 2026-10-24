package common

import (
	"encoding/json"
	"net/http"
	"slices"

	"github.com/bsamanvith/agentkit/connectors/internal/rest"
)

// InputError is an error the model can fix by changing its input. A tool
// made by [Func] reports it as [tool.ErrInvalidInput].
type InputError = rest.InputError

// Invalid returns an [InputError].
func Invalid(format string, args ...any) error { return rest.Invalid(format, args...) }

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

// isRateLimited says whether a 403 is a rate or quota limit (a 429 always is).
func isRateLimited(status int, reasons []string) bool {
	return status == http.StatusForbidden && slices.ContainsFunc(reasons, func(r string) bool { return slices.Contains(rateReasons, r) })
}
