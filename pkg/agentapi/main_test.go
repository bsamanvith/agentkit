package agentapi_test

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain fails the package if a goroutine outlives its tests: a run's
// execution, a stream, or a shared watch (T103) that nobody stopped.
func TestMain(m *testing.M) { goleak.VerifyTestMain(m) }
