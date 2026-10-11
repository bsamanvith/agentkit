package llm

import "errors"

// Failures a model can report. Adapters wrap the provider's error so that
// errors.Is matches one of these and errors.As still reaches the original:
//
//	return fmt.Errorf("%w: %w", llm.ErrRateLimited, providerErr)
//
// Transport failures (a dropped connection, a 5xx, a stream cut short) have no
// sentinel of their own: adapters report them as [ErrOverloaded], so they are
// retryable. A cancelled or expired context is reported as the context's own error and
// matches none of these.
var (
	// ErrRateLimited means the provider is throttling this caller. Retry later.
	ErrRateLimited = errors.New("rate limited")
	// ErrOverloaded means the provider cannot serve the request right now.
	// Retry later.
	ErrOverloaded = errors.New("model overloaded")
	// ErrInvalidRequest means the provider rejected the request itself.
	// Retrying the same request will fail again.
	ErrInvalidRequest = errors.New("invalid request")
	// ErrAuth means the credentials are missing, wrong or not allowed.
	ErrAuth = errors.New("authentication failed")
	// ErrContextTooLong means the prompt does not fit the model's context
	// window. Shorten the history; do not retry as is.
	ErrContextTooLong = errors.New("context too long")
)

// IsRetryable reports whether err is a transient failure that the same
// request may survive: [ErrRateLimited] or [ErrOverloaded], on their own or
// wrapped.
func IsRetryable(err error) bool {
	return errors.Is(err, ErrRateLimited) || errors.Is(err, ErrOverloaded)
}
