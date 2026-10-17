package busopen_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/bus/busopen"
	"github.com/bsamanvith/agentkit/pkg/bus/memory"
)

// TestOpen: memory (also for an empty spec) opens a bus, a program's own
// backend opens from its spec, and a spec nothing opens is ErrUnknownSpec
// without the password it carried.
func TestOpen(t *testing.T) {
	t.Parallel()
	for _, spec := range []string{"", "memory"} {
		b, closeBus, err := busopen.Open(t.Context(), spec)
		if err != nil || b == nil {
			t.Fatalf("Open(%q) = %v, %v", spec, b, err)
		}
		if err := closeBus(); err != nil {
			t.Errorf("Open(%q): close: %v", spec, err)
		}
	}
	own := busopen.Backend{
		Name: "mine", Prefixes: []string{"mine://"},
		Open: func(context.Context, string, busopen.Config) (bus.Bus, func() error, error) {
			b := memory.New()
			return b, b.Close, nil
		},
	}
	if _, _, err := busopen.Open(t.Context(), "mine://x", busopen.WithBackends(own)); err != nil {
		t.Errorf("own backend: %v", err)
	}
	for _, spec := range []string{"mem", "nats://u:secret@host"} {
		_, _, err := busopen.Open(t.Context(), spec)
		if !errors.Is(err, busopen.ErrUnknownSpec) || strings.Contains(err.Error(), "secret") {
			t.Errorf("Open(%q) = %v, want ErrUnknownSpec without the password", spec, err)
		}
	}
}
