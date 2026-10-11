package memory_test

import (
	"testing"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/memory"
	"github.com/bsamanvith/agentkit/pkg/store/storetest"
)

func newConversations(_ *testing.T, clk clock.Clock) store.ConversationStore {
	return memory.New(memory.WithClock(clk))
}

func newRuns(_ *testing.T, clk clock.Clock) store.RunStore {
	return memory.New(memory.WithClock(clk))
}

func TestConversationStore(t *testing.T) {
	t.Parallel()
	storetest.RunConversationStore(t, newConversations)
}

func TestRunStore(t *testing.T) {
	t.Parallel()
	storetest.RunRunStore(t, newRuns)
}

func TestCheckpointer(t *testing.T) {
	t.Parallel()
	storetest.RunCheckpointer(t, func(*testing.T) flow.Checkpointer { return memory.New() })
}

func FuzzConversationSimulation(f *testing.F) {
	for seed := range uint64(8) {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed uint64) {
		storetest.SimulateConversations(t, newConversations, seed)
	})
}

func FuzzRunSimulation(f *testing.F) {
	for seed := range uint64(8) {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed uint64) {
		storetest.SimulateRuns(t, newRuns, seed)
	})
}
