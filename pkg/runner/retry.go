package runner

import (
	"fmt"
	"time"
)

// RetryPolicy says how a run that fails with a transient model error
// ([llm.IsRetryable]) is tried again: a background run by the recovery loop
// ([WithRetry]), an interactive one inline ([WithInteractiveRetry]). Other errors are never retried
// automatically: [harness.ErrBudgetExceeded], [harness.ErrRefused],
// [harness.ErrMaxTokens], [flow.ErrShapeChanged], invalid input, and, until
// compaction exists (T031), [llm.ErrContextTooLong].
//
// After a run's n-th failure ([store.Run.Attempts], counted by the store), it
// is tried again if n < MaxAttempts, after a delay d/2 + rand[0, d/2] where
// d = min(MaxDelay, BaseDelay·2^(n-1)), so delays grow and two runs failing at
// once spread out. From the MaxAttempts-th failure on it is failed for good,
// and only an operator's [Runner.Resume] continues it.
type RetryPolicy struct {
	MaxAttempts int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
}

// DefaultInteractiveRetryPolicy is 3 attempts, from 1s up to 8s: a user
// waits through them ([WithInteractiveRetry]).
func DefaultInteractiveRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxAttempts: 3, BaseDelay: time.Second, MaxDelay: 8 * time.Second}
}

// DefaultRetryPolicy is 5 attempts, from 2s up to 5 minutes.
func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxAttempts: 5, BaseDelay: 2 * time.Second, MaxDelay: 5 * time.Minute}
}

func (p RetryPolicy) check() error {
	if p.MaxAttempts < 1 || p.BaseDelay <= 0 || p.MaxDelay < p.BaseDelay {
		return fmt.Errorf("%w: retry policy %+v", ErrInvalidConfig, p)
	}
	return nil
}

// delay is the wait after the n-th failure (n from 1).
func (p RetryPolicy) delay(n int, jitter func(int64) int64) time.Duration {
	d := p.BaseDelay
	for i := 1; i < n && d < p.MaxDelay; i++ {
		d *= 2
	}
	d = min(d, p.MaxDelay)
	half := d / 2
	if half <= 0 {
		return d
	}
	return d - half + time.Duration(jitter(int64(half)+1))
}
