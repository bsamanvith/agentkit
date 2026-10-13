package memory_test

import (
	"reflect"
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

func newAdmin(_ *testing.T, clk clock.Clock) (store.RunStore, store.Admin) {
	s := memory.New(memory.WithClock(clk))
	return s, memory.Admin(s)
}

func newEvents(_ *testing.T, clk clock.Clock) store.EventLog {
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

func TestAdmin(t *testing.T) {
	t.Parallel()
	storetest.RunAdmin(t, newAdmin)
}

func TestEventLog(t *testing.T) {
	t.Parallel()
	storetest.RunEventLog(t, newEvents)
}

func TestCommandLog(t *testing.T) {
	t.Parallel()
	storetest.RunCommandLog(t, func(_ *testing.T, clk clock.Clock) storetest.CommandStore { return memory.New(memory.WithClock(clk)) })
}

// The cross-tenant read is not reachable from the tenant-scoped store: code
// holding it cannot type-assert its way to other tenants' runs, neither to
// store.Admin itself nor to any method that hands one out.
func TestAdminIsASeparateValue(t *testing.T) {
	t.Parallel()
	s := memory.New()
	var rs store.RunStore = s
	if _, ok := rs.(store.Admin); ok {
		t.Error("the tenant-scoped store is also a store.Admin")
	}
	admin := reflect.TypeFor[store.Admin]()
	for m := range reflect.TypeOf(s).Methods() {
		for out := range m.Type.Outs() {
			if out == admin || out.Implements(admin) {
				t.Errorf("(*memory.Store).%s returns a %v, which reaches other tenants' runs", m.Name, out)
			}
		}
	}
	if _, ok := memory.Admin(s).(store.RunStore); ok {
		t.Error("the Admin view is also a store.RunStore")
	}
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

func FuzzAdminSimulation(f *testing.F) {
	for seed := range uint64(8) {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed uint64) {
		storetest.SimulateAdmin(t, newAdmin, seed)
	})
}

func FuzzEventSimulation(f *testing.F) {
	for seed := range uint64(8) {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed uint64) {
		storetest.SimulateEvents(t, newEvents, seed)
	})
}
