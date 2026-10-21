// Package sqltime encodes the times of credential blobs for the SQL
// backends (credentials/sqlite, credentials/postgres): Unix nanoseconds, as
// the stores keep theirs, NULL for no expiry.
package sqltime

import (
	"fmt"
	"math"
	"time"

	"github.com/bsamanvith/agentkit/pkg/credentials"
)

// The times Unix nanoseconds in an int64 can hold: 1677 to 2262.
var (
	minTime = time.Unix(0, math.MinInt64)
	maxTime = time.Unix(0, math.MaxInt64)
)

func encode(what string, t time.Time) (int64, error) {
	if t.Before(minTime) || t.After(maxTime) {
		return 0, fmt.Errorf("%w: %s %s is outside the years 1678 to 2262", credentials.ErrInvalidCredential, what, t.UTC().Format(time.RFC3339))
	}
	return t.UnixNano(), nil
}

// Blob encodes b's UpdatedAt, and its Expiry (nil for none).
func Blob(b credentials.Blob) (at int64, expiry *int64, err error) {
	if at, err = encode("time", b.UpdatedAt); err != nil {
		return 0, nil, err
	}
	if !b.Expiry.IsZero() {
		e, err := encode("expiry", b.Expiry)
		if err != nil {
			return 0, nil, err
		}
		expiry = &e
	}
	return at, expiry, nil
}

// Load decodes a time column.
func Load(n int64) time.Time { return time.Unix(0, n).UTC() }

// LoadNull decodes a time column that may be NULL (the zero time).
func LoadNull(n int64, valid bool) time.Time {
	if !valid {
		return time.Time{}
	}
	return Load(n)
}
