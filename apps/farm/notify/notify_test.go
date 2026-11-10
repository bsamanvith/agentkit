package notify_test

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/notify"
	"github.com/bsamanvith/agentkit/apps/farm/notify/notifytest"
	"github.com/bsamanvith/agentkit/apps/farm/notify/sqlite"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

// t0 is 11:30 in India: outside the default quiet hours.
var t0 = time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)

func store(t *testing.T) notify.Store {
	t.Helper()
	s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "farm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func ravi(lanes ...string) notify.Recipient {
	r := notify.Recipient{
		ID: "ravi", Name: "Ravi", Phone: "+919876543210", Email: "ravi@example.com", Telegram: "4242",
		Consent: map[string]notify.Consent{}, CreatedAt: t0, UpdatedAt: t0,
	}
	for _, l := range lanes {
		r.Consent[l] = notify.Consent{OptedIn: true, At: t0, Source: "settings"}
	}
	return r
}

func message(id, sev string) notify.Message {
	return notify.Message{ID: id, To: "ravi", Kind: notify.KindAlert, Severity: sev, Title: "Heavy rain Thursday", Body: "Clear the drains."}
}

type world struct {
	t     *testing.T
	ctx   context.Context
	store notify.Store
	clock *clocktest.Fake
	lanes map[string]*notifytest.Fake
	opts  map[string][]notify.GuardOption
}

func newWorld(t *testing.T, r notify.Recipient) *world {
	t.Helper()
	w := &world{t: t, ctx: context.Background(), store: store(t), clock: clocktest.NewFake(t0), lanes: map[string]*notifytest.Fake{}, opts: map[string][]notify.GuardOption{}}
	if err := w.store.PutRecipient(w.ctx, r); err != nil {
		t.Fatal(err)
	}
	for name, k := range map[string]notify.AddressKind{
		"push": notify.AddrPush, "telegram": notify.AddrTelegram, "email": notify.AddrEmail,
		"whatsapp": notify.AddrPhone, "sms": notify.AddrPhone,
	} {
		w.lanes[name] = notifytest.NewFake(name, k)
	}
	return w
}

func (w *world) guarded() map[string]notify.Sender {
	out := map[string]notify.Sender{}
	for name, l := range w.lanes {
		out[name] = notify.Guard(l, w.store, append([]notify.GuardOption{notify.WithGuardClock(w.clock)}, w.opts[name]...)...)
	}
	return out
}

func (w *world) dispatcher(routes string, opts ...notify.DispatchOption) *notify.Dispatcher {
	w.t.Helper()
	root, err := notify.Parse(routes, w.guarded())
	if err != nil {
		w.t.Fatal(err)
	}
	d, err := notify.NewDispatcher(w.store, root, append([]notify.DispatchOption{notify.WithClock(w.clock)}, opts...)...)
	if err != nil {
		w.t.Fatal(err)
	}
	return d
}

func (w *world) send(s notify.Sender, m notify.Message) ([]notify.Receipt, error) {
	w.t.Helper()
	r, err := w.store.GetRecipient(w.ctx, m.To)
	if err != nil {
		w.t.Fatal(err)
	}
	m.Recipient = r
	return s.Send(w.ctx, m)
}

func (w *world) item(id string) notify.Item {
	w.t.Helper()
	it, err := w.store.GetItem(w.ctx, id)
	if err != nil {
		w.t.Fatal(err)
	}
	return it
}

func (w *world) counts() map[string]int {
	out := map[string]int{}
	for n, l := range w.lanes {
		if c := l.Count(); c > 0 {
			out[n] = c
		}
	}
	return out
}

func sameCounts(got, want map[string]int) bool {
	if len(got) != len(want) {
		return false
	}
	for k, v := range want {
		if got[k] != v {
			return false
		}
	}
	return true
}

func TestGuardSendsOnlyWithAddressAndConsent(t *testing.T) {
	r := ravi("sms")
	r.Email = ""
	r.Push = []notify.PushSubscription{{Endpoint: "https://push.example/1"}}
	r.Push[0].Keys.P256dh, r.Push[0].Keys.Auth = "k", "a"
	w := newWorld(t, r)
	g := w.guarded()
	for _, c := range []struct {
		lane string
		want error
	}{
		{"sms", nil},                      // opted in
		{"whatsapp", notify.ErrNoConsent}, // a phone, no consent
		{"email", notify.ErrNoAddress},    // no address
		{"push", nil},                     // subscribing is consent
	} {
		rcs, err := w.send(g[c.lane], message("m-"+c.lane, notify.Urgent))
		if !errors.Is(err, c.want) || (c.want == nil && err != nil) {
			t.Errorf("%s: %v, want %v", c.lane, err, c.want)
		}
		if c.want != nil && (len(rcs) != 1 || rcs[0].Status != notify.StatusSkipped) {
			t.Errorf("%s: receipts %+v, want one skipped", c.lane, rcs)
		}
	}
	if got := w.counts(); !sameCounts(got, map[string]int{"sms": 1, "push": 1}) {
		t.Errorf("sent %v, want sms and push once", got)
	}
	// A STOP on push stops it, though push needs no opt-in.
	if _, err := w.store.Update(w.ctx, "ravi", func(r *notify.Recipient) error {
		r.Consent["push"] = notify.Consent{OptedIn: false, Source: "stop"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.send(g["push"], message("m-push-2", notify.Urgent)); !errors.Is(err, notify.ErrNoConsent) {
		t.Errorf("push after STOP: %v, want ErrNoConsent", err)
	}
}

func TestGuardSendsAMessageOncePerLane(t *testing.T) {
	w := newWorld(t, ravi("sms"))
	g := w.guarded()["sms"]
	m := message("m1", notify.Urgent)
	for range 3 {
		rcs, err := w.send(g, m)
		if err != nil {
			t.Fatal(err)
		}
		if len(rcs) != 1 || !notify.Accepted(rcs[0].Status) {
			t.Fatalf("receipts %+v", rcs)
		}
	}
	if n := w.lanes["sms"].Count(); n != 1 {
		t.Errorf("sms sent %d times, want 1", n)
	}
	// A temporary failure may be tried again, and is.
	w.lanes["sms"].FailNext(notify.Temporary(errors.New("503")))
	if _, err := w.send(g, message("m2", notify.Urgent)); !errors.Is(err, notify.ErrTemporary) {
		t.Fatalf("first try: %v, want ErrTemporary", err)
	}
	if _, err := w.send(g, message("m2", notify.Urgent)); err != nil {
		t.Fatalf("second try: %v", err)
	}
	// A final failure is not.
	w.lanes["sms"].FailNext(errors.New("invalid number"))
	if _, err := w.send(g, message("m3", notify.Urgent)); !errors.Is(err, notify.ErrRejected) {
		t.Fatalf("an unclassified lane error: %v, want ErrRejected", err)
	}
	if _, err := w.send(g, message("m3", notify.Urgent)); !errors.Is(err, notify.ErrRejected) {
		t.Errorf("again after a final failure: %v, want the same rejection", err)
	}
	if n := w.lanes["sms"].Count(); n != 2 {
		t.Errorf("sms sent %d times, want 2 (m1, m2)", n)
	}
}

func TestGoneIsAStop(t *testing.T) {
	w := newWorld(t, ravi("telegram"))
	w.lanes["telegram"].FailNext(notify.Gone(errors.New("bot was blocked by the user")))
	if _, err := w.send(w.guarded()["telegram"], message("m1", notify.Urgent)); !errors.Is(err, notify.ErrGone) {
		t.Fatalf("send: %v, want ErrGone", err)
	}
	r, _ := w.store.GetRecipient(w.ctx, "ravi")
	if c := r.Consent["telegram"]; c.OptedIn || c.Source != "lane" {
		t.Errorf("consent after the lane said gone = %+v, want opted out by the lane", c)
	}
	if _, err := w.send(w.guarded()["telegram"], message("m2", notify.Urgent)); !errors.Is(err, notify.ErrNoConsent) {
		t.Errorf("the next message: %v, want ErrNoConsent", err)
	}
}

func TestCostCaps(t *testing.T) {
	w := newWorld(t, ravi("whatsapp", "sms"))
	var told []string
	w.opts["whatsapp"] = []notify.GuardOption{
		notify.WithCost(14), notify.WithCap(notify.Cap{Day: 2, MonthCost: 50}),
		notify.WithOnCap(func(lane, why string) { told = append(told, lane+": "+why) }),
	}
	tree := notify.Chain(w.guarded()["whatsapp"], w.guarded()["sms"])
	lanesUsed := func(id string) string {
		rcs, err := w.send(tree, message(id, notify.Urgent))
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		var sent []string
		for _, r := range rcs {
			if notify.Accepted(r.Status) {
				sent = append(sent, r.Lane)
			}
		}
		return strings.Join(sent, ",")
	}
	for i, want := range []string{"whatsapp", "whatsapp", "sms", "sms"} {
		if got := lanesUsed("d1-" + string(rune('a'+i))); got != want {
			t.Errorf("message %d went by %s, want %s (two a day on WhatsApp, then the fallback)", i+1, got, want)
		}
	}
	if len(told) != 1 || !strings.Contains(told[0], "2 messages this day") {
		t.Errorf("the admin was told %q, want once that the day's cap was reached", told)
	}
	// The next day: one more fits under the month's ₹0.50 (3 × 14 paise = 42), the next does not.
	w.clock.Advance(24 * time.Hour)
	for i, want := range []string{"whatsapp", "sms"} {
		if got := lanesUsed("d2-" + string(rune('a'+i))); got != want {
			t.Errorf("day 2, message %d went by %s, want %s (the month's cost cap)", i+1, got, want)
		}
	}
	n, cost, _ := w.store.Spend(w.ctx, "whatsapp", t0.Add(-24*time.Hour))
	if n != 3 || cost != 42 {
		t.Errorf("WhatsApp spend = %d messages, %d paise; want 3, 42", n, cost)
	}
}

func TestChain(t *testing.T) {
	w := newWorld(t, ravi("whatsapp", "sms", "telegram"))
	g := w.guarded()
	tree := notify.Chain(g["telegram"], g["whatsapp"], g["sms"])

	w.lanes["telegram"].FailNext(notify.Temporary(errors.New("429")))
	w.lanes["whatsapp"].FailNext(notify.Rejected(errors.New("no template")))
	rcs, err := w.send(tree, message("m1", notify.Urgent))
	if err != nil {
		t.Fatal(err)
	}
	if got := w.counts(); !sameCounts(got, map[string]int{"sms": 1}) {
		t.Errorf("sent %v, want only sms after telegram and whatsapp failed", got)
	}
	if len(rcs) != 3 || rcs[2].Lane != "sms" || rcs[2].Status != notify.StatusSent || rcs[0].Status != notify.StatusFailed {
		t.Errorf("receipts %+v", rcs)
	}

	// It stops at the first that sends.
	if _, err := w.send(tree, message("m2", notify.Urgent)); err != nil {
		t.Fatal(err)
	}
	if got := w.counts(); !sameCounts(got, map[string]int{"sms": 1, "telegram": 1}) {
		t.Errorf("sent %v, want telegram only for m2", got)
	}

	// A message already sent on a later lane is not sent on an earlier one.
	m := message("m1", notify.Urgent)
	m.Sent = []string{"sms"}
	if _, err := w.send(tree, m); err != nil {
		t.Fatal(err)
	}
	if got := w.counts(); !sameCounts(got, map[string]int{"sms": 1, "telegram": 1}) {
		t.Errorf("a retry of m1 sent again: %v", got)
	}

	// Nobody reachable: skipped, not failed.
	empty := notify.Chain(notify.Off("whatsapp"), g["email"])
	if _, err := w.send(empty, message("m3", notify.Urgent)); !errors.Is(err, notify.ErrSkipped) || errors.Is(err, notify.ErrRejected) {
		t.Errorf("all skipped: %v, want ErrSkipped only", err)
	}
	// A temporary failure is kept, so the message is tried again.
	w.lanes["telegram"].FailNext(notify.Temporary(errors.New("timeout")))
	if _, err := w.send(notify.Chain(g["telegram"], notify.Off("sms")), message("m4", notify.Urgent)); !errors.Is(err, notify.ErrTemporary) {
		t.Errorf("temporary then skipped: %v, want ErrTemporary", err)
	}
}

func TestFanOut(t *testing.T) {
	w := newWorld(t, ravi("whatsapp", "sms", "telegram", "email"))
	g := w.guarded()
	tree := notify.FanOut(g["telegram"], g["email"], notify.Off("push"))
	rcs, err := w.send(tree, message("m1", notify.Urgent))
	if err != nil {
		t.Fatal(err)
	}
	if len(rcs) != 3 {
		t.Errorf("receipts %+v, want one per lane", rcs)
	}
	if got := w.counts(); !sameCounts(got, map[string]int{"telegram": 1, "email": 1}) {
		t.Errorf("sent %v", got)
	}
	w.lanes["email"].FailNext(notify.Rejected(errors.New("bad address")))
	if _, err := w.send(tree, message("m2", notify.Urgent)); err != nil {
		t.Errorf("one sent, one rejected: %v, want nil (it got through)", err)
	}
	w.lanes["email"].FailNext(notify.Temporary(errors.New("421")))
	if _, err := w.send(tree, message("m3", notify.Urgent)); !errors.Is(err, notify.ErrTemporary) {
		t.Errorf("one sent, one temporary: %v, want ErrTemporary so it is tried again", err)
	}
	if _, err := w.send(notify.FanOut(notify.Off("a"), notify.Off("b")), message("m4", notify.Urgent)); !errors.Is(err, notify.ErrSkipped) {
		t.Errorf("all off: %v, want ErrSkipped", err)
	}
}

func TestRouterAndParse(t *testing.T) {
	w := newWorld(t, ravi())
	root, err := notify.Parse(" urgent = fanout(push, chain(whatsapp,sms)) ; alert.warning=telegram; digest=email; default=push", w.guarded())
	if err != nil {
		t.Fatal(err)
	}
	want := "urgent=fanout(push,chain(whatsapp,sms)); alert.warning=telegram; digest=email; default=push"
	if root.Name() != want {
		t.Errorf("Name = %q, want %q", root.Name(), want)
	}
	if got := root.Lanes(); !slices.Equal(got, []string{"push", "whatsapp", "sms", "telegram", "email"}) {
		t.Errorf("Lanes = %v", got)
	}
	pick := func(kind, sev string) string {
		m := notify.Message{Kind: kind, Severity: sev}
		s := root.(interface {
			Pick(notify.Message) notify.Sender
		}).Pick(m)
		if s == nil {
			return ""
		}
		return s.Name()
	}
	for _, c := range []struct{ kind, sev, want string }{
		{notify.KindAlert, notify.Urgent, "fanout(push,chain(whatsapp,sms))"},
		{notify.KindAlert, notify.Warning, "telegram"},
		{notify.KindReply, notify.Warning, "push"},
		{notify.KindDigest, notify.Urgent, "email"}, // the kind wins over the severity
		{notify.KindTest, notify.Advice, "push"},
	} {
		if got := pick(c.kind, c.sev); got != c.want {
			t.Errorf("%s %s goes to %s, want %s", c.sev, c.kind, got, c.want)
		}
	}

	for _, bad := range []string{
		"", "urgent", "urgent=pigeon", "urgent=fanout(push", "urgent=fanout()", "urgent=push,sms",
		"loud=push", "alert.loud=push", "urgent=push; urgent=sms", "urgent=fan(push)", "urgent=push)",
		"urgent=" + strings.Repeat("chain(", 10) + "push" + strings.Repeat(")", 10),
	} {
		if _, err := notify.Parse(bad, w.guarded()); !errors.Is(err, notify.ErrInvalid) {
			t.Errorf("Parse(%q): %v, want ErrInvalid", bad, err)
		}
	}
	if _, err := notify.Parse(notify.DefaultRoutes, w.guarded()); err != nil {
		t.Errorf("the default routes: %v", err)
	}
}

func TestDispatcherQuietHours(t *testing.T) {
	r := ravi("telegram")
	r.Quiet = &notify.QuietHours{Start: 21 * 60, End: 6 * 60}
	w := newWorld(t, r)
	// 22:00 in India.
	w.clock = clocktest.NewFake(time.Date(2026, 11, 10, 16, 30, 0, 0, time.UTC))
	d := w.dispatcher("default=telegram")
	for _, id := range []string{"advice", "urgent"} {
		sev := notify.Advice
		if id == "urgent" {
			sev = notify.Urgent
		}
		if _, err := d.Enqueue(w.ctx, message(id, sev)); err != nil {
			t.Fatal(err)
		}
	}
	d.Pass(w.ctx)
	if got := w.item("urgent").Status; got != notify.ItemDone {
		t.Errorf("urgent at 22:00: %s, want sent at once", got)
	}
	adv := w.item("advice")
	if adv.Status != notify.ItemPending || !adv.NextAt.Equal(time.Date(2026, 11, 11, 0, 30, 0, 0, time.UTC)) {
		t.Errorf("advice at 22:00: %s due %s, want held to 06:00 IST (00:30 UTC)", adv.Status, adv.NextAt)
	}
	w.clock.Advance(8 * time.Hour) // 06:00 IST
	d.Pass(w.ctx)
	if got := w.item("advice").Status; got != notify.ItemDone {
		t.Errorf("advice at 06:00: %s, want sent", got)
	}
	if n := w.lanes["telegram"].Count(); n != 2 {
		t.Errorf("telegram sent %d, want 2", n)
	}
}

func TestDispatcherRetriesWithoutSendingTwice(t *testing.T) {
	w := newWorld(t, ravi("whatsapp", "sms", "telegram"))
	d := w.dispatcher("default=fanout(telegram,chain(whatsapp,sms))", notify.WithTries(3, time.Minute))
	// Telegram is down for a while; WhatsApp fails, SMS gets through.
	w.lanes["telegram"].FailNext(notify.Temporary(errors.New("502")), notify.Temporary(errors.New("502")))
	w.lanes["whatsapp"].FailNext(notify.Temporary(errors.New("503")))
	if ok, err := d.Enqueue(w.ctx, message("m1", notify.Warning)); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if ok, _ := d.Enqueue(w.ctx, message("m1", notify.Warning)); ok {
		t.Error("the same message was queued twice")
	}
	d.Pass(w.ctx)
	it := w.item("m1")
	if it.Status != notify.ItemPending || it.Attempts != 1 || !it.NextAt.Equal(t0.Add(time.Minute)) {
		t.Fatalf("after the first try: %+v, want pending again in a minute", it)
	}
	w.clock.Advance(time.Minute)
	d.Pass(w.ctx)
	if it := w.item("m1"); it.Attempts != 2 || !it.NextAt.Equal(t0.Add(3*time.Minute)) {
		t.Fatalf("after the second try: %+v, want the wait doubled", it)
	}
	w.clock.Advance(2 * time.Minute)
	d.Pass(w.ctx)
	if it := w.item("m1"); it.Status != notify.ItemDone {
		t.Fatalf("after the third try: %+v, want done", it)
	}
	if got := w.counts(); !sameCounts(got, map[string]int{"telegram": 1, "sms": 1}) {
		t.Errorf("sent %v: want telegram once, sms once, and whatsapp never (its chain got through on sms)", got)
	}
	ds, _ := w.store.ListDeliveries(w.ctx, notify.DeliveryFilter{Message: "m1"})
	if len(ds) != 3 {
		t.Errorf("deliveries %+v, want telegram, whatsapp and sms", ds)
	}
}

func TestDispatcherGivesUpAndExpires(t *testing.T) {
	w := newWorld(t, ravi("telegram"))
	d := w.dispatcher("default=telegram", notify.WithTries(2, time.Minute), notify.WithExpiry(notify.Advice, 90*time.Minute))
	w.lanes["telegram"].FailNext(notify.Temporary(errors.New("1")), notify.Temporary(errors.New("2")))
	_, _ = d.Enqueue(w.ctx, message("tries", notify.Urgent))
	d.Pass(w.ctx)
	w.clock.Advance(time.Minute)
	d.Pass(w.ctx)
	if it := w.item("tries"); it.Status != notify.ItemFailed || it.Attempts != 2 || !strings.Contains(it.LastError, "gave up") {
		t.Errorf("after two failed tries: %+v, want failed", it)
	}

	r, _ := w.store.GetRecipient(w.ctx, "ravi")
	r.Quiet = &notify.QuietHours{Start: 11 * 60, End: 13 * 60} // 11:30 now: quiet
	_ = w.store.PutRecipient(w.ctx, r)
	_, _ = d.Enqueue(w.ctx, message("stale", notify.Advice))
	d.Pass(w.ctx)
	w.clock.Advance(2 * time.Hour)
	d.Pass(w.ctx)
	if it := w.item("stale"); it.Status != notify.ItemExpired {
		t.Errorf("advice held past its expiry: %+v, want expired", it)
	}

	_ = w.store.DeleteRecipient(w.ctx, "ravi")
	_, _ = d.Enqueue(w.ctx, message("gone", notify.Urgent))
	d.Pass(w.ctx)
	if it := w.item("gone"); it.Status != notify.ItemNothing {
		t.Errorf("to a removed recipient: %+v, want nothing", it)
	}
	if _, err := d.Enqueue(w.ctx, notify.Message{ID: "x", To: "ravi", Severity: "loud", Title: "x"}); !errors.Is(err, notify.ErrInvalid) {
		t.Errorf("a bad severity: %v", err)
	}
}

func TestDispatcherNothingToSend(t *testing.T) {
	w := newWorld(t, ravi()) // no consent anywhere
	d := w.dispatcher(notify.DefaultRoutes)
	_, _ = d.Enqueue(w.ctx, message("m1", notify.Urgent))
	d.Pass(w.ctx)
	if it := w.item("m1"); it.Status != notify.ItemNothing {
		t.Errorf("no lane open: %+v, want nothing (the notice is in the inbox)", it)
	}
	rcs, err := d.SendNow(w.ctx, notify.Message{ID: "test1", To: "ravi", Severity: notify.Advice, Title: "Test"}, nil)
	if !errors.Is(err, notify.ErrSkipped) || len(rcs) == 0 {
		t.Errorf("SendNow with no lane open = %+v, %v", rcs, err)
	}
}

func TestQuietUntil(t *testing.T) {
	r := notify.Recipient{}
	ist := func(h, m int) time.Time { return time.Date(2026, 11, 10, h, m, 0, 0, time.UTC).Add(-330 * time.Minute) }
	for _, c := range []struct {
		at    time.Time
		quiet bool
		until time.Time
	}{
		{ist(20, 59), false, time.Time{}},
		{ist(21, 0), true, ist(30, 0)},
		{ist(23, 30), true, ist(30, 0)},
		{ist(2, 0), true, ist(6, 0)},
		{ist(6, 0), false, time.Time{}},
	} {
		until, quiet := r.QuietUntil(c.at)
		if quiet != c.quiet || !until.Equal(c.until) {
			t.Errorf("at %s IST: %v until %s, want %v until %s", c.at.Add(330*time.Minute).Format("15:04"), quiet, until, c.quiet, c.until)
		}
	}
	r.Quiet = &notify.QuietHours{Start: 0, End: 0}
	if _, quiet := r.QuietUntil(ist(2, 0)); quiet {
		t.Error("no quiet hours were quiet")
	}
}

func TestInbound(t *testing.T) {
	r := ravi("whatsapp")
	r.Telegram, r.LinkCode = "", "link-code-123"
	w := newWorld(t, r)
	in := notify.NewInbound(w.store, w.clock, nil)

	in.Stop(w.ctx, "whatsapp", notify.AddrPhone, "+919876543210")
	got, _ := w.store.GetRecipient(w.ctx, "ravi")
	if c := got.Consent["whatsapp"]; c.OptedIn || c.Source != "stop" {
		t.Errorf("after STOP: %+v", c)
	}
	in.Start(w.ctx, "whatsapp", notify.AddrPhone, "+919876543210")
	if got, _ := w.store.GetRecipient(w.ctx, "ravi"); !got.OptedIn("whatsapp") {
		t.Error("START did not opt in again")
	}
	in.Stop(w.ctx, "sms", notify.AddrPhone, "+911111111111") // a stranger: nothing happens

	name, err := in.Link(w.ctx, "telegram", "link-code-123", notify.AddrTelegram, "777")
	if err != nil || name != "Ravi" {
		t.Fatalf("Link = %q, %v", name, err)
	}
	got, _ = w.store.GetRecipient(w.ctx, "ravi")
	if got.Telegram != "777" || !got.OptedIn("telegram") || got.LinkCode != "" {
		t.Errorf("after Link: %+v", got)
	}
	if _, err := in.Link(w.ctx, "telegram", "link-code-123", notify.AddrTelegram, "888"); !errors.Is(err, notify.ErrNotFound) {
		t.Errorf("a used code: %v, want ErrNotFound", err)
	}

	d := w.dispatcher("default=whatsapp")
	_, _ = d.Enqueue(w.ctx, message("m1", notify.Urgent))
	d.Pass(w.ctx)
	in.Status(w.ctx, "whatsapp", "whatsapp-1", notify.StatusRead, "")
	in.Status(w.ctx, "whatsapp", "whatsapp-1", notify.StatusDelivered, "")
	ds, _ := w.store.ListDeliveries(w.ctx, notify.DeliveryFilter{Message: "m1"})
	if len(ds) != 1 || ds[0].Status != notify.StatusRead {
		t.Errorf("after read then a late delivered: %+v, want read", ds)
	}
}

func TestIsStop(t *testing.T) {
	for _, s := range []string{"STOP", " stop ", "Unsubscribe", "/stop", "ఆపు", "बंद करें"} {
		if !notify.IsStop(s) {
			t.Errorf("IsStop(%q) = false", s)
		}
	}
	for _, s := range []string{"stop the rain?", "when to sow", ""} {
		if notify.IsStop(s) {
			t.Errorf("IsStop(%q) = true", s)
		}
	}
	if !notify.IsStart("START") || notify.IsStart("yes") {
		t.Error("IsStart")
	}
}

func TestRecipientCheck(t *testing.T) {
	ok := ravi()
	if err := ok.Check(); err != nil {
		t.Fatal(err)
	}
	for name, f := range map[string]func(*notify.Recipient){
		"phone without +":   func(r *notify.Recipient) { r.Phone = "919876543210" },
		"phone with letter": func(r *notify.Recipient) { r.Phone = "+91987654321a" },
		"email":             func(r *notify.Recipient) { r.Email = "ravi" },
		"offset":            func(r *notify.Recipient) { r.UTCOffset = 2000 },
		"quiet":             func(r *notify.Recipient) { r.Quiet = &notify.QuietHours{Start: 1440} },
		"push":              func(r *notify.Recipient) { r.Push = []notify.PushSubscription{{Endpoint: "http://x"}} },
		"id":                func(r *notify.Recipient) { r.ID = "" },
	} {
		r := ravi()
		f(&r)
		if err := r.Check(); !errors.Is(err, notify.ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
	}
}
