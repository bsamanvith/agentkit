package cycletest_test

import (
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/cycle"
	"github.com/bsamanvith/agentkit/apps/farm/cycle/cycletest"
)

func TestMemory(t *testing.T) {
	t.Parallel()
	cycletest.RunStore(t, func(*testing.T) cycle.Store { return cycletest.NewMemory() })
}
