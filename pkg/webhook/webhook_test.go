package webhook_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/store/memory"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
	"github.com/bsamanvith/agentkit/pkg/webhook"
)

func TestSignAndVerify(t *testing.T) {
	t.Parallel()
	secret := []byte("s3cret")
	now := time.Unix(1_800_000_000, 0)
	body := []byte(`{"run":"r"}`)
	sig := webhook.Sign(secret, now, body)
	if err := webhook.Verify(secret, sig, body, now.Add(time.Minute), 0); err != nil {
		t.Fatalf("Verify of a fresh signature: %v", err)
	}
	for _, c := range []struct {
		name   string
		secret []byte
		header string
		body   []byte
		now    time.Time
	}{
		{"another body", secret, sig, []byte(`{"run":"x"}`), now},
		{"another secret", []byte("other"), sig, body, now},
		{"too old", secret, sig, body, now.Add(webhook.DefaultTolerance + time.Second)},
		{"from the future", secret, sig, body, now.Add(-webhook.DefaultTolerance - time.Second)},
		{"no header", secret, "", body, now},
		{"no v1", secret, "t=" + strconv.FormatInt(now.Unix(), 10), body, now},
		{"another timestamp", secret, "t=" + strconv.FormatInt(now.Unix()+1, 10) + sig[len("t=")+10:], body, now},
	} {
		if err := webhook.Verify(c.secret, c.header, c.body, c.now, 0); !errors.Is(err, webhook.ErrSignature) {
			t.Errorf("%s: Verify = %v, want ErrSignature", c.name, err)
		}
	}
	// A receiver rotating secrets gets several v1 values.
	two := sig + ",v1=" + "00"
	if err := webhook.Verify(secret, two, body, now, 0); err != nil {
		t.Errorf("Verify with a second v1: %v", err)
	}
}

func TestSecrets(t *testing.T) {
	t.Parallel()
	master := []byte("master")
	a, b := webhook.DeriveSecret(master, "acme"), webhook.DeriveSecret(master, "globex")
	if slices.Equal(a, b) || !slices.Equal(a, webhook.DeriveSecret(master, "acme")) {
		t.Error("tenants must get different secrets, and the same one each time")
	}
	got, err := webhook.DecodeSecret(webhook.EncodeSecret(a))
	if err != nil || !slices.Equal(got, a) {
		t.Errorf("DecodeSecret(EncodeSecret) = %x, %v", got, err)
	}
	for _, s := range []string{"", "whsec_", "abc", "whsec_!!"} {
		if _, err := webhook.DecodeSecret(s); err == nil {
			t.Errorf("DecodeSecret(%q) succeeded", s)
		}
	}
}

// rig is a memory store with ended runs, a receiver and notifiers.
type rig struct {
	t     *testing.T
	clk   *clocktest.Fake
	st    *memory.Store
	admin store.Admin
	srv   *httptest.Server

	mu       sync.Mutex
	answer   func(attempt int) int // the status of each attempt; default 200
	received []delivery
}

type delivery struct {
	id      string
	attempt int
	body    []byte
	err     error // the signature's
}

func secretOf(tn tenancy.Tenant) ([]byte, error) {
	return webhook.DeriveSecret([]byte("master"), tn.ID()), nil
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, clk: clocktest.NewFake(time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC))}
	r.st = memory.New(memory.WithClock(r.clk))
	r.admin = memory.Admin(r.st)
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		tn, _ := tenancy.NewTenant(req.URL.Query().Get("tenant"))
		secret, _ := secretOf(tn)
		n, _ := strconv.Atoi(req.Header.Get(webhook.HeaderAttempt))
		d := delivery{
			id: req.Header.Get(webhook.HeaderID), attempt: n, body: body,
			err: webhook.Verify(secret, req.Header.Get(webhook.HeaderSignature), body, r.clk.Now(), 0),
		}
		r.mu.Lock()
		r.received = append(r.received, d)
		answer := r.answer
		r.mu.Unlock()
		status := http.StatusOK
		if answer != nil {
			status = answer(n)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

// ended makes a completed run of tenant with a notification to the receiver.
func (r *rig) ended(tenant, id string) {
	r.t.Helper()
	tn, err := tenancy.NewTenant(tenant)
	if err != nil {
		r.t.Fatal(err)
	}
	ctx := tenancy.With(r.t.Context(), tn)
	if _, err := r.st.Create(ctx, id, "conv-"+id); err != nil {
		r.t.Fatal(err)
	}
	if _, err := r.st.CreateNotification(ctx, id, r.srv.URL+"/hook?tenant="+tenant); err != nil {
		r.t.Fatal(err)
	}
	if _, err := r.st.Transition(ctx, id, store.StatusRunning, store.StatusCompleted, store.Details{}); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) setAnswer(f func(attempt int) int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.answer = f
}

func (r *rig) notifier(opts ...webhook.Option) *webhook.Notifier {
	r.t.Helper()
	payload := func(ctx context.Context, runID string) ([]byte, error) {
		tn, _ := tenancy.From(ctx)
		return json.Marshal(map[string]string{"tenant": tn.ID(), "run": runID})
	}
	n, err := webhook.New(r.admin, payload, secretOf, append([]webhook.Option{webhook.WithClock(r.clk), webhook.WithClient(r.srv.Client())}, opts...)...)
	if err != nil {
		r.t.Fatal(err)
	}
	return n
}

func (r *rig) deliveries() []delivery {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.received)
}

func (r *rig) notification(tenant, id string) store.Notification {
	r.t.Helper()
	tn, _ := tenancy.NewTenant(tenant)
	n, err := r.st.GetNotification(tenancy.With(r.t.Context(), tn), id)
	if err != nil {
		r.t.Fatal(err)
	}
	return n
}

// Criterion "Webhook delivery": signed with the tenant's secret and verified
// by the receiver, with the run's payload.
func TestDeliveryIsSignedAndVerified(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.ended("acme", "run-1")
	rep, err := r.notifier().Deliver(t.Context())
	if err != nil || rep != (webhook.Report{Delivered: 1}) {
		t.Fatalf("Deliver = %+v, %v; want one delivered", rep, err)
	}
	got := r.deliveries()
	if len(got) != 1 {
		t.Fatalf("the receiver got %d deliveries, want 1", len(got))
	}
	d := got[0]
	if d.err != nil || d.id != "run-1" || d.attempt != 1 || string(d.body) != `{"run":"run-1","tenant":"acme"}` {
		t.Errorf("delivery = %+v (signature: %v), want run-1's, signed", d, d.err)
	}
	n := r.notification("acme", "run-1")
	if n.State != store.NotificationDelivered || len(n.Attempts) != 1 || n.Attempts[0].Status != 200 || n.Attempts[0].Error != "" {
		t.Errorf("notification = %+v, want delivered after one recorded attempt", n)
	}
	// Another tenant's secret does not verify it.
	other, _ := secretOf(mustTenant(t, "globex"))
	sig := webhook.Sign(webhook.DeriveSecret([]byte("master"), "acme"), r.clk.Now(), d.body)
	if webhook.Verify(other, sig, d.body, r.clk.Now(), 0) == nil {
		t.Error("another tenant's secret verifies the delivery")
	}
}

// Criterion "Webhook delivery": retried after a 500, on the fake clock, with
// the same idempotency key, and every attempt recorded.
func TestRetriedAfterA500(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.setAnswer(func(attempt int) int {
		if attempt == 1 {
			return http.StatusInternalServerError
		}
		return http.StatusNoContent
	})
	r.ended("acme", "run-1")
	n := r.notifier(webhook.WithBackoff(10*time.Second, time.Hour))
	if rep, err := n.Deliver(t.Context()); err != nil || rep != (webhook.Report{Retrying: 1}) {
		t.Fatalf("first pass = %+v, %v; want one retrying", rep, err)
	}
	after := r.notification("acme", "run-1")
	if after.State != store.NotificationPending || !after.NextAttemptAt.Equal(r.clk.Now().Add(10*time.Second)) ||
		len(after.Attempts) != 1 || after.Attempts[0].Status != 500 {
		t.Errorf("after the 500: %+v, want pending for 10s with the attempt recorded", after)
	}
	r.clk.Advance(9 * time.Second)
	if rep, _ := n.Deliver(t.Context()); rep != (webhook.Report{}) {
		t.Errorf("before the delay: %+v, want nothing", rep)
	}
	r.clk.Advance(time.Second)
	if rep, err := n.Deliver(t.Context()); err != nil || rep != (webhook.Report{Delivered: 1}) {
		t.Fatalf("after the delay = %+v, %v; want delivered", rep, err)
	}
	got := r.deliveries()
	if len(got) != 2 || got[0].id != got[1].id || got[0].attempt != 1 || got[1].attempt != 2 || got[1].err != nil {
		t.Errorf("deliveries = %+v, want two attempts of one webhook ID, signed", got)
	}
	if final := r.notification("acme", "run-1"); final.State != store.NotificationDelivered || len(final.Attempts) != 2 {
		t.Errorf("finally %+v, want delivered after two attempts", final)
	}
}

// A webhook that never answers well is tried with growing delays for the
// window, then given up.
func TestGivesUpAfterTheWindow(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.setAnswer(func(int) int { return http.StatusBadGateway })
	r.ended("acme", "run-1")
	n := r.notifier(webhook.WithBackoff(time.Minute, time.Hour), webhook.WithWindow(3*time.Hour))
	var failed int
	for range 20 {
		rep, err := n.Deliver(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		failed += rep.Failed
		r.clk.Advance(time.Hour)
	}
	got := r.notification("acme", "run-1")
	if failed != 1 || got.State != store.NotificationFailed {
		t.Fatalf("after the window: %+v (%d given up), want failed once", got, failed)
	}
	// 0, 1m, 2m, 4m, ... capped at 1h, within 3h of the first.
	first := got.Attempts[0].At
	for i, a := range got.Attempts {
		if a.At.Sub(first) > 3*time.Hour || a.Status != http.StatusBadGateway {
			t.Errorf("attempt %d: %+v, want a 502 within the window", i+1, a)
		}
	}
	if len(got.Attempts) < 4 || len(r.deliveries()) != len(got.Attempts) {
		t.Errorf("%d attempts recorded, %d made; want the same, several", len(got.Attempts), len(r.deliveries()))
	}
}

// Criterion "Webhook delivery": never sent twice for one completion, by two
// notifiers delivering at once.
func TestNeverSentTwice(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	const runs = 20
	for i := range runs {
		r.ended([]string{"acme", "globex"}[i%2], fmt.Sprintf("run-%d", i))
	}
	a, b := r.notifier(webhook.WithOwner("a")), r.notifier(webhook.WithOwner("b"))
	var wg sync.WaitGroup
	var total webhook.Report
	var mu sync.Mutex
	for _, n := range []*webhook.Notifier{a, b} {
		wg.Go(func() {
			rep, err := n.Deliver(t.Context())
			if err != nil {
				t.Error(err)
			}
			mu.Lock()
			total.Delivered += rep.Delivered
			mu.Unlock()
		})
	}
	wg.Wait()
	ids := map[string]int{}
	for _, d := range r.deliveries() {
		ids[d.id]++
		if d.err != nil {
			t.Errorf("delivery of %s: %v", d.id, d.err)
		}
	}
	if total.Delivered != runs || len(ids) != runs {
		t.Errorf("delivered %d, to %d webhook IDs; want %d", total.Delivered, len(ids), runs)
	}
	for id, n := range ids {
		if n != 1 {
			t.Errorf("%s was sent %d times", id, n)
		}
	}
	// Delivered is final: a later pass sends nothing.
	if rep, _ := a.Deliver(t.Context()); rep != (webhook.Report{}) {
		t.Errorf("a later pass: %+v, want nothing", rep)
	}
}

// A redirect is not followed: it is a failed attempt.
func TestRedirectIsAFailedAttempt(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.setAnswer(func(int) int { return http.StatusFound })
	r.ended("acme", "run-1")
	if rep, err := r.notifier().Deliver(t.Context()); err != nil || rep.Retrying != 1 {
		t.Fatalf("Deliver = %+v, %v; want retrying", rep, err)
	}
	if n := r.notification("acme", "run-1"); n.Attempts[0].Status != http.StatusFound || n.Attempts[0].Error == "" {
		t.Errorf("attempt = %+v, want the 302 recorded as a failure", n.Attempts[0])
	}
}

// Only ended runs are told: a running run's notification waits.
func TestWaitsForTheRunToEnd(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	ctx := tenancy.With(t.Context(), mustTenant(t, "acme"))
	if _, err := r.st.Create(ctx, "run-1", "c"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.CreateNotification(ctx, "run-1", r.srv.URL+"/hook?tenant=acme"); err != nil {
		t.Fatal(err)
	}
	n := r.notifier()
	if rep, _ := n.Deliver(t.Context()); rep != (webhook.Report{}) {
		t.Errorf("while the run goes on: %+v, want nothing", rep)
	}
	if _, err := r.st.Transition(ctx, "run-1", store.StatusRunning, store.StatusFailed, store.Details{Error: "boom"}); err != nil {
		t.Fatal(err)
	}
	if rep, _ := n.Deliver(t.Context()); rep.Delivered != 1 {
		t.Errorf("once failed for good: %+v, want delivered", rep)
	}
}

func TestNewRefusesBadOptions(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	p := func(context.Context, string) ([]byte, error) { return nil, nil }
	for _, opts := range [][]webhook.Option{
		{webhook.WithLease(time.Second), webhook.WithTimeout(time.Second)},
		{webhook.WithWorkers(0)},
		{webhook.WithBackoff(time.Minute, time.Second)},
		{webhook.WithOwner("")},
	} {
		if _, err := webhook.New(r.admin, p, secretOf, opts...); !errors.Is(err, webhook.ErrInvalidConfig) {
			t.Errorf("New(%d options) = %v, want ErrInvalidConfig", len(opts), err)
		}
	}
	if _, err := webhook.New(nil, p, secretOf); !errors.Is(err, webhook.ErrInvalidConfig) {
		t.Errorf("New with no admin = %v, want ErrInvalidConfig", err)
	}
}

func mustTenant(t *testing.T, id string) tenancy.Tenant {
	t.Helper()
	tn, err := tenancy.NewTenant(id)
	if err != nil {
		t.Fatal(err)
	}
	return tn
}
