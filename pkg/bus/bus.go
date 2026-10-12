package bus

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// Errors returned by buses, checked with [errors.Is]. A done context is
// reported as the context's own error.
var (
	// ErrGap means messages the subscriber asked for are no longer kept: the
	// topic's history was trimmed past its position, expired, or lost (a Redis
	// restart without its data). Reload from the source of truth and
	// subscribe again after what was reloaded.
	ErrGap = errors.New("bus history has a gap")
	// ErrStale means a published message's ID is not after the topic's last
	// one. The message was not published. A publisher retrying after a crash
	// sees this for messages that already went out, and may ignore it.
	ErrStale = errors.New("message id is not after the topic's last")
	// ErrInvalid means an argument cannot be used: a zero topic, a zero
	// message ID, or a trim policy with no limits.
	ErrInvalid = errors.New("invalid argument")
	// ErrClosed means the bus was closed.
	ErrClosed = errors.New("bus closed")
)

// Bus publishes messages to topics and delivers them to subscribers.
//
// Implementations are safe for concurrent use. A publisher never waits for a
// subscriber: each subscriber reads the topic's history at its own pace, and
// one that falls behind the history gets [ErrGap].
type Bus interface {
	// Publish appends msg to the topic. msg.ID must be after the topic's last
	// ID ([ErrStale] otherwise) and not zero. The bus keeps its own copy of
	// msg.Data.
	//
	// Any other error, a done ctx included, may leave msg published or not.
	// Publish it again, or go on: then a subscriber past the last message
	// before it gets [ErrGap] at the next one, unless msg turns out to have
	// been published.
	Publish(ctx context.Context, topic Topic, msg Message) error
	// Subscribe delivers the topic's messages with IDs after the given one,
	// in order, then new ones as they are published, until ctx is done, the
	// bus is closed, or the history has a gap. A zero after means from the
	// start of the history.
	//
	// The only errors returned here are about the arguments and a closed bus;
	// everything that ends the subscription later, [ErrGap] included, is its
	// [Subscription.Err].
	Subscribe(ctx context.Context, topic Topic, after ID) (Subscription, error)
	// Close ends every subscription (their Err is [ErrClosed]) and waits for
	// them to stop. Publish and Subscribe then return [ErrClosed]. It does
	// not close what the bus was given (a Redis client).
	Close() error
}

// Subscription is one subscriber's stream of messages.
type Subscription interface {
	// C delivers messages in order, each with its own copy of Data. It is
	// closed when the subscription ends.
	C() <-chan Message
	// Err says why the subscription ended: [ErrGap], [ErrClosed], the
	// context's error, or a backend error. It is nil until C is closed.
	Err() error
}

// Message is one published message.
type Message struct {
	// ID is chosen by the publisher and increases along a topic.
	ID ID
	// Data is opaque to the bus.
	Data []byte
}

// ID identifies a message within its topic. IDs are ordered by Seq, then N.
//
// A run's durable events are <seq> (N is zero) and its live deltas within a
// turn are <seq>.<n>, so all of a run's events have one total order (E18). The
// text form is what SSE sends as the event id and clients send back as
// Last-Event-ID.
type ID struct {
	Seq uint64
	N   uint64
}

// IsZero reports whether id is the zero ID, which is before every message.
func (id ID) IsZero() bool { return id == ID{} }

// Compare returns -1, 0 or +1 as id is before, equal to or after o.
func (id ID) Compare(o ID) int {
	switch {
	case id.Seq < o.Seq:
		return -1
	case id.Seq > o.Seq:
		return 1
	case id.N < o.N:
		return -1
	case id.N > o.N:
		return 1
	}
	return 0
}

// String returns "<seq>" when N is zero and "<seq>.<n>" otherwise.
func (id ID) String() string {
	if id.N == 0 {
		return strconv.FormatUint(id.Seq, 10)
	}
	return strconv.FormatUint(id.Seq, 10) + "." + strconv.FormatUint(id.N, 10)
}

// ParseID parses the text form [ID.String] returns, and only that form: no
// sign, no leading zeros and no ".0", so that each ID has one spelling.
func ParseID(s string) (ID, error) {
	seq, n, dotted := strings.Cut(s, ".")
	a, err := parseDecimal(seq)
	if err != nil {
		return ID{}, fmt.Errorf("%w: id %q: %w", ErrInvalid, s, err)
	}
	if !dotted {
		return ID{Seq: a}, nil
	}
	b, err := parseDecimal(n)
	if err != nil || b == 0 {
		return ID{}, fmt.Errorf("%w: id %q: want <seq> or <seq>.<n> with n > 0", ErrInvalid, s)
	}
	return ID{Seq: a, N: b}, nil
}

func parseDecimal(s string) (uint64, error) {
	if s == "" || (len(s) > 1 && s[0] == '0') || s[0] < '0' || s[0] > '9' {
		return 0, errors.New("want a decimal number with no leading zeros")
	}
	return strconv.ParseUint(s, 10, 64)
}

// TopicKind says what a topic carries, for trim policies.
type TopicKind string

// The kinds of topic.
const (
	// KindRun carries a run's events.
	KindRun TopicKind = "run"
	// KindCommands carries commands to the process running a run.
	KindCommands TopicKind = "cmd"
	// KindUser carries one user's notifications.
	KindUser TopicKind = "user"
)

// Topic names a stream of messages. Make one with [RunTopic],
// [CommandTopic] or [UserTopic]; the zero Topic is invalid.
//
// Every topic name includes the tenant, and each part of a name is escaped,
// so two different (tenant, id) pairs never share a topic.
type Topic struct {
	name string
	kind TopicKind
}

// RunTopic is the topic of a run's events: run:{tenant}:{run}.
func RunTopic(t tenancy.Tenant, runID string) (Topic, error) {
	return newTopic(KindRun, t, runID, "")
}

// CommandTopic is the topic of the commands to a run: run:{tenant}:{run}:cmd.
func CommandTopic(t tenancy.Tenant, runID string) (Topic, error) {
	return newTopic(KindCommands, t, runID, ":cmd")
}

// UserTopic is the topic of a user's notifications: user:{tenant}:{user}.
func UserTopic(t tenancy.Tenant, userID string) (Topic, error) {
	return newTopic(KindUser, t, userID, "")
}

func newTopic(kind TopicKind, t tenancy.Tenant, id, suffix string) (Topic, error) {
	if t.ID() == "" || id == "" {
		return Topic{}, fmt.Errorf("%w: a topic needs a tenant and an id", ErrInvalid)
	}
	prefix := string(kind)
	if kind == KindCommands {
		prefix = string(KindRun)
	}
	return Topic{name: prefix + ":" + escape(t.ID()) + ":" + escape(id) + suffix, kind: kind}, nil
}

// escape keeps letters, digits, '.', '_' and '-' and writes every other byte
// as %XX, so a ':' in an id cannot forge another topic's name.
func escape(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := range len(s) {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0xf])
	}
	return b.String()
}

// IsZero reports whether t is the zero Topic.
func (t Topic) IsZero() bool { return t.name == "" }

// Kind says what the topic carries.
func (t Topic) Kind() TopicKind { return t.kind }

// String returns the topic's name.
func (t Topic) String() string { return t.name }

// Trim is how much of a topic's history a bus keeps.
type Trim struct {
	// MaxLen is the number of messages kept, at least 1. Redis keeps at least
	// this many (MAXLEN ~ trims whole nodes), the memory bus exactly this many.
	MaxLen int
	// MaxAge is how long a topic's history is kept after its last publish.
	// Then the whole topic is dropped, and a subscriber resuming from a
	// position in it gets [ErrGap].
	MaxAge time.Duration
}

// Validate returns [ErrInvalid] unless both limits are positive.
func (t Trim) Validate() error {
	if t.MaxLen < 1 || t.MaxAge <= 0 {
		return fmt.Errorf("%w: trim needs MaxLen >= 1 and MaxAge > 0, got %+v", ErrInvalid, t)
	}
	return nil
}

// DefaultTrim is the trim policy buses use unless told otherwise: room for a
// long run's deltas for a day, and a user's recent notifications for an hour.
func DefaultTrim(t Topic) Trim {
	switch t.Kind() {
	case KindRun:
		return Trim{MaxLen: 10000, MaxAge: 24 * time.Hour}
	case KindCommands:
		return Trim{MaxLen: 1000, MaxAge: 24 * time.Hour}
	default:
		return Trim{MaxLen: 1000, MaxAge: time.Hour}
	}
}
