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

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/store/memory"
	"github.com/bsamanvith/agentkit/pkg/store/storeopen"
)

// ErrUnknownSpec reports a store spec [Open] does not know. It is
// [storeopen.ErrUnknownSpec].
var ErrUnknownSpec = storeopen.ErrUnknownSpec

// Store is what a serving component gets of the store: every interface but
// the backend's Admin.
type Store = storeopen.Store

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

// Open opens the store spec names, as AGENTKIT_STORE does ([storeopen.Open]):
// "memory", "sqlite:" and a file path, or a "postgres://" (or
// "postgresql://") URL, of the backends this binary is built with. It
// returns the store and the function that closes it. The SQLite and Postgres
// stores apply their migrations. clk is the store's clock (nil: the real
// clock). An unknown spec is [ErrUnknownSpec].
func Open(ctx context.Context, spec string, clk clock.Clock) (Store, func() error, error) {
	if spec == "" {
		return nil, nil, errors.Join(ErrUnknownSpec, errors.New("empty spec"))
	}
	o, err := storeopen.Open(ctx, spec, storeopen.WithClock(clk))
	if err != nil {
		return nil, nil, err
	}
	return view{o.Store}, o.Close, nil
}
