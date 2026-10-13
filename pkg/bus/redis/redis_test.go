//go:build integration

package redis_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"

	"github.com/bsamanvith/agentkit/internal/testkit"
	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/bus/bustest"
	redisbus "github.com/bsamanvith/agentkit/pkg/bus/redis"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// Tests share one Redis (AGENTKIT_REDIS_URL): each bus writes under a prefix
// of its own and deletes its keys when its test ends. They never FLUSHDB.

func newClient(t *testing.T, url string, configure ...func(*goredis.Options)) *goredis.Client {
	t.Helper()
	opt, err := goredis.ParseURL(url)
	if err != nil {
		t.Fatalf("AGENTKIT_REDIS_URL: %v", err)
	}
	opt.ClientName = "agentkit-test-" + rand.Text()
	opt.MaintNotificationsConfig = &maintnotifications.Config{Mode: maintnotifications.ModeDisabled}
	for _, f := range configure {
		f(opt)
	}
	c := goredis.NewClient(opt)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestNewRejectsAutoMaintNotifications(t *testing.T) {
	t.Parallel()
	c := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1"}) // never dialled
	defer c.Close()
	if _, err := redisbus.New(c); !errors.Is(err, bus.ErrInvalid) {
		t.Errorf("New with go-redis's default options: err = %v, want ErrInvalid", err)
	}
	ok := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1", MaintNotificationsConfig: &maintnotifications.Config{Mode: maintnotifications.ModeDisabled}})
	defer ok.Close()
	if _, err := redisbus.New(ok, redisbus.WithSubscribeClient(c)); !errors.Is(err, bus.ErrInvalid) {
		t.Errorf("New with a default-options subscribe client: err = %v, want ErrInvalid", err)
	}
}

func newBus(t *testing.T, c *goredis.Client, opts ...redisbus.Option) *redisbus.Bus {
	t.Helper()
	prefix := "agentkit-test:" + rand.Text() + ":"
	opts = append([]redisbus.Option{redisbus.WithPrefix(prefix), redisbus.WithBackoff(10*time.Millisecond, 200*time.Millisecond)}, opts...)
	b, err := redisbus.New(c, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closed := make(chan struct{})
		go func() { _ = b.Close(); close(closed) }()
		select {
		case <-closed:
		case <-time.After(10 * time.Second):
			t.Error("Close did not return in 10s")
		}
		ctx := context.Background()
		keys, err := c.Keys(ctx, prefix+"*").Result()
		if err == nil && len(keys) > 0 {
			err = c.Del(ctx, keys...).Err()
		}
		if err != nil {
			t.Logf("deleting the test's keys: %v", err)
		}
	})
	return b
}

func TestConformance(t *testing.T) {
	t.Parallel()
	url := testkit.IntegrationEnv(t, "AGENTKIT_REDIS_URL")
	bustest.Run(t, func(t *testing.T, trim func(bus.Topic) bus.Trim) bustest.Backend {
		c := newClient(t, url)
		b := newBus(t, c, redisbus.WithTrim(trim))
		return bustest.Backend{
			Bus: b,
			// Expiry deletes the key, and the bus forgets the stream
			// when it expires.
			Lose: func(t *testing.T, topic bus.Topic) {
				if err := c.Del(t.Context(), b.Key(topic)).Err(); err != nil {
					t.Fatal(err)
				}
				redisbus.Forget(b, topic)
			},
		}
	})
}

// MaxAge is the stream's expiry, set by every publish.
func TestPublishSetsExpiry(t *testing.T) {
	t.Parallel()
	c := newClient(t, testkit.IntegrationEnv(t, "AGENTKIT_REDIS_URL"))
	const maxAge = time.Hour
	b := newBus(t, c, redisbus.WithTrim(func(bus.Topic) bus.Trim { return bus.Trim{MaxLen: 10, MaxAge: maxAge} }))
	topic := runTopic(t, "expiry")
	publish(t, b, topic, 1)
	ttl, err := c.PTTL(t.Context(), b.Key(topic)).Result()
	if err != nil {
		t.Fatal(err)
	}
	if ttl <= 0 || ttl > maxAge {
		t.Errorf("PTTL = %v, want in (0, %v]", ttl, maxAge)
	}
}

// A subscriber whose connection drops (killed, as in a restart or a network
// failure) reconnects and continues where it was.
func TestReconnect(t *testing.T) {
	t.Parallel()
	url := testkit.IntegrationEnv(t, "AGENTKIT_REDIS_URL")
	c := newClient(t, url)
	b := newBus(t, c)
	topic := runTopic(t, "reconnect")
	s := subscribe(t, b, topic, bus.ID{})
	publish(t, b, topic, 1)
	expect(t, s, 1)

	// The subscriber is now blocked in XREAD on its own connection. Kill
	// every connection of this test's client (not the other tests' on the
	// shared server).
	admin := newClient(t, url)
	list, err := admin.ClientList(t.Context()).Result()
	if err != nil {
		t.Fatal(err)
	}
	killed := 0
	for line := range strings.Lines(list) {
		fields := strings.Fields(line)
		if !slices.Contains(fields, "name="+c.Options().ClientName) {
			continue
		}
		for _, f := range fields {
			if id, ok := strings.CutPrefix(f, "id="); ok {
				if err := admin.Do(t.Context(), "CLIENT", "KILL", "ID", id).Err(); err != nil {
					t.Fatal(err)
				}
				killed++
			}
		}
	}
	if killed == 0 {
		t.Fatal("found no connection of the test's client to kill")
	}
	for i := range uint64(4) {
		publish(t, b, topic, i+2)
	}
	expect(t, s, 2, 3, 4, 5)
}

// Cancelling a subscription blocked in XREAD ends it long before the block
// time would.
func TestCancelUnblocksTheRead(t *testing.T) {
	t.Parallel()
	c := newClient(t, testkit.IntegrationEnv(t, "AGENTKIT_REDIS_URL"))
	b := newBus(t, c, redisbus.WithBlock(time.Minute))
	topic := runTopic(t, "cancel")
	ctx, cancel := context.WithCancel(t.Context())
	s, err := b.Subscribe(ctx, topic, bus.ID{})
	if err != nil {
		t.Fatal(err)
	}
	publish(t, b, topic, 1)
	expect(t, s, 1) // the reader is now blocked waiting for the next message
	cancel()
	select {
	case _, ok := <-s.C():
		if ok {
			t.Fatal("a message after cancel")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancel did not end a blocked read within 10s (block time 1m)")
	}
	if !errors.Is(s.Err(), context.Canceled) {
		t.Errorf("Err = %v, want context.Canceled", s.Err())
	}
}

// A cancel that lands after a read's last context check, before its XREAD
// reaches Redis, finds nothing to unblock. The cancel hook keeps trying until
// the read returns, so the subscription still ends long before the block time.
func TestCancelRacingTheRead(t *testing.T) {
	t.Parallel()
	c := newClient(t, testkit.IntegrationEnv(t, "AGENTKIT_REDIS_URL"))
	clk := clocktest.NewFake(time.Now())
	b := newBus(t, c, redisbus.WithBlock(time.Minute), redisbus.WithClock(clk))
	ctx, cancel := context.WithCancel(t.Context())
	var once sync.Once
	redisbus.SetBeforeXRead(b, func() {
		once.Do(func() {
			cancel()
			// The hook's first CLIENT UNBLOCK found nothing blocked: wait
			// until it waits to try again (a hook that gives up never does).
			wctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
			defer stop()
			_ = clk.WaitForTimers(wctx, 1)
		})
	})
	s, err := b.Subscribe(ctx, runTopic(t, "race"), bus.ID{})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(10 * time.Second)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case _, ok := <-s.C():
			if ok {
				t.Fatal("a message after cancel")
			}
			if !errors.Is(s.Err(), context.Canceled) {
				t.Errorf("Err = %v, want context.Canceled", s.Err())
			}
			return
		case <-tick.C:
			clk.Advance(10 * time.Millisecond) // the hook's retries
		case <-deadline:
			t.Fatal("a cancel racing the XREAD did not end it within 10s (block time 1m)")
		}
	}
}

// A publish that finds its own ID already last on the stream (go-redis
// retried a script whose reply was lost, or an earlier attempt landed) gets
// ErrStale, and the bus remembers the message as published: if Redis then
// loses it, the next message is a gap, not a silent skip.
func TestStaleOwnIDIsRemembered(t *testing.T) {
	t.Parallel()
	c := newClient(t, testkit.IntegrationEnv(t, "AGENTKIT_REDIS_URL"))
	b := newBus(t, c)
	topic := runTopic(t, "stale-own")
	key := b.Key(topic)
	// Another bus on the same keys stands in for the attempt that landed.
	other, err := redisbus.New(c, redisbus.WithPrefix(strings.TrimSuffix(key, topic.String())))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	publish(t, other, topic, 1)
	if err := b.Publish(t.Context(), topic, bus.Message{ID: bus.ID{Seq: 1}, Data: []byte("m1")}); !errors.Is(err, bus.ErrStale) {
		t.Fatalf("publish 1 again: err = %v, want ErrStale", err)
	}
	if err := c.Del(t.Context(), key).Err(); err != nil { // Redis loses it
		t.Fatal(err)
	}
	s := subscribe(t, b, topic, bus.ID{})
	publish(t, b, topic, 2)
	if err := end(t, s); !errors.Is(err, bus.ErrGap) {
		t.Errorf("from the start after 1 was lost: err = %v, want ErrGap", err)
	}
}

// A stream that expired starts afresh: the bus forgets what it published
// there when the stream expires, so the next message is a restart point, not
// a gap.
func TestExpiredStreamRestarts(t *testing.T) {
	t.Parallel()
	c := newClient(t, testkit.IntegrationEnv(t, "AGENTKIT_REDIS_URL"))
	clk := clocktest.NewFake(time.Now())
	const maxAge = 100 * time.Millisecond
	b := newBus(t, c, redisbus.WithClock(clk), redisbus.WithBlock(time.Second),
		redisbus.WithTrim(func(bus.Topic) bus.Trim { return bus.Trim{MaxLen: 10, MaxAge: maxAge} }))
	topic := runTopic(t, "expired")
	publish(t, b, topic, 1)
	testkit.Eventually(t, 10*time.Second, func() bool { return c.Exists(t.Context(), b.Key(topic)).Val() == 0 })
	clk.Advance(maxAge)
	s := subscribe(t, b, topic, bus.ID{})
	// clk also times the retries of unblocking a cancelled read. When the
	// test ends (cancelling s), keep it moving until s has ended: a cancel
	// that races the start of a read otherwise waits on it forever, and so
	// does Close.
	t.Cleanup(func() { _ = endAdvancing(t, s, clk) })
	publish(t, b, topic, 2)
	expect(t, s, 2)
}

// Subscriptions on a client of their own leave the publishing client's pool
// alone: with their pool of 2 held, publishes go through, and subscriptions
// waiting for a connection get one when others end.
func TestSubscribeClient(t *testing.T) {
	t.Parallel()
	url := testkit.IntegrationEnv(t, "AGENTKIT_REDIS_URL")
	// Both pools hold 2: subscriptions on the publishing client would
	// starve the publishes below.
	small := func(opt *goredis.Options) { opt.PoolSize, opt.PoolTimeout = 2, 100*time.Millisecond }
	b := newBus(t, newClient(t, url, small), redisbus.WithSubscribeClient(newClient(t, url, small)))
	topic := runTopic(t, "subscribe-client")
	var subs []bus.Subscription
	var cancels []context.CancelFunc
	for range 4 {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		s, err := b.Subscribe(ctx, topic, bus.ID{})
		if err != nil {
			t.Fatal(err)
		}
		subs, cancels = append(subs, s), append(cancels, cancel)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	for i := range uint64(20) {
		if err := b.Publish(ctx, topic, bus.Message{ID: bus.ID{Seq: i + 1}, Data: fmt.Appendf(nil, "m%d", i+1)}); err != nil {
			t.Fatalf("publish %d with the subscribe pool full: %v", i+1, err)
		}
	}
	// Two of the four hold the pool and read; end those, and the two that
	// were waiting for a connection get one and read too.
	first := make(chan int, len(subs))
	for i, s := range subs {
		go func() {
			if m, ok := <-s.C(); ok && m.ID == (bus.ID{Seq: 1}) {
				first <- i
			}
		}()
	}
	read := map[int]bool{}
	for range 2 {
		select {
		case i := <-first:
			read[i] = true
			cancels[i]()
		case <-time.After(10 * time.Second):
			t.Fatal("no subscription read within 10s")
		}
	}
	for range 2 {
		select {
		case i := <-first:
			read[i] = true
		case <-time.After(10 * time.Second):
			t.Fatalf("only %d of 4 subscriptions read within 10s of two ending", len(read))
		}
	}
}

// A cancelled read that Redis refuses to unblock (an ACL without CLIENT
// UNBLOCK) ends after the block time, and the bus says so once instead of
// retrying the refused command until then.
func TestUnblockRefused(t *testing.T) {
	t.Parallel()
	raw := testkit.IntegrationEnv(t, "AGENTKIT_REDIS_URL")
	admin := newClient(t, raw)
	user, pass := "agentkit-test-"+rand.Text(), rand.Text()
	if err := admin.Do(t.Context(), "ACL", "SETUSER", user, "on", ">"+pass, "~*", "&*", "+@all", "-client|unblock").Err(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Do(context.Background(), "ACL", "DELUSER", user).Err() })
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(user, pass)
	logs := &records{}
	b := newBus(t, newClient(t, u.String()), redisbus.WithBlock(2*time.Second), redisbus.WithLogger(slog.New(logs)))
	topic := runTopic(t, "unblock-refused")
	ctx, cancel := context.WithCancel(t.Context())
	s, err := b.Subscribe(ctx, topic, bus.ID{})
	if err != nil {
		t.Fatal(err)
	}
	publish(t, b, topic, 1)
	expect(t, s, 1)
	// Cancel only once the reader is blocked in XREAD again, so that only
	// CLIENT UNBLOCK, refused, could end the read early.
	testkit.Eventually(t, 10*time.Second, func() bool {
		list, err := admin.ClientList(t.Context()).Result()
		if err != nil {
			t.Fatal(err)
		}
		for line := range strings.Lines(list) {
			if strings.Contains(line, " user="+user+" ") && strings.Contains(line, " flags=b ") {
				return true
			}
		}
		return false
	})
	cancel()
	select {
	case _, ok := <-s.C():
		if ok {
			t.Fatal("a message after cancel")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancel did not end the read within 10s (block time 2s)")
	}
	if n := logs.count("bus: cannot unblock"); n != 1 {
		t.Errorf("logged the refused unblock %d times, want once", n)
	}
}

// records is a slog.Handler that keeps every record's message.
type records struct {
	mu   sync.Mutex
	msgs []string
}

func (r *records) Enabled(context.Context, slog.Level) bool { return true }
func (r *records) WithAttrs([]slog.Attr) slog.Handler       { return r }
func (r *records) WithGroup(string) slog.Handler            { return r }

func (r *records) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, rec.Message)
	return nil
}

func (r *records) count(prefix string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, m := range r.msgs {
		if strings.HasPrefix(m, prefix) {
			n++
		}
	}
	return n
}

// Restarts of a real server (AGENTKIT_REDIS_SERVER: the redis-server binary,
// version 7 or later). With its data kept, a subscriber continues where it
// was; after Redis restored an older snapshot, it gets ErrGap rather than
// silently missing the writes the snapshot lost; trimmed history is ErrGap;
// and a publish that failed while Redis was down is ErrGap for a subscriber
// past it, unless the publisher retried it.
func TestRestart(t *testing.T) {
	t.Parallel()
	server := startServer(t, testkit.IntegrationEnv(t, "AGENTKIT_REDIS_SERVER"))
	c := newClient(t, server.url)
	b := newBus(t, c, redisbus.WithTrim(func(bus.Topic) bus.Trim { return bus.Trim{MaxLen: 10, MaxAge: time.Hour} }))
	topic := runTopic(t, "restart")

	live := subscribe(t, b, topic, bus.ID{})
	for i := range uint64(5) {
		publish(t, b, topic, i+1)
	}
	expect(t, live, 1, 2, 3, 4, 5)

	// Data kept: the snapshot has 1-5.
	server.restart(t, "SAVE")
	for i := range uint64(5) {
		publish(t, b, topic, i+6)
	}
	expect(t, live, 6, 7, 8, 9, 10)
	resumed := subscribe(t, b, topic, bus.ID{Seq: 7})
	expect(t, resumed, 8, 9, 10)

	// Data lost: Redis comes back with the older snapshot (1-5), without
	// 6-10. 11 follows 10, the last this bus published: the subscribers that
	// got 10 miss nothing and go on; one before it, or reading from the
	// start, gets ErrGap after 5.
	server.restart(t, "NOSAVE")
	behind := subscribe(t, b, topic, bus.ID{Seq: 7})
	fromStart := subscribe(t, b, topic, bus.ID{})
	expect(t, fromStart, 1, 2, 3, 4, 5)
	publish(t, b, topic, 11)
	expect(t, live, 11)
	expect(t, resumed, 11)
	for name, s := range map[string]bus.Subscription{"behind": behind, "from the start": fromStart} {
		if err := end(t, s); !errors.Is(err, bus.ErrGap) {
			t.Errorf("%s: err = %v, want ErrGap", name, err)
		}
	}

	// Trimmed: MAXLEN ~ 10 drops whole nodes of 100.
	for i := range uint64(600) {
		publish(t, b, topic, i+12)
	}
	if err := end(t, subscribe(t, b, topic, bus.ID{Seq: 11})); !errors.Is(err, bus.ErrGap) {
		t.Errorf("after trimmed history: err = %v, want ErrGap", err)
	}
	expect(t, subscribe(t, b, topic, bus.ID{Seq: 609}), 610, 611)

	// Failed and not retried: 612 is lost while Redis is down, and 613 must
	// not silently follow 611.
	past := subscribe(t, b, topic, bus.ID{Seq: 611})
	server.stop(t, "SAVE")
	failPublish(t, b, topic, 612)
	server.start(t)
	publish(t, b, topic, 613)
	if err := end(t, past); !errors.Is(err, bus.ErrGap) {
		t.Errorf("after a failed publish: err = %v, want ErrGap", err)
	}

	// Failed, then retried: the stream continues with no gap.
	cont := subscribe(t, b, topic, bus.ID{Seq: 613})
	server.stop(t, "SAVE")
	failPublish(t, b, topic, 614)
	server.start(t)
	publish(t, b, topic, 614)
	publish(t, b, topic, 615)
	expect(t, cont, 614, 615)
}

// failPublish publishes seq while Redis is down, which must fail.
func failPublish(t *testing.T, b bus.Bus, topic bus.Topic, seq uint64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err := b.Publish(ctx, topic, bus.Message{ID: bus.ID{Seq: seq}, Data: fmt.Appendf(nil, "m%d", seq)})
	if err == nil || errors.Is(err, bus.ErrStale) {
		t.Fatalf("publish %d with Redis down: err = %v, want a connection error", seq, err)
	}
}

type server struct {
	bin, dir, port, url string
	cmd                 *exec.Cmd
}

func startServer(t *testing.T, bin string) *server {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	_ = l.Close()
	s := &server{bin: bin, dir: t.TempDir(), port: port, url: "redis://127.0.0.1:" + port + "/0"}
	s.start(t)
	t.Cleanup(func() {
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
	})
	return s
}

func (s *server) start(t *testing.T) {
	t.Helper()
	// No automatic snapshots: the test decides what each restart keeps.
	s.cmd = exec.Command(s.bin, "--port", s.port, "--bind", "127.0.0.1", "--dir", s.dir, "--save", "", "--appendonly", "no")
	if err := s.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:" + s.port, MaxRetries: -1, MaintNotificationsConfig: &maintnotifications.Config{Mode: maintnotifications.ModeDisabled}})
	defer c.Close()
	testkit.Eventually(t, 10*time.Second, func() bool { return c.Ping(context.Background()).Err() == nil })
}

// restart stops the server and starts it again on the same port.
func (s *server) restart(t *testing.T, mode string) {
	t.Helper()
	s.stop(t, mode)
	s.start(t)
}

// stop shuts the server down: SHUTDOWN SAVE keeps the data, NOSAVE drops what
// is not in the last snapshot.
func (s *server) stop(t *testing.T, mode string) {
	t.Helper()
	c := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:" + s.port, MaxRetries: -1, MaintNotificationsConfig: &maintnotifications.Config{Mode: maintnotifications.ModeDisabled}})
	_ = c.Do(context.Background(), "SHUTDOWN", mode).Err() // the connection drops
	_ = c.Close()
	if err := s.cmd.Wait(); err != nil {
		t.Logf("redis-server exited: %v", err)
	}
}

func runTopic(t *testing.T, run string) bus.Topic {
	t.Helper()
	acme, err := tenancy.NewTenant("acme")
	if err != nil {
		t.Fatal(err)
	}
	topic, err := bus.RunTopic(acme, run)
	if err != nil {
		t.Fatal(err)
	}
	return topic
}

// publish publishes seq, retrying while Redis is coming back.
func publish(t *testing.T, b bus.Bus, topic bus.Topic, seq uint64) {
	t.Helper()
	var err error
	testkit.Eventually(t, 10*time.Second, func() bool {
		err = b.Publish(t.Context(), topic, bus.Message{ID: bus.ID{Seq: seq}, Data: fmt.Appendf(nil, "m%d", seq)})
		return err == nil || errors.Is(err, bus.ErrStale)
	})
	if err != nil && !errors.Is(err, bus.ErrStale) { // stale: an attempt landed
		t.Fatalf("publish %d: %v", seq, err)
	}
}

func subscribe(t *testing.T, b bus.Bus, topic bus.Topic, after bus.ID) bus.Subscription {
	t.Helper()
	s, err := b.Subscribe(t.Context(), topic, after)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func expect(t *testing.T, s bus.Subscription, seqs ...uint64) {
	t.Helper()
	for _, seq := range seqs {
		select {
		case m, ok := <-s.C():
			if !ok {
				t.Fatalf("subscription ended before %d: %v", seq, s.Err())
			}
			if m.ID != (bus.ID{Seq: seq}) || string(m.Data) != fmt.Sprintf("m%d", seq) {
				t.Fatalf("got %s %q, want %d", m.ID, m.Data, seq)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("no message %d within 10s", seq)
		}
	}
}

// endAdvancing is end for a bus on a fake clock: it moves clk on while it
// waits, for the cancel hook's unblock retries.
func endAdvancing(t *testing.T, s bus.Subscription, clk *clocktest.Fake) error {
	t.Helper()
	timeout := time.After(10 * time.Second)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case m, ok := <-s.C():
			if !ok {
				return s.Err()
			}
			t.Errorf("unexpected message %s before the end", m.ID)
		case <-tick.C:
			clk.Advance(10 * time.Millisecond)
		case <-timeout:
			t.Fatal("subscription did not end within 10s")
			return nil
		}
	}
}

func end(t *testing.T, s bus.Subscription) error {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case m, ok := <-s.C():
			if !ok {
				return s.Err()
			}
			t.Errorf("unexpected message %s before the end", m.ID)
		case <-timeout:
			t.Fatal("subscription did not end within 10s")
			return nil
		}
	}
}
