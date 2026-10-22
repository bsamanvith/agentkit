package inmem_test

import (
	"testing"

	"github.com/bsamanvith/agentkit/pkg/memory"
	"github.com/bsamanvith/agentkit/pkg/memory/inmem"
	"github.com/bsamanvith/agentkit/pkg/memory/memorytest"
)

func newIndex(t *testing.T, cfg memory.IndexConfig) memory.Index {
	t.Helper()
	x, err := inmem.NewIndex(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return x
}

// TestIndexConformance: the suite, with its keyword tests and the keyword
// relevance set.
func TestIndexConformance(t *testing.T) {
	t.Parallel()
	memorytest.RunIndex(t, newIndex)
}

// FuzzKeywordSimulation runs the suite's keyword simulation on any seed.
func FuzzKeywordSimulation(f *testing.F) {
	for seed := range uint64(4) {
		f.Add(seed+100, 60)
	}
	f.Fuzz(func(t *testing.T, seed uint64, n int) {
		memorytest.SimulateKeyword(t, newIndex(t, memory.IndexConfig{Scope: memory.ScopeUser, MaxItems: 5}), 5, seed, min(max(n, 0), 200))
	})
}

func TestNewIndexRefusesABadConfig(t *testing.T) {
	t.Parallel()
	for _, cfg := range []memory.IndexConfig{{Scope: "team"}, {MaxItems: -1}, {MaxTextBytes: -1}} {
		if _, err := inmem.NewIndex(cfg); err == nil {
			t.Errorf("NewIndex(%+v) succeeded", cfg)
		}
	}
}
