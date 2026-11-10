package memberstest_test

import (
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/members"
	"github.com/bsamanvith/agentkit/apps/farm/members/memberstest"
)

func TestMemory(t *testing.T) {
	memberstest.RunStore(t, func(*testing.T) members.Store { return &memberstest.Memory{} })
}
