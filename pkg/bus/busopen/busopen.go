// Package busopen opens the bus a spec names, such as $AGENTKIT_BUS:
// "memory" (one process), or a redis:// or rediss:// URL. It is the one table
// of bus backends; a program adds its own with [WithBackends].
//
// The noredis build tag leaves Redis out; a spec for it is then
// [ErrNotBuilt], which names the tag.
//
// A backend must keep the contract of [bus.Bus]: Publish is an atomic
// conditional append (an ID not after the topic's last is [bus.ErrStale],
// across every process), and a subscription replays from an ID and reports a
// gap. NATS JetStream can keep it (expected last message ID); a plain queue
// (SQS, Kafka without a sequencer) cannot.
package busopen

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/bus/memory"
	"github.com/bsamanvith/agentkit/pkg/clock"
)

var (
	// ErrUnknownSpec reports a spec no backend matches. The error never
	// shows the spec: a URL can carry a password.
	ErrUnknownSpec = errors.New("unknown bus spec")
	// ErrNotBuilt reports a spec for a backend this binary's build tags
	// leave out.
	ErrNotBuilt = errors.New("bus backend not built")
)

// Config is what [Open] passes a [Backend].
type Config struct {
	// Clock and Logger are never nil.
	Clock  clock.Clock
	Logger *slog.Logger
}

// Backend opens the buses of the specs it matches.
type Backend struct {
	// Name names the backend in errors ("Redis").
	Name string
	// Prefixes are what its specs start with: one that ends in ":" or "://"
	// is a prefix, any other ("memory") the whole spec.
	Prefixes []string
	// Open opens the bus spec names, and returns the function that closes
	// it.
	Open func(ctx context.Context, spec string, cfg Config) (bus.Bus, func() error, error)
}

func (b Backend) matches(spec string) bool {
	for _, p := range b.Prefixes {
		if spec == p || (strings.HasSuffix(p, ":") || strings.HasSuffix(p, "://")) && strings.HasPrefix(spec, p) {
			return true
		}
	}
	return false
}

// Option configures [Open].
type Option func(*options)

type options struct {
	cfg   Config
	extra []Backend
}

// WithClock sets the bus's clock (default, also for nil, [clock.Real]).
func WithClock(c clock.Clock) Option {
	return func(o *options) {
		if c != nil {
			o.cfg.Clock = c
		}
	}
}

// WithLogger sets the bus's logger (default, also for nil, discard).
func WithLogger(l *slog.Logger) Option {
	return func(o *options) {
		if l != nil {
			o.cfg.Logger = l
		}
	}
}

// WithBackends adds backends to the built-in ones, tried first.
func WithBackends(b ...Backend) Option {
	return func(o *options) { o.extra = append(o.extra, b...) }
}

// Backends returns the built-in backends, in the order [Open] tries them;
// those this binary's build tags leave out answer [ErrNotBuilt].
func Backends() []Backend { return []Backend{memoryBackend(), redisBackend()} }

// Open opens the bus spec names; an empty spec is "memory".
func Open(ctx context.Context, spec string, opts ...Option) (bus.Bus, func() error, error) {
	if spec == "" {
		spec = "memory"
	}
	o := options{cfg: Config{Clock: clock.Real(), Logger: slog.New(slog.DiscardHandler)}}
	for _, opt := range opts {
		opt(&o)
	}
	all := append(slices.Clip(o.extra), Backends()...)
	for _, b := range all {
		if b.matches(spec) {
			return b.Open(ctx, spec, o.cfg)
		}
	}
	var names []string
	for _, b := range all {
		for _, p := range b.Prefixes {
			names = append(names, p+"…")
		}
	}
	return nil, nil, fmt.Errorf("%w; want one of %s", ErrUnknownSpec, strings.Join(names, ", "))
}

// memoryBackend is the bus of one process.
func memoryBackend() Backend {
	return Backend{
		Name: "memory", Prefixes: []string{"memory"},
		Open: func(_ context.Context, _ string, cfg Config) (bus.Bus, func() error, error) {
			b := memory.New(memory.WithClock(cfg.Clock))
			return b, b.Close, nil
		},
	}
}
