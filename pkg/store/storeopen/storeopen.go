// Package storeopen opens the store a spec names, such as $AGENTKIT_STORE:
// "memory", "sqlite:" and a file path, or a postgres:// URL. It is the one
// table of store backends, for the binary (cmd/agentkit) and for
// [scoped.Open]; a program adds its own backends with [WithBackends].
//
// The backends a binary links are chosen by build tags, so that an image
// carries only the drivers it runs: nosqlite leaves SQLite out, nopostgres
// Postgres. A spec for a backend left out is [ErrNotBuilt], which names the
// tag.
//
// [Opened.Admin] is the backend's cross-tenant [store.Admin], which only the
// runner's recovery loop may hold (T038): a serving package never imports
// this package, and takes its store from [scoped] in tests.
//
// [scoped]: https://pkg.go.dev/github.com/bsamanvith/agentkit/pkg/store/scoped
// [scoped.Open]: https://pkg.go.dev/github.com/bsamanvith/agentkit/pkg/store/scoped#Open
package storeopen

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/bsamanvith/agentkit/internal/specs"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/flow"
	"github.com/bsamanvith/agentkit/pkg/store"
)

var (
	// ErrUnknownSpec reports a spec no backend matches, or one its backend
	// cannot use (a sqlite: spec with no path). The error never shows the
	// spec: a URL can carry a password.
	ErrUnknownSpec = errors.New("unknown store spec")
	// ErrNotBuilt reports a spec for a backend this binary's build tags
	// leave out.
	ErrNotBuilt = errors.New("store backend not built")
	// ErrNoMigrations reports a [Migrate] of a backend with no migrations to
	// run as a job.
	ErrNoMigrations = errors.New("store has no migrations to run as a job")
)

// Store is a whole store: every interface a backend implements. A backend's
// store must be one transactional store, not several joined: deleting a
// conversation removes its log, runs, events, checkpoints and notifications at once, and a
// command is appended under the run's lease in the same write.
type Store interface {
	store.ConversationStore
	store.ConversationIndex
	store.RunStore
	store.EventLog
	store.CommandLog
	store.ToolsetStore
	store.RunIndex
	store.NotificationLog
	store.ItemStore
	store.MessageLog
	store.TriggerStore
	flow.Checkpointer
}

// Opened is an open store: the store, its cross-tenant Admin, and the
// function that closes it.
type Opened struct {
	Store Store
	Admin store.Admin
	// Triggers finds triggers across tenants, as Admin finds runs.
	Triggers store.TriggerAdmin
	Close    func() error
}

// Config is what [Open] passes a [Backend].
type Config struct {
	// Clock is the store's clock, which times entries, runs and leases. It
	// is never nil.
	Clock clock.Clock
	// Migrate says whether to apply the migrations the database lacks (the
	// default) or only check that it has them all.
	Migrate bool
	// AdminURL, if set, is where the backend's [store.Admin] connects, as a
	// role that may read across tenants (Postgres: one that bypasses
	// row-level security). Backends without such roles ignore it.
	AdminURL string
}

// Backend opens the stores of the specs it matches.
type Backend struct {
	// Name names the backend in errors ("SQLite").
	Name string
	// Prefixes are what its specs start with: one that ends in ":" or "://"
	// ("sqlite:", "postgres://") is a prefix, any other ("memory") the whole
	// spec.
	Prefixes []string
	// Open opens the store spec names, spec in full.
	Open func(ctx context.Context, spec string, cfg Config) (Opened, error)
	// Migrate, if set, applies the migrations to the database spec names,
	// as a job.
	Migrate func(ctx context.Context, spec string) error
}

func (b Backend) matches(spec string) bool { return specs.Match(b.Prefixes, spec) }

// Option configures [Open] and [Migrate].
type Option func(*options)

type options struct {
	cfg   Config
	extra []Backend
}

// WithClock sets the store's clock (default, also for nil, [clock.Real]).
func WithClock(c clock.Clock) Option {
	return func(o *options) {
		if c != nil {
			o.cfg.Clock = c
		}
	}
}

// WithMigrate sets whether opening a store applies the migrations its
// database lacks (the default) or only checks that it has them all.
func WithMigrate(on bool) Option { return func(o *options) { o.cfg.Migrate = on } }

// WithAdminURL sets [Config.AdminURL].
func WithAdminURL(url string) Option { return func(o *options) { o.cfg.AdminURL = url } }

// WithBackends adds backends to the built-in ones, tried first: a program's
// own store (DynamoDB, ...) opens from a spec like the others.
func WithBackends(b ...Backend) Option {
	return func(o *options) { o.extra = append(o.extra, b...) }
}

func newOptions(opts []Option) options {
	o := options{cfg: Config{Clock: clock.Real(), Migrate: true}}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// Backends returns the built-in backends, in the order [Open] tries them;
// those this binary's build tags leave out answer [ErrNotBuilt].
func Backends() []Backend { return []Backend{memoryBackend(), sqliteBackend(), postgresBackend()} }

func find(spec string, o options) (Backend, error) {
	all := append(slices.Clip(o.extra), Backends()...)
	for _, b := range all {
		if b.matches(spec) {
			return b, nil
		}
	}
	prefixes := make([][]string, len(all))
	for i, b := range all {
		prefixes[i] = b.Prefixes
	}
	return Backend{}, fmt.Errorf("%w; want one of %s", ErrUnknownSpec, specs.Want(prefixes...))
}

// Open opens the store spec names; an empty spec is "memory".
func Open(ctx context.Context, spec string, opts ...Option) (Opened, error) {
	if spec == "" {
		spec = "memory"
	}
	o := newOptions(opts)
	b, err := find(spec, o)
	if err != nil {
		return Opened{}, err
	}
	return b.Open(ctx, spec, o.cfg)
}

// Migrate applies the migrations of the store spec names, as a one-off job:
// [ErrNoMigrations] for a backend that has none to run that way.
func Migrate(ctx context.Context, spec string, opts ...Option) error {
	if spec == "" {
		spec = "memory"
	}
	b, err := find(spec, newOptions(opts))
	if err != nil {
		return err
	}
	if b.Migrate == nil {
		return fmt.Errorf("%w: %s", ErrNoMigrations, b.Name)
	}
	return b.Migrate(ctx, spec)
}

// notBuilt is the backend of name, left out by tag.
func notBuilt(name, tag string, prefixes ...string) Backend { //nolint:unused // only the builds that leave a backend out call it
	err := fmt.Errorf("%w: this agentkit is built without %s (-tags %s); use an image that has it", ErrNotBuilt, name, tag)
	return Backend{
		Name: name, Prefixes: prefixes,
		Open:    func(context.Context, string, Config) (Opened, error) { return Opened{}, err },
		Migrate: func(context.Context, string) error { return err },
	}
}
