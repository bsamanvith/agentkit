package redis_test

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package if any goroutine outlives its tests: every
// subscription the tests cancel or close must stop, and with it its reads.
// The tests themselves need Redis and the integration tag.
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }
