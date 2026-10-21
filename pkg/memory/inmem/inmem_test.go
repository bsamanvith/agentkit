package inmem_test

import (
	"testing"

	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/inmem"
	"github.com/bsamanvith/agentkit/pkg/memory/memorytest"
)

func newStore(t *testing.T, cfg memory.Config) memory.Store {
	t.Helper()
	s, err := inmem.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestConformance(t *testing.T) {
	t.Parallel()
	memorytest.RunStore(t, newStore)
}

// FuzzSimulation runs the suite's seeded simulation on any seed.
func FuzzSimulation(f *testing.F) {
	for seed := range uint64(4) {
		f.Add(seed+100, 40)
	}
	f.Fuzz(func(t *testing.T, seed uint64, n int) {
		l := memory.Limits{MaxFileBytes: 48, MaxScopeBytes: 160, MaxFiles: 6}
		s := newStore(t, memory.Config{Limits: l})
		memorytest.Simulate(t, s, l, seed, min(max(n, 0), 80))
	})
}

func TestNewRejectsABadConfig(t *testing.T) {
	t.Parallel()
	for _, cfg := range []memory.Config{{Scope: "team"}, {Limits: memory.Limits{MaxFiles: -1}}} {
		if _, err := inmem.New(cfg); err == nil {
			t.Errorf("New(%+v) succeeded, want an error", cfg)
		}
	}
}
