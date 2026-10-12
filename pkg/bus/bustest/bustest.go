// Package bustest holds the conformance suite every [bus.Bus] passes.
//
// Every subscription in the suite goes through one helper that checks the
// bus's invariants on each delivery, whatever the test is about:
//
//   - a subscriber receives exactly the messages published after its
//     position, in order, each one the next published after the one before:
//     never a skip, a repeat or a reorder (a gap ends the subscription with
//     [bus.ErrGap] instead);
//   - each message has the data it was published with, though the publisher
//     and every subscriber scribble on the slices they handled;
//   - a subscription ends by closing its channel, and only then has an Err.
package bustest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// Backend is one bus under test.
type Backend struct {
	// Bus is empty and trimmed by the policy it was made with. The suite
	// closes it.
	Bus bus.Bus
	// Lose drops all of a topic's history as when it expires: the backend
	// and every publisher forget it, and the next publish starts the topic
	// afresh. (A backend that loses data while its publisher still
	// remembers what it published can do better, and tests that itself.)
	Lose func(t *testing.T, topic bus.Topic)
}

// NewBackend makes a [Backend] whose bus trims topics by trim.
type NewBackend func(t *testing.T, trim func(bus.Topic) bus.Trim) Backend

// wait bounds every wait for something that must happen, so that a broken
// bus fails a test instead of hanging it.
const wait = 10 * time.Second

// Run runs the conformance suite against the backend newBackend makes.
func Run(t *testing.T, newBackend NewBackend) {
	t.Run("Order", func(t *testing.T) { t.Parallel(); testOrder(t, newBackend) })
	t.Run("ResumeAfterAnID", func(t *testing.T) { t.Parallel(); testResume(t, newBackend) })
	t.Run("ResumeBeforeTheFirstPublish", func(t *testing.T) { t.Parallel(); testResumeBeforeFirst(t, newBackend) })
	t.Run("FanOut", func(t *testing.T) { t.Parallel(); testFanOut(t, newBackend) })
	t.Run("TrimmedHistoryIsAGap", func(t *testing.T) { t.Parallel(); testTrimmed(t, newBackend) })
	t.Run("SlowSubscriberBlocksNobody", func(t *testing.T) { t.Parallel(); testSlow(t, newBackend) })
	t.Run("LostHistoryIsAGap", func(t *testing.T) { t.Parallel(); testLost(t, newBackend) })
	t.Run("FailedPublishIsAGap", func(t *testing.T) { t.Parallel(); testFailed(t, newBackend) })
	t.Run("Last", func(t *testing.T) { t.Parallel(); testLast(t, newBackend) })
	t.Run("StaleIDs", func(t *testing.T) { t.Parallel(); testStale(t, newBackend) })
	t.Run("InvalidArguments", func(t *testing.T) { t.Parallel(); testInvalid(t, newBackend) })
	t.Run("Cancel", func(t *testing.T) { t.Parallel(); testCancel(t, newBackend) })
	t.Run("Close", func(t *testing.T) { t.Parallel(); testClose(t, newBackend) })
	t.Run("TenantSafeTopics", func(t *testing.T) { t.Parallel(); testTenants(t, newBackend) })
}

// roomy keeps everything the tests publish.
func roomy(bus.Topic) bus.Trim { return bus.Trim{MaxLen: 10000, MaxAge: time.Hour} }

// tight keeps few messages; Redis (MAXLEN ~) trims only whole nodes of 100,
// so the tests that need a trim publish several hundred.
func tight(bus.Topic) bus.Trim { return bus.Trim{MaxLen: 10, MaxAge: time.Hour} }

func testOrder(t *testing.T, nb NewBackend) {
	h := newHarness(t, nb, roomy)
	topic := runTopic(t, "acme", "order")
	ids := []bus.ID{id(1, 0), id(1, 1), id(1, 2), id(2, 0), id(3, 0), id(3, 7), id(10, 0), id(1<<62, 1<<62)}
	for _, id := range ids {
		h.publish(topic, id)
	}
	h.publishData(topic, bus.ID{Seq: 1<<62 + 1}, nil)
	h.publishData(topic, bus.ID{Seq: 1<<62 + 2}, []byte{0, 0xff, '\n', 0})

	s := h.subscribe(t.Context(), topic, bus.ID{})
	for range len(ids) + 2 {
		s.next()
	}
}

func testResume(t *testing.T, nb NewBackend) {
	h := newHarness(t, nb, roomy)
	topic := runTopic(t, "acme", "resume")
	for i := range uint64(10) {
		h.publish(topic, bus.ID{Seq: i + 1})
	}

	// After a published ID, and after one between two published IDs.
	for _, after := range []bus.ID{id(4, 0), id(4, 5)} {
		s := h.subscribe(t.Context(), topic, after)
		for range 6 {
			s.next()
		}
	}

	// After the last one, and after one past it: then live.
	last := h.subscribe(t.Context(), topic, bus.ID{Seq: 10})
	past := h.subscribe(t.Context(), topic, bus.ID{Seq: 100})
	h.publish(topic, bus.ID{Seq: 11})
	last.next()
	h.publish(topic, bus.ID{Seq: 101})
	if got := last.next(); got.ID != (bus.ID{Seq: 101}) {
		t.Errorf("after 10 got %s second, want 101", got.ID)
	}
	if got := past.next(); got.ID != (bus.ID{Seq: 101}) {
		t.Errorf("after 100 got %s first, want 101", got.ID)
	}
}

// A reader that replayed the store before the publisher's first publish (the
// store is written first) waits, then continues with no gap.
func testResumeBeforeFirst(t *testing.T, nb NewBackend) {
	h := newHarness(t, nb, roomy)
	topic := runTopic(t, "acme", "early")
	s := h.subscribe(t.Context(), topic, bus.ID{Seq: 2})
	for i := range uint64(4) {
		h.publish(topic, bus.ID{Seq: i + 1})
	}
	s.next() // 3
	s.next() // 4
}

func testFanOut(t *testing.T, nb NewBackend) {
	h := newHarness(t, nb, roomy)
	topic := runTopic(t, "acme", "fanout")
	var subs []*sub
	for i := range uint64(40) {
		if i%4 == 0 {
			// Subscribers join at different points: from the start, and
			// from the message just published.
			subs = append(subs, h.subscribe(t.Context(), topic, bus.ID{}), h.subscribe(t.Context(), topic, bus.ID{Seq: i}))
		}
		h.publish(topic, bus.ID{Seq: i + 1})
	}
	// Each subscriber is read on its own, after all the publishes: none of
	// them held up the publisher or the others.
	for _, s := range subs {
		for !s.caughtUp() {
			s.next()
		}
	}
}

func testTrimmed(t *testing.T, nb NewBackend) {
	h := newHarness(t, nb, tight)
	topic := runTopic(t, "acme", "trimmed")
	for i := range uint64(600) {
		h.publish(topic, bus.ID{Seq: i + 1})
	}
	for _, after := range []bus.ID{{}, {Seq: 1}, {Seq: 300}} {
		s := h.subscribe(t.Context(), topic, after)
		if err := s.end(); !errors.Is(err, bus.ErrGap) {
			t.Errorf("after %s: err = %v, want ErrGap", after, err)
		}
		if len(s.got) != 0 {
			t.Errorf("after %s: got %d messages before the gap, want none", after, len(s.got))
		}
	}
	// The newest messages are kept.
	s := h.subscribe(t.Context(), topic, bus.ID{Seq: 595})
	for range 5 {
		s.next()
	}
	h.publish(topic, bus.ID{Seq: 601})
	s.next()
}

// A subscriber that stops reading never slows the publisher or another
// subscriber: the publisher only appends to the bounded history, and the
// stalled subscriber finds a gap when it reads again.
func testSlow(t *testing.T, nb NewBackend) {
	h := newHarness(t, nb, tight)
	topic := runTopic(t, "acme", "slow")
	stalled := h.subscribe(t.Context(), topic, bus.ID{})
	fast := h.subscribe(t.Context(), topic, bus.ID{})
	h.publish(topic, bus.ID{Seq: 1})
	stalled.next()
	for i := range uint64(600) {
		h.publish(topic, bus.ID{Seq: i + 2}) // fails the test if it blocks
		fast.next()
	}
	if err := stalled.end(); !errors.Is(err, bus.ErrGap) {
		t.Errorf("stalled subscriber: err = %v after %d messages, want ErrGap", err, len(stalled.got))
	}
}

func testLost(t *testing.T, nb NewBackend) {
	h := newHarness(t, nb, roomy)
	topic := runTopic(t, "acme", "lost")
	for i := range uint64(5) {
		h.publish(topic, bus.ID{Seq: i + 1})
	}
	live := h.subscribe(t.Context(), topic, bus.ID{})
	for range 5 {
		live.next()
	}
	h.lose(topic)
	// Waits on the missing topic; the first publish after the loss tells
	// it messages are missing.
	resumed := h.subscribe(t.Context(), topic, bus.ID{Seq: 3})
	h.publish(topic, bus.ID{Seq: 6})
	for _, s := range []*sub{live, resumed} {
		if err := s.end(); !errors.Is(err, bus.ErrGap) {
			t.Errorf("after %s: err = %v, want ErrGap", s.after, err)
		}
	}
	// Reading from the start sees what the topic has now.
	fresh := h.subscribe(t.Context(), topic, bus.ID{})
	fresh.next()
}

// A publish that failed (here its context is done) is a gap for a subscriber
// past the message before it, or from the start when it was the first,
// unless the publisher publishes it again.
func testFailed(t *testing.T, nb NewBackend) {
	h := newHarness(t, nb, roomy)
	topic := runTopic(t, "acme", "failed")
	done, cancel := context.WithCancel(t.Context())
	cancel()
	fail := func(topic bus.Topic, id bus.ID) {
		t.Helper()
		if err := h.b.Publish(done, topic, bus.Message{ID: id}); !errors.Is(err, context.Canceled) {
			t.Fatalf("publish %s with a done context: err = %v, want context.Canceled", id, err)
		}
	}

	first := runTopic(t, "acme", "failed-first")
	fail(first, id(1, 0))
	h.publish(first, id(2, 0))
	if err := h.subscribe(t.Context(), first, bus.ID{}).end(); !errors.Is(err, bus.ErrGap) {
		t.Errorf("from the start after a failed first publish: err = %v, want ErrGap", err)
	}

	h.publish(topic, id(1, 0))
	past := h.subscribe(t.Context(), topic, id(1, 0))
	fail(topic, id(2, 0))
	h.publish(topic, id(3, 0))
	if err := past.end(); !errors.Is(err, bus.ErrGap) {
		t.Errorf("after a failed publish: err = %v, want ErrGap", err)
	}

	cont := h.subscribe(t.Context(), topic, id(3, 0))
	fail(topic, id(4, 0))
	h.publish(topic, id(4, 0)) // published again: no gap
	h.publish(topic, id(5, 0))
	cont.next()
	cont.next()
}

// Last is the newest kept message's ID, zero on a topic with none, and a
// subscription after it delivers what comes next, even once the history's
// start was trimmed (where one from the start gets ErrGap).
func testLast(t *testing.T, nb NewBackend) {
	h := newHarness(t, nb, tight)
	topic := runTopic(t, "acme", "last")
	last := func() bus.ID {
		t.Helper()
		id, err := h.b.Last(t.Context(), topic)
		if err != nil {
			t.Fatalf("Last: %v", err)
		}
		return id
	}
	if got := last(); !got.IsZero() {
		t.Errorf("Last of a topic with no message = %s, want zero", got)
	}
	for i := range uint64(600) {
		h.publish(topic, id(i+1, 0))
	}
	got := last()
	if got != id(600, 0) {
		t.Fatalf("Last = %s, want 600", got)
	}
	s := h.subscribe(t.Context(), topic, got)
	h.publish(topic, id(601, 0))
	s.next()
	if got := last(); got != id(601, 0) {
		t.Errorf("Last = %s, want 601", got)
	}
	h.lose(topic)
	if got := last(); !got.IsZero() {
		t.Errorf("Last of a lost topic = %s, want zero", got)
	}
}

func testStale(t *testing.T, nb NewBackend) {
	h := newHarness(t, nb, roomy)
	topic := runTopic(t, "acme", "stale")
	h.publish(topic, bus.ID{Seq: 5})
	for _, stale := range []bus.ID{id(5, 0), id(4, 0), id(4, 9)} {
		if err := h.b.Publish(t.Context(), topic, bus.Message{ID: stale, Data: []byte("x")}); !errors.Is(err, bus.ErrStale) {
			t.Errorf("publish %s after 5: err = %v, want ErrStale", stale, err)
		}
	}
	h.publish(topic, bus.ID{Seq: 5, N: 1})
	s := h.subscribe(t.Context(), topic, bus.ID{})
	s.next()
	s.next()
}

func testInvalid(t *testing.T, nb NewBackend) {
	h := newHarness(t, nb, roomy)
	topic := runTopic(t, "acme", "invalid")
	if err := h.b.Publish(t.Context(), bus.Topic{}, bus.Message{ID: bus.ID{Seq: 1}}); !errors.Is(err, bus.ErrInvalid) {
		t.Errorf("publish to the zero topic: err = %v, want ErrInvalid", err)
	}
	if err := h.b.Publish(t.Context(), topic, bus.Message{}); !errors.Is(err, bus.ErrInvalid) {
		t.Errorf("publish the zero id: err = %v, want ErrInvalid", err)
	}
	if _, err := h.b.Last(t.Context(), bus.Topic{}); !errors.Is(err, bus.ErrInvalid) {
		t.Errorf("last of the zero topic: err = %v, want ErrInvalid", err)
	}
	if _, err := h.b.Subscribe(t.Context(), bus.Topic{}, bus.ID{}); !errors.Is(err, bus.ErrInvalid) {
		t.Errorf("subscribe to the zero topic: err = %v, want ErrInvalid", err)
	}
	none := newHarness(t, nb, func(bus.Topic) bus.Trim { return bus.Trim{} })
	if err := none.b.Publish(t.Context(), topic, bus.Message{ID: bus.ID{Seq: 1}}); !errors.Is(err, bus.ErrInvalid) {
		t.Errorf("publish with no trim limits: err = %v, want ErrInvalid", err)
	}
}

func testCancel(t *testing.T, nb NewBackend) {
	h := newHarness(t, nb, roomy)
	topic := runTopic(t, "acme", "cancel")

	// Waiting for a message.
	ctx, cancel := context.WithCancel(t.Context())
	idle := h.subscribe(ctx, topic, bus.ID{})
	cancel()
	if err := idle.end(); !errors.Is(err, context.Canceled) {
		t.Errorf("idle subscriber: err = %v, want context.Canceled", err)
	}

	// Holding messages nobody reads.
	for i := range uint64(20) {
		h.publish(topic, bus.ID{Seq: i + 1})
	}
	ctx, cancel = context.WithCancel(t.Context())
	full := h.subscribe(ctx, topic, bus.ID{})
	full.next()
	cancel()
	if err := full.end(); !errors.Is(err, context.Canceled) {
		t.Errorf("unread subscriber: err = %v, want context.Canceled", err)
	}

	// Already done.
	if err := h.b.Publish(ctx, topic, bus.Message{ID: bus.ID{Seq: 100}}); !errors.Is(err, context.Canceled) {
		t.Errorf("publish with a done context: err = %v, want context.Canceled", err)
	}
	if s, err := h.b.Subscribe(ctx, topic, bus.ID{}); err == nil {
		late := h.track(topic, bus.ID{}, s)
		if err := late.end(); !errors.Is(err, context.Canceled) {
			t.Errorf("subscribe with a done context: err = %v, want context.Canceled", err)
		}
	} else if !errors.Is(err, context.Canceled) {
		t.Errorf("subscribe with a done context: err = %v, want context.Canceled", err)
	}
}

func testClose(t *testing.T, nb NewBackend) {
	h := newHarness(t, nb, roomy)
	topic := runTopic(t, "acme", "close")
	h.publish(topic, bus.ID{Seq: 1})
	subs := []*sub{
		h.subscribe(t.Context(), topic, bus.ID{}), // holds a message nobody reads
		h.subscribe(t.Context(), topic, id(1, 0)), // waits
	}
	if err := closeBus(t, h.b); err != nil {
		t.Fatalf("Close: %v", err)
	}
	for _, s := range subs {
		if err := s.end(); !errors.Is(err, bus.ErrClosed) {
			t.Errorf("after %s: err = %v, want ErrClosed", s.after, err)
		}
	}
	if err := h.b.Publish(t.Context(), topic, bus.Message{ID: bus.ID{Seq: 2}}); !errors.Is(err, bus.ErrClosed) {
		t.Errorf("publish after Close: err = %v, want ErrClosed", err)
	}
	if _, err := h.b.Subscribe(t.Context(), topic, bus.ID{}); !errors.Is(err, bus.ErrClosed) {
		t.Errorf("subscribe after Close: err = %v, want ErrClosed", err)
	}
	if _, err := h.b.Last(t.Context(), topic); !errors.Is(err, bus.ErrClosed) {
		t.Errorf("last after Close: err = %v, want ErrClosed", err)
	}
	if err := closeBus(t, h.b); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// Topics of different tenants, kinds and ids never share messages, including
// ids that contain the separator.
func testTenants(t *testing.T, nb NewBackend) {
	h := newHarness(t, nb, roomy)
	acme, globex := tenant(t, "acme"), tenant(t, "globex")
	topics := []bus.Topic{
		must(bus.RunTopic(acme, "r1")),
		must(bus.RunTopic(globex, "r1")),
		must(bus.CommandTopic(acme, "r1")),
		must(bus.RunTopic(acme, "r1:cmd")),
		must(bus.RunTopic(acme, "u")),
		must(bus.UserTopic(acme, "u")),
		must(bus.UserTopic(globex, "u")),
		must(bus.UserTopic(acme, "u:x")),
		must(bus.UserTopic(acme, "u%3Ax")),
	}
	var subs []*sub
	for _, topic := range topics {
		subs = append(subs, h.subscribe(t.Context(), topic, bus.ID{}))
	}
	// One message per topic, all with the same ID: a shared stream would
	// reject the second as stale, and a shared reader would see another
	// topic's message first.
	for _, topic := range topics {
		h.publish(topic, bus.ID{Seq: 1})
	}
	for _, s := range subs {
		s.next()
	}
}

func tenant(t *testing.T, id string) tenancy.Tenant {
	t.Helper()
	tn, err := tenancy.NewTenant(id)
	if err != nil {
		t.Fatal(err)
	}
	return tn
}

func runTopic(t *testing.T, tenantID, runID string) bus.Topic {
	t.Helper()
	topic, err := bus.RunTopic(tenant(t, tenantID), runID)
	if err != nil {
		t.Fatal(err)
	}
	return topic
}

func id(seq, n uint64) bus.ID { return bus.ID{Seq: seq, N: n} }

func must(topic bus.Topic, err error) bus.Topic {
	if err != nil {
		panic(err)
	}
	return topic
}

// harness is one test's bus and the record of everything published to it.
type harness struct {
	t      *testing.T
	b      bus.Bus
	loseFn func(*testing.T, bus.Topic)

	mu sync.Mutex
	// log is every message published to each topic, in order, across losses.
	log map[string][]bus.Message
	// starts are the indexes in log where a topic started afresh.
	starts map[string][]int
}

func newHarness(t *testing.T, nb NewBackend, trim func(bus.Topic) bus.Trim) *harness {
	t.Helper()
	be := nb(t, trim)
	t.Cleanup(func() {
		if err := closeBus(t, be.Bus); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return &harness{t: t, b: be.Bus, loseFn: be.Lose, log: map[string][]bus.Message{}, starts: map[string][]int{}}
}

// closeBus closes b, failing the test if Close does not return in time: a
// bus that cannot end its subscriptions would otherwise hang the whole run.
func closeBus(t *testing.T, b bus.Bus) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- b.Close() }()
	select {
	case err := <-done:
		return err
	case <-time.After(wait):
		t.Fatalf("Close did not return in %v", wait)
		return nil
	}
}

func (h *harness) publish(topic bus.Topic, id bus.ID) {
	h.t.Helper()
	h.publishData(topic, id, fmt.Appendf(nil, "%s@%s", topic, id))
}

// publishData publishes, failing the test if Publish errs or does not return
// in time (a publisher must never wait for a subscriber), then scribbles on
// data: the bus must have kept its own copy.
func (h *harness) publishData(topic bus.Topic, id bus.ID, data []byte) {
	h.t.Helper()
	want := slices.Clone(data)
	done := make(chan error, 1)
	go func() { done <- h.b.Publish(context.Background(), topic, bus.Message{ID: id, Data: data}) }()
	select {
	case err := <-done:
		if err != nil {
			h.t.Fatalf("publish %s to %s: %v", id, topic, err)
		}
	case <-time.After(wait):
		h.t.Fatalf("publish %s to %s did not return in %v: is it waiting for a subscriber?", id, topic, wait)
	}
	for i := range data {
		data[i] = '!'
	}
	h.mu.Lock()
	h.log[topic.String()] = append(h.log[topic.String()], bus.Message{ID: id, Data: want})
	h.mu.Unlock()
}

func (h *harness) lose(topic bus.Topic) {
	h.t.Helper()
	h.loseFn(h.t, topic)
	h.mu.Lock()
	h.starts[topic.String()] = append(h.starts[topic.String()], len(h.log[topic.String()]))
	h.mu.Unlock()
}

func (h *harness) subscribe(ctx context.Context, topic bus.Topic, after bus.ID) *sub {
	h.t.Helper()
	s, err := h.b.Subscribe(ctx, topic, after)
	if err != nil {
		h.t.Fatalf("subscribe to %s after %s: %v", topic, after, err)
	}
	return h.track(topic, after, s)
}

func (h *harness) track(topic bus.Topic, after bus.ID, s bus.Subscription) *sub {
	return &sub{h: h, topic: topic, s: s, after: after, cursor: after}
}

// sub is one subscription and what it received.
type sub struct {
	h      *harness
	topic  bus.Topic
	s      bus.Subscription
	after  bus.ID
	cursor bus.ID
	got    []bus.Message
}

// next returns the next message, failing the test if the subscription ends or
// nothing arrives in time.
func (s *sub) next() bus.Message {
	s.h.t.Helper()
	select {
	case m, ok := <-s.s.C():
		if !ok {
			s.h.t.Fatalf("subscription to %s after %s ended after %d messages: %v", s.topic, s.after, len(s.got), s.s.Err())
		}
		s.check(m)
		return m
	case <-time.After(wait):
		s.h.t.Fatalf("subscription to %s after %s: no message in %v after %d", s.topic, s.after, wait, len(s.got))
	}
	return bus.Message{}
}

// end waits for the subscription to end, checking what it still delivers,
// and returns its Err.
func (s *sub) end() error {
	s.h.t.Helper()
	timeout := time.After(wait)
	for {
		select {
		case m, ok := <-s.s.C():
			if !ok {
				return s.s.Err()
			}
			s.check(m)
		case <-timeout:
			s.h.t.Fatalf("subscription to %s after %s did not end in %v", s.topic, s.after, wait)
			return nil
		}
	}
}

// caughtUp reports whether the subscriber has received the last message
// published to its topic.
func (s *sub) caughtUp() bool {
	s.h.mu.Lock()
	defer s.h.mu.Unlock()
	log := s.h.log[s.topic.String()]
	return len(log) == 0 || s.cursor.Compare(log[len(log)-1].ID) >= 0
}

// check holds every delivery to the invariants in the package comment.
func (s *sub) check(m bus.Message) {
	s.h.t.Helper()
	s.h.mu.Lock()
	log := s.h.log[s.topic.String()]
	starts := s.h.starts[s.topic.String()]
	s.h.mu.Unlock()

	i := slices.IndexFunc(log, func(p bus.Message) bool { return p.ID.Compare(s.cursor) > 0 })
	// From the start, a subscriber may begin where the topic last started
	// afresh: a bus cannot know about a history lost before its first entry.
	allowed := i >= 0 && log[i].ID == m.ID
	if !allowed && s.cursor.IsZero() && len(s.got) == 0 {
		for _, k := range starts {
			if k < len(log) && log[k].ID == m.ID {
				allowed, i = true, k
			}
		}
	}
	switch {
	case !allowed && i < 0:
		s.h.t.Errorf("%s after %s: got %s, which was never published after %s", s.topic, s.after, m.ID, s.cursor)
	case !allowed:
		s.h.t.Errorf("%s after %s: got %s after %s, want %s (nothing skipped, repeated or reordered)", s.topic, s.after, m.ID, s.cursor, log[i].ID)
	case !bytes.Equal(m.Data, log[i].Data):
		s.h.t.Errorf("%s: message %s has data %q, want %q", s.topic, m.ID, m.Data, log[i].Data)
	}
	s.cursor = m.ID
	s.got = append(s.got, m)
	for j := range m.Data {
		m.Data[j] = '?' // a subscriber may write to what it was given
	}
}
