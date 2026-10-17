// Package redis is the [bus.Bus] across replicas, on Redis Streams (Redis 7
// or later).
//
// Each topic is a stream. Publish runs one script: it rejects a stale ID,
// adds the message with the ID of the one before it ("p", empty for the first
// of a stream), trims with MAXLEN ~ and sets the stream to expire MaxAge after
// this publish. Each subscription reads with XREAD BLOCK on a connection of
// its own and checks every entry's "p" against what it delivered last, so a
// trim or a lost stream is [bus.ErrGap], never a silent skip.
//
// A bus also remembers what it published, or tried to, on each stream. When
// the stream lacks it (Redis restored an older snapshot, or a failed publish
// did not land), the next entry's "p" is that remembered ID rather than the
// stream's last, so subscribers before it get ErrGap. Only the process that
// published remembers: if Redis loses writes and another process (another
// replica, or this one restarted) publishes next, a subscriber positioned
// inside the lost range can skip it silently. The store, not the bus, is the
// source of truth for what must not be lost.
//
// Every subscription holds one connection while it runs. Give subscriptions
// a client of their own ([WithSubscribeClient]) sized for them, so that many
// subscribers cannot starve publishes of connections. Clients'
// MaintNotificationsConfig must not be in auto mode (go-redis's default); see
// [New].
package redis

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"

	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/bus/internal/stream"
	"github.com/bsamanvith/agentkit/pkg/clock"
)

// readBatch is the most entries one XREAD returns.
const readBatch = 256

// Bus is the Redis Streams bus. Make one with [New].
type Bus struct {
	client     *goredis.Client
	subClient  *goredis.Client // subscriptions' connections; client by default
	log        *slog.Logger
	prefix     string
	trim       func(bus.Topic) bus.Trim
	clock      clock.Clock
	block      time.Duration
	backoffMin time.Duration
	backoffMax time.Duration
	subs       stream.Group
	published  lastPublished
	// beforeXRead, set only by tests, runs between a read's last context
	// check and its XREAD: where a cancel races the read.
	beforeXRead func()
}

var _ bus.Bus = (*Bus)(nil)

// Option configures a [Bus].
type Option func(*Bus)

// WithPrefix sets the prefix of every key the bus writes (default
// "agentkit:bus:"), so that the bus shares a Redis with other uses.
func WithPrefix(p string) Option { return func(b *Bus) { b.prefix = p } }

// WithTrim sets the trim policy per topic (default [bus.DefaultTrim]).
func WithTrim(trim func(bus.Topic) bus.Trim) Option { return func(b *Bus) { b.trim = trim } }

// WithClock sets the clock that times reconnect backoff and the retries of
// unblocking a cancelled read (default, also for nil, [clock.Real]).
func WithClock(c clock.Clock) Option {
	return func(b *Bus) {
		if c != nil {
			b.clock = c
		}
	}
}

// WithBackoff sets the wait before reconnecting a subscription after Redis
// failed: min at first, doubling up to max (default 50ms to 5s).
func WithBackoff(min, max time.Duration) Option {
	return func(b *Bus) { b.backoffMin, b.backoffMax = min, max }
}

// WithSubscribeClient makes subscriptions take their connections from c
// instead of the publishing client. Each subscription holds one connection
// for as long as it runs, so with a shared pool enough subscribers make every
// Publish wait for a connection and fail. c must reach the same Redis; the bus
// does not close it.
func WithSubscribeClient(c *goredis.Client) Option { return func(b *Bus) { b.subClient = c } }

// WithLogger sets the logger (default: discard). It logs a subscription's
// failed reads, which it retries, and a cancel that Redis refuses to unblock.
func WithLogger(l *slog.Logger) Option {
	return func(b *Bus) {
		if l != nil {
			b.log = l
		}
	}
}

// WithBlock sets the longest one XREAD waits (default 5s). A cancelled
// subscription is unblocked at once (CLIENT UNBLOCK); this bounds the wait
// when that misses, such as a cancel racing the start of a read.
func WithBlock(d time.Duration) Option { return func(b *Bus) { b.block = d } }

// New returns a bus on client. The caller keeps owning client and closes it
// after the bus. The client's maintenance notifications must be disabled or
// enabled, not left in go-redis's auto mode ([bus.ErrInvalid]), and so must
// those of a [WithSubscribeClient]:
//
//	opt.MaintNotificationsConfig = &maintnotifications.Config{Mode: maintnotifications.ModeDisabled}
func New(client *goredis.Client, opts ...Option) (*Bus, error) {
	if client == nil {
		return nil, fmt.Errorf("%w: nil redis client", bus.ErrInvalid)
	}
	b := &Bus{
		client: client, subClient: client, log: slog.New(slog.DiscardHandler),
		prefix: "agentkit:bus:", trim: bus.DefaultTrim, clock: clock.Real(),
		block: 5 * time.Second, backoffMin: 50 * time.Millisecond, backoffMax: 5 * time.Second,
	}
	for _, o := range opts {
		o(b)
	}
	if b.subClient == nil {
		return nil, fmt.Errorf("%w: nil subscribe client", bus.ErrInvalid)
	}
	// In auto mode go-redis (v9.23) turns maintenance notifications off when
	// the server refuses them by writing the shared options while a
	// connection from Client.Conn reads them: a data race on every first
	// connection to a server without them, such as open-source Redis.
	for _, c := range []*goredis.Client{b.client, b.subClient} {
		if mc := c.Options().MaintNotificationsConfig; mc != nil && mc.Mode == maintnotifications.ModeAuto {
			return nil, fmt.Errorf("%w: set the client's MaintNotificationsConfig.Mode to disabled (or enabled); "+
				"go-redis's auto mode races with the per-subscription connections", bus.ErrInvalid)
		}
	}
	if b.block < time.Millisecond || b.backoffMin <= 0 || b.backoffMax < b.backoffMin {
		return nil, fmt.Errorf("%w: block %v, backoff %v to %v", bus.ErrInvalid, b.block, b.backoffMin, b.backoffMax)
	}
	return b, nil
}

// Key returns the Redis key of a topic's stream.
func (b *Bus) Key(t bus.Topic) string { return b.prefix + t.String() }

// publishScript adds ARGV[4] to stream KEYS[1] with ID ARGV[1], unless that
// ID is not after the stream's last ({0, last} then). ARGV[2] is MAXLEN and
// ARGV[3] the expiry in milliseconds. ARGV[5] is the ID the stream must have
// reached, or empty: the last ID this publisher put on it, or a later one
// whose publish failed and may have landed. A stream that is empty or whose
// last entry is before it lost writes (Redis restored an older snapshot, or
// the failed publish did not land), so the new entry follows ARGV[5] instead:
// a subscriber that got that far misses nothing, any other one gets ErrGap. IDs are compared as decimal strings, since
// Lua numbers lose precision past 2^53.
var publishScript = goredis.NewScript(`
local function before(a, b)
  local a1, a2 = string.match(a, '^(%d+)-(%d+)$')
  local b1, b2 = string.match(b, '^(%d+)-(%d+)$')
  if #a1 ~= #b1 then return #a1 < #b1 end
  if a1 ~= b1 then return a1 < b1 end
  if #a2 ~= #b2 then return #a2 < #b2 end
  return a2 < b2
end
local prev = ''
local last = redis.call('XREVRANGE', KEYS[1], '+', '-', 'COUNT', 1)
if #last > 0 then
  prev = last[1][1]
  if not before(prev, ARGV[1]) then return {0, prev} end
end
if ARGV[5] ~= '' and (prev == '' or before(prev, ARGV[5])) then prev = ARGV[5] end
redis.call('XADD', KEYS[1], 'MAXLEN', '~', ARGV[2], ARGV[1], 'p', prev, 'd', ARGV[4])
redis.call('PEXPIRE', KEYS[1], ARGV[3])
return {1}
`)

// Publish implements [bus.Bus].
func (b *Bus) Publish(ctx context.Context, t bus.Topic, msg bus.Message) error {
	if t.IsZero() || msg.ID.IsZero() {
		return fmt.Errorf("%w: publish needs a topic and a non-zero id", bus.ErrInvalid)
	}
	trim := b.trim(t)
	if err := trim.Validate(); err != nil {
		return err
	}
	if b.subs.Closed() {
		return bus.ErrClosed
	}
	key := b.Key(t)
	if err := ctx.Err(); err != nil {
		// Not sent, but the caller will go on without it: a gap all the same.
		b.published.set(key, msg.ID, false, trim.MaxAge, b.clock.Now())
		return err
	}
	ttl := max(trim.MaxAge.Milliseconds(), 1)
	res, err := publishScript.Run(ctx, b.client, []string{key},
		redisID(msg.ID), trim.MaxLen, ttl, msg.Data, b.published.reached(key, msg.ID, b.clock.Now())).Slice()
	if err != nil {
		b.published.set(key, msg.ID, false, trim.MaxAge, b.clock.Now())
		return fmt.Errorf("bus: publish to %s: %w", t, err)
	}
	if ok, _ := res[0].(int64); ok != 1 {
		// The stream's last is msg.ID itself when go-redis retried a script
		// whose reply was lost after it ran, or an earlier attempt landed:
		// remember it as published. A later last may hide this one's fate.
		last, _ := res[1].(string)
		b.published.set(key, msg.ID, last == redisID(msg.ID), trim.MaxAge, b.clock.Now())
		return fmt.Errorf("%w: %s after %s on %s", bus.ErrStale, msg.ID, last, t)
	}
	b.published.set(key, msg.ID, true, trim.MaxAge, b.clock.Now())
	return nil
}

// Last implements [bus.Bus].
func (b *Bus) Last(ctx context.Context, t bus.Topic) (bus.ID, error) {
	if t.IsZero() {
		return bus.ID{}, fmt.Errorf("%w: last needs a topic", bus.ErrInvalid)
	}
	if b.subs.Closed() {
		return bus.ID{}, bus.ErrClosed
	}
	msgs, err := b.client.XRevRangeN(ctx, b.Key(t), "+", "-", 1).Result()
	if err != nil {
		return bus.ID{}, fmt.Errorf("bus: last of %s: %w", t, err)
	}
	if len(msgs) == 0 {
		return bus.ID{}, nil
	}
	id, err := parseRedisID(msgs[0].ID)
	if err != nil {
		return bus.ID{}, fmt.Errorf("%w: stream %s: %w", errMalformed, b.Key(t), err)
	}
	return id, nil
}

// lastPublished remembers, per stream and until the stream would have expired,
// the last ID this bus published there and the last one whose publish failed,
// so a publish can tell a stream that lost writes from one that is behind for
// no reason. A failed publish may or may not have landed: a later message
// links to the stream's last entry only if that is the failed one or after it,
// so subscribers get ErrGap rather than silently miss it. A retry of the
// failed ID itself links as usual.
type lastPublished struct {
	mu        sync.Mutex
	ids       map[string]published
	lastSweep time.Time
}

type published struct {
	id, failed bus.ID
	expires    time.Time
}

// reached returns the ID stream key must have reached for next to follow its
// last entry (publishScript's ARGV[5]), or "" when there is none, including
// once the stream would have expired: a new stream after that is a restart,
// not a loss.
func (l *lastPublished) reached(key string, next bus.ID, now time.Time) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	p := l.ids[key]
	if !now.Before(p.expires) {
		return ""
	}
	id := p.id
	if p.failed.Compare(id) > 0 && next.Compare(p.failed) > 0 {
		id = p.failed
	}
	if id.IsZero() {
		return ""
	}
	return redisID(id)
}

// set records a publish of id to stream key that succeeded (ok) or failed.
func (l *lastPublished) set(key string, id bus.ID, ok bool, maxAge time.Duration, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ids == nil {
		l.ids = map[string]published{}
	}
	p := l.ids[key]
	switch {
	case !ok:
		if id.Compare(p.failed) > 0 {
			p.failed = id
		}
	case id.Compare(p.id) > 0:
		p.id = id
		if p.failed.Compare(id) <= 0 {
			p.failed = bus.ID{}
		}
	}
	p.expires = now.Add(maxAge)
	l.ids[key] = p
	if now.Sub(l.lastSweep) >= time.Minute {
		l.lastSweep = now
		for k, p := range l.ids {
			if !now.Before(p.expires) {
				delete(l.ids, k)
			}
		}
	}
}

// Subscribe implements [bus.Bus].
func (b *Bus) Subscribe(ctx context.Context, t bus.Topic, after bus.ID) (bus.Subscription, error) {
	if t.IsZero() {
		return nil, fmt.Errorf("%w: subscribe needs a topic", bus.ErrInvalid)
	}
	return b.subs.Start(ctx, &reader{b: b, key: b.Key(t)}, after)
}

// Close implements [bus.Bus]. It does not close the client.
func (b *Bus) Close() error {
	b.subs.Close()
	return nil
}

// reader is one subscription's XREAD loop on a connection of its own.
type reader struct {
	b    *Bus
	key  string
	conn *goredis.Conn
	// id is the connection's CLIENT ID, 0 while there is none, read by the
	// cancel hook to unblock a read.
	id atomic.Int64
	// reading is true from just before the last context check of a read
	// until its XREAD returns.
	reading atomic.Bool
	// stop deregisters the cancel hook; unblocked is closed when a hook that
	// ran has finished.
	stop      func() bool
	unblocked chan struct{}
}

func (r *reader) Read(ctx context.Context, after bus.ID) ([]stream.Entry, error) {
	if r.stop == nil {
		r.unblocked = make(chan struct{})
		r.stop = context.AfterFunc(ctx, func() {
			defer close(r.unblocked)
			r.unblock()
		})
	}
	wait := r.b.backoffMin
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries, err := r.read(ctx, after)
		switch {
		case err == nil && len(entries) > 0:
			return entries, nil
		case err == nil || errors.Is(err, goredis.Nil):
			continue // the block timed out or was unblocked
		case permanent(err):
			return nil, err
		}
		// The connection failed (or Redis restarted): drop it, wait, and
		// read again on a new one from the same position.
		r.drop()
		if ctx.Err() != nil {
			return nil, ctx.Err() // cancelled mid-read: not a failure
		}
		r.b.log.WarnContext(ctx, "bus: subscription read failed, retrying",
			"key", r.key, "after", after.String(), "wait", wait, "err", err)
		t := r.b.clock.NewTimer(wait)
		select {
		case <-t.C():
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		}
		wait = min(2*wait, r.b.backoffMax)
	}
}

func (r *reader) read(ctx context.Context, after bus.ID) ([]stream.Entry, error) {
	if r.conn == nil {
		r.conn = r.b.subClient.Conn()
		id, err := r.conn.ClientID(ctx).Result()
		if err != nil {
			return nil, err
		}
		r.id.Store(id)
	}
	// A cancel either comes before the check below, and nothing blocks, or
	// its hook sees reading and unblocks the XREAD (see unblock).
	r.reading.Store(true)
	defer r.reading.Store(false)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.b.beforeXRead != nil {
		r.b.beforeXRead()
	}
	streams, err := r.conn.XRead(ctx, &goredis.XReadArgs{
		Streams: []string{r.key, redisID(after)},
		Count:   readBatch,
		Block:   r.b.block,
	}).Result()
	if err != nil {
		return nil, err
	}
	var out []stream.Entry
	for _, s := range streams {
		for _, m := range s.Messages {
			e, err := entry(m)
			if err != nil {
				return nil, fmt.Errorf("%w: stream %s: %w", errMalformed, r.key, err)
			}
			out = append(out, e)
		}
	}
	return out, nil
}

// unblockRetry is how often unblock tries again while a read has not yet
// reached Redis.
const unblockRetry = 10 * time.Millisecond

// unblock runs once the subscription's context is done and wakes its XREAD,
// so that a cancelled subscription ends at once rather than after the block
// time. The read may not have reached Redis yet (CLIENT UNBLOCK then finds
// nothing blocked), so it tries again until the read has returned. It goes
// through the publishing client, whose pool subscriptions do not hold. An
// error reply (an ACL without CLIENT UNBLOCK) ends the tries: the read then
// ends when its block time does.
func (r *reader) unblock() {
	for r.reading.Load() {
		if id := r.id.Load(); id != 0 {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			n, err := r.b.client.ClientUnblock(ctx, id).Result()
			cancel()
			if err == nil && n == 1 {
				return
			}
			if err != nil && permanent(err) {
				r.b.log.Warn("bus: cannot unblock a cancelled read; it ends after the block time",
					"key", r.key, "block", r.b.block, "err", err)
				return
			}
		}
		t := r.b.clock.NewTimer(unblockRetry)
		<-t.C()
	}
}

func (r *reader) drop() {
	r.id.Store(0)
	if r.conn != nil {
		_ = r.conn.Close() // the connection already failed
		r.conn = nil
	}
}

func (r *reader) Close() {
	if r.stop != nil && !r.stop() {
		<-r.unblocked // the hook ran: wait for it to finish
	}
	r.drop()
}

var errMalformed = errors.New("bus: malformed stream entry")

// permanent reports whether retrying cannot help: an entry the bus did not
// write, a closed client, or an error reply from Redis other than the ones a
// starting or failing-over server gives.
func permanent(err error) bool {
	if errors.Is(err, errMalformed) || errors.Is(err, goredis.ErrClosed) {
		return true
	}
	var reply goredis.Error
	if !errors.As(err, &reply) {
		return false // the network, a pool timeout
	}
	for _, p := range []string{"LOADING", "BUSY", "TRYAGAIN", "MASTERDOWN", "READONLY", "max number of clients"} {
		if goredis.HasErrorPrefix(err, p) {
			return false
		}
	}
	return true
}

func entry(m goredis.XMessage) (stream.Entry, error) {
	id, err := parseRedisID(m.ID)
	if err != nil {
		return stream.Entry{}, err
	}
	p, ok1 := m.Values["p"].(string)
	d, ok2 := m.Values["d"].(string)
	if !ok1 || !ok2 {
		return stream.Entry{}, fmt.Errorf("entry %s has no p or d field", m.ID)
	}
	e := stream.Entry{ID: id, Data: []byte(d), Fresh: p == ""}
	if !e.Fresh {
		if e.Prev, err = parseRedisID(p); err != nil {
			return stream.Entry{}, err
		}
	}
	return e, nil
}

// redisID writes a bus ID as a stream ID: <seq>-<n>.
func redisID(id bus.ID) string {
	return strconv.FormatUint(id.Seq, 10) + "-" + strconv.FormatUint(id.N, 10)
}

func parseRedisID(s string) (bus.ID, error) {
	a, b, ok := strings.Cut(s, "-")
	seq, err1 := strconv.ParseUint(a, 10, 64)
	n, err2 := strconv.ParseUint(b, 10, 64)
	if !ok || err1 != nil || err2 != nil {
		return bus.ID{}, fmt.Errorf("stream id %q", s)
	}
	return bus.ID{Seq: seq, N: n}, nil
}
