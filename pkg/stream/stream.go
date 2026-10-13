package stream

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/clock"
)

// ErrInvalid means an argument cannot be used: an empty event type, data
// that is not JSON, a missing store or bus. Check with [errors.Is].
var ErrInvalid = errors.New("stream: invalid argument")

// Event is one event of a run's stream or of a user's notifications.
type Event struct {
	// ID orders the events of one stream; its text form is the SSE id.
	ID bus.ID
	// Type names the event; the stream does not interpret it beyond the
	// terminal function given to [Watch].
	Type string
	// Data is the event's JSON, kept byte for byte. It may be empty.
	Data json.RawMessage
}

// envelope is an event as the bus carries it.
type envelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

func encode(typ string, data json.RawMessage) ([]byte, error) {
	if err := check(typ, data); err != nil {
		return nil, err
	}
	return json.Marshal(envelope{Type: typ, Data: data})
}

func decode(m bus.Message) (Event, error) {
	var e envelope
	if err := json.Unmarshal(m.Data, &e); err != nil || e.Type == "" {
		return Event{}, fmt.Errorf("stream: message %s is not an event: %q", m.ID, m.Data)
	}
	return Event{ID: m.ID, Type: e.Type, Data: e.Data}, nil
}

func check(typ string, data json.RawMessage) error {
	if typ == "" {
		return fmt.Errorf("%w: event with no type", ErrInvalid)
	}
	if len(data) > 0 && !json.Valid(data) {
		return fmt.Errorf("%w: %s event: data is not JSON", ErrInvalid, typ)
	}
	return nil
}

// Option configures a [Publisher], a [Notifier] or [Commands].
type Option func(*config)

type config struct {
	log  *slog.Logger
	clk  clock.Clock
	poll time.Duration
}

// DefaultPoll is how often [Watch] checks the log for events the bus did not
// bring, unless [WithPoll] says otherwise.
const DefaultPoll = 10 * time.Second

func newConfig(opts []Option) config {
	c := config{log: slog.Default(), clk: clock.Real(), poll: DefaultPoll}
	for _, o := range opts {
		o(&c)
	}
	return c
}

// WithLogger sets where failed publishes are logged (default [slog.Default]).
func WithLogger(l *slog.Logger) Option {
	return func(c *config) {
		if l != nil {
			c.log = l
		}
	}
}

// WithClock sets the clock a [Notifier] takes its IDs from and [Watch] and
// an [Inbox] wait with (default [clock.Real]).
func WithClock(clk clock.Clock) Option {
	return func(c *config) {
		if clk != nil {
			c.clk = clk
		}
	}
}

// WithPoll sets how often [Watch] checks the log while the bus is quiet
// (default [DefaultPoll]); zero or less keeps the default. The check finds
// events whose publish failed with nothing published after them, above all
// a run's last one, which no later message would report as a gap.
func WithPoll(d time.Duration) Option {
	return func(c *config) {
		if d > 0 {
			c.poll = d
		}
	}
}
