package worktest_test

import (
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/work"
	"github.com/bsamanvith/agentkit/apps/farm/work/worktest"
)

func TestMemory(t *testing.T) {
	t.Parallel()
	worktest.RunStore(t, func(*testing.T) work.Store { return worktest.NewMemory() })
}
