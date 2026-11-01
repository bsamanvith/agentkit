package farmtest_test

import (
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farm/farmtest"
)

func TestMemory(t *testing.T) {
	t.Parallel()
	farmtest.RunStore(t, func(*testing.T) farm.Store { return &farmtest.Memory{} })
}

func TestMemoryGroups(t *testing.T) {
	t.Parallel()
	farmtest.RunGroupStore(t, func(*testing.T) farm.GroupStore { return &farmtest.Memory{} })
}
