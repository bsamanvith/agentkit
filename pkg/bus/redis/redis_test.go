//go:build integration

package redis_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
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

func newClient(t *testing.T, url string) *goredis.Client {
	t.Helper()
	opt, err := goredis.ParseURL(url)
	if err != nil {
		t.Fatalf("AGENTKIT_REDIS_URL: %v", err)
	}
	opt.ClientName = "agentkit-test-" + rand.Text()
	opt.MaintNotificationsConfig = &maintnotifications.Config{Mode: maintnotifications.ModeDisabled}
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
			// Expiry deletes the key, as does a restart without the data.
			Lose: func(t *testing.T, topic bus.Topic) {
				if err := c.Del(t.Context(), b.Key(topic)).Err(); err != nil {
					t.Fatal(err)
				}
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

	// Data lost: Redis comes back with the older snapshot (1-5), without 6-10.
	server.restart(t, "NOSAVE")
	behind := subscribe(t, b, topic, bus.ID{Seq: 7})
	publish(t, b, topic, 11)
	for name, s := range map[string]bus.Subscription{"live": live, "resumed": resumed, "behind": behind} {
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
	if err != nil {
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
