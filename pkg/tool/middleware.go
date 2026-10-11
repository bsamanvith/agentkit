package tool

import (
	"context"
	"encoding/json"
	"time"
)

// Timeout limits every call to d. A call that runs longer sees its context
// expire and returns an error matching context.DeadlineExceeded. Tools must
// honor their context for this to take effect.
func Timeout(d time.Duration) Middleware {
	return func(next InvokeFunc) InvokeFunc {
		return func(ctx context.Context, def Definition, input json.RawMessage) (json.RawMessage, error) {
			ctx, cancel := context.WithTimeout(ctx, d)
			defer cancel()
			return next(ctx, def, input)
		}
	}
}
