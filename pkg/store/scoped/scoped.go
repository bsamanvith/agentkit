// Package scoped gives a component that serves requests its store: the
// tenant-scoped views of a backend (conversations and their records, runs,
// checkpoints), and never the backend's cross-tenant [store.Admin], which
// only the runner's recovery loop may hold (T038).
//
// A serving package (pkg/agentapi) never imports a store backend, since a
// backend is the way to its Admin. Its tests and examples take their store
// from here: [Memory], or the backend a spec names ([Open]). A program that
// runs the recovery loop opens its backend itself, for the Admin.
package scoped

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/memory"
	"github.com/bsamanvith/agentkit/pkg/store/postgres"
	"github.com/bsamanvith/agentkit/pkg/store/sqlite"
)

// ErrUnknownSpec reports a store spec [Open] does not know.
var ErrUnknownSpec = errors.New("unknown store spec")

// Store is what a serving component gets of the store.
type Store interface {
	store.ConversationStore
	store.ConversationIndex
	store.RunStore
	flow.Checkpointer
}

// view hides the backend behind the interface: a caller cannot assert its
// way back to the backend's type, and so to its Admin.
type view struct{ Store }

// Memory returns an empty in-memory store whose times come from clk (nil:
// the real clock). What it keeps is lost when the process ends.
func Memory(clk clock.Clock) Store {
	if clk == nil {
		clk = clock.Real()
	}
	return view{memory.New(memory.WithClock(clk))}
}

// Open opens the store spec names, as AGENTKIT_STORE does: "memory",
// "sqlite:" and a file path, or a "postgres://" (or "postgresql://") URL. It
// returns the store and the function that closes it. The SQLite and
// Postgres stores apply their migrations. clk is the store's clock (nil: the
// real clock). An unknown spec is [ErrUnknownSpec].
func Open(ctx context.Context, spec string, clk clock.Clock) (Store, func() error, error) {
	if clk == nil {
		clk = clock.Real()
	}
	switch {
	case spec == "memory":
		return Memory(clk), func() error { return nil }, nil
	case strings.HasPrefix(spec, "sqlite:"):
		path := strings.TrimPrefix(spec, "sqlite:")
		if path == "" {
			return nil, nil, fmt.Errorf("%w: %q has no file path", ErrUnknownSpec, spec)
		}
		s, err := sqlite.Open(ctx, path, sqlite.WithClock(clk))
		if err != nil {
			return nil, nil, err
		}
		return view{s}, s.Close, nil
	case strings.HasPrefix(spec, "postgres://"), strings.HasPrefix(spec, "postgresql://"):
		s, err := postgres.Open(ctx, spec, postgres.WithClock(clk))
		if err != nil {
			return nil, nil, err
		}
		return view{s}, func() error { s.Close(); return nil }, nil
	}
	return nil, nil, fmt.Errorf("%w: %q; want memory, sqlite:/path or postgres://…", ErrUnknownSpec, redact(spec))
}

// redact drops what could be a password from a spec shown in an error.
func redact(spec string) string {
	if i := strings.Index(spec, "@"); i >= 0 {
		if j := strings.Index(spec, "://"); j >= 0 && j < i {
			return spec[:j+3] + "…" + spec[i:]
		}
	}
	return spec
}
