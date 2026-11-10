package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
)

// Cap bounds a paid lane: messages and paise a day and a month, counted
// in UTC+Offset days (India's when zero). Zero is no bound.
type Cap struct {
	Day       int   `json:"day,omitempty"`
	Month     int   `json:"month,omitempty"`
	DayCost   int64 `json:"day_cost,omitempty"`   // paise
	MonthCost int64 `json:"month_cost,omitempty"` // paise
	Offset    int   `json:"offset,omitempty"`     // minutes east of UTC
}

func (c Cap) none() bool { return c.Day == 0 && c.Month == 0 && c.DayCost == 0 && c.MonthCost == 0 }

// GuardOption configures [Guard].
type GuardOption func(*guard)

// WithCost sets what one message costs on the lane, in paise, when the
// lane does not say (default 0).
func WithCost(paise int64) GuardOption { return func(g *guard) { g.cost = paise } }

// WithCap bounds the lane's spend.
func WithCap(c Cap) GuardOption { return func(g *guard) { g.cap = c } }

// WithOnCap calls f, at most once a day for the lane, when the lane
// reaches its cap: tell the admin.
func WithOnCap(f func(lane, why string)) GuardOption { return func(g *guard) { g.onCap = f } }

// WithGuardClock sets the clock (default the machine's).
func WithGuardClock(c clock.Clock) GuardOption { return func(g *guard) { g.clock = c } }

// WithGuardLogger sets the logger.
func WithGuardLogger(l *slog.Logger) GuardOption { return func(g *guard) { g.log = l } }

// WithoutConsent sends without a recorded opt-in: for lanes with no
// address (the inbox, the log), and push, where subscribing is the
// consent. A STOP on the lane still stops it.
func WithoutConsent() GuardOption { return func(g *guard) { g.implicit = true } }

type guard struct {
	lane     Lane
	info     LaneInfo
	store    Store
	clock    clock.Clock
	log      *slog.Logger
	cost     int64
	cap      Cap
	onCap    func(lane, why string)
	implicit bool

	mu       sync.Mutex // serialises the cap check and the claim, so two sends do not both pass the cap
	toldOnce string     // the day the admin was last told of the cap
}

// Guard makes lane a [Sender] over store: it sends m only when the
// person has an address on the lane and agreed to it, the lane is under
// its cap, and m was not sent on the lane before; it records the
// delivery; and an [ErrGone] from the lane is saved as the person's STOP.
func Guard(lane Lane, store Store, opts ...GuardOption) Sender {
	g := &guard{lane: lane, info: lane.Info(), store: store, clock: clock.Real(), log: slog.New(slog.DiscardHandler)}
	for _, o := range opts {
		o(g)
	}
	if g.info.Address == AddrNone || g.info.Address == AddrPush {
		g.implicit = true
	}
	return g
}

func (g *guard) Name() string    { return g.info.Name }
func (g *guard) Lanes() []string { return []string{g.info.Name} }

func (g *guard) skip(err error) ([]Receipt, error) {
	return []Receipt{{Lane: g.info.Name, Status: StatusSkipped, Error: err.Error()}}, err
}

func (g *guard) Send(ctx context.Context, m Message) ([]Receipt, error) {
	name := g.info.Name
	if slices.Contains(m.Sent, name) {
		return []Receipt{{Lane: name, Status: StatusSent, Repeat: true}}, nil
	}
	r := m.Recipient
	if r.Address(g.info.Address) == "" {
		return g.skip(ErrNoAddress)
	}
	if c, ok := r.Consent[name]; (ok && !c.OptedIn) || (!ok && !g.implicit) {
		return g.skip(ErrNoConsent)
	}
	g.mu.Lock()
	if err := g.underCap(ctx); err != nil {
		g.mu.Unlock()
		if errors.Is(err, ErrOverCap) {
			return g.skip(err)
		}
		return nil, Temporary(err)
	}
	now := g.clock.Now().UTC()
	prev, claimed, err := g.store.BeginDelivery(ctx, Delivery{Message: m.ID, Lane: name, Recipient: r.ID, Status: StatusSending, Cost: g.cost, CreatedAt: now, UpdatedAt: now})
	g.mu.Unlock()
	if err != nil {
		return nil, Temporary(err)
	}
	if !claimed {
		// Sent before, or being sent by another worker, or its outcome is
		// unknown (a crash mid-send): never send it twice.
		rc := Receipt{Lane: name, Status: prev.Status, ProviderID: prev.ProviderID, Cost: prev.Cost, Error: prev.Error, Repeat: true}
		if prev.Status == StatusFailed {
			return []Receipt{rc}, Rejected(errors.New(prev.Error))
		}
		return []Receipt{rc}, nil
	}
	rc, err := g.lane.Deliver(ctx, m)
	rc.Lane = name
	d := Delivery{Message: m.ID, Lane: name, Recipient: r.ID, ProviderID: rc.ProviderID, Cost: rc.Cost, CreatedAt: now, UpdatedAt: g.clock.Now().UTC()}
	if d.Cost == 0 {
		d.Cost = g.cost
	}
	switch {
	case err == nil:
		d.Status = StatusSent
		if Accepted(rc.Status) {
			d.Status = rc.Status
		}
	case errors.Is(err, ErrTemporary):
		d.Status, d.Retryable, d.Error, d.Cost = StatusFailed, true, err.Error(), 0
	default:
		d.Status, d.Error, d.Cost = StatusFailed, err.Error(), 0
	}
	if m.Kind == KindCode && d.Error != "" {
		d.Error = redact(d.Error, r.Address(g.info.Address))
	}
	rc.Status, rc.Cost, rc.Error = d.Status, d.Cost, d.Error
	if ferr := g.store.FinishDelivery(context.WithoutCancel(ctx), d); ferr != nil {
		g.log.Error("notify: save delivery", "lane", name, "message", m.ID, "err", ferr)
	}
	if errors.Is(err, ErrGone) && m.Kind != KindCode { // a code's recipient is made up for it: nobody to record
		g.left(ctx, r.ID)
	}
	if err != nil && !errors.Is(err, ErrTemporary) && !errors.Is(err, ErrGone) && !errors.Is(err, ErrRejected) {
		err = Rejected(err) // a lane's unclassified error is final
	}
	return []Receipt{rc}, err
}

// left records that the person left the lane (a push lane drops the dead
// subscription itself and only says ErrGone when none is left).
func (g *guard) left(ctx context.Context, id string) {
	_, err := g.store.Update(context.WithoutCancel(ctx), id, func(r *Recipient) error {
		if r.Consent == nil {
			r.Consent = map[string]Consent{}
		}
		r.Consent[g.info.Name] = Consent{OptedIn: false, At: g.clock.Now().UTC(), Source: "lane"}
		return nil
	})
	if err != nil {
		g.log.Error("notify: record opt-out", "lane", g.info.Name, "recipient", id, "err", err)
	}
}

// underCap reports ErrOverCap when one more message would pass the cap.
func (g *guard) underCap(ctx context.Context) error {
	if g.cap.none() {
		return nil
	}
	off := time.Duration(g.cap.Offset) * time.Minute
	if g.cap.Offset == 0 {
		off = IndiaOffset * time.Minute
	}
	local := g.clock.Now().UTC().Add(off)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC).Add(-off)
	month := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, time.UTC).Add(-off)
	check := func(since time.Time, n int, cost int64, span string) error {
		if n == 0 && cost == 0 {
			return nil
		}
		count, spent, err := g.store.Spend(ctx, g.info.Name, since)
		if err != nil {
			return err
		}
		var why string
		switch {
		case n > 0 && count+1 > n:
			why = fmt.Sprintf("%d messages this %s, the cap is %d", count, span, n)
		case cost > 0 && spent+g.cost > cost:
			why = fmt.Sprintf("₹%.2f spent this %s, the cap is ₹%.2f", float64(spent)/100, span, float64(cost)/100)
		default:
			return nil
		}
		g.tell(local.Format(time.DateOnly), why)
		return fmt.Errorf("%w: %s", ErrOverCap, why)
	}
	if err := check(day, g.cap.Day, g.cap.DayCost, "day"); err != nil {
		return err
	}
	return check(month, g.cap.Month, g.cap.MonthCost, "month")
}

func (g *guard) tell(day, why string) {
	if g.toldOnce == day {
		return
	}
	g.toldOnce = day
	g.log.Warn("notify: lane over its cap; its messages go to the next lane in their chain", "lane", g.info.Name, "why", why)
	if g.onCap != nil {
		g.onCap(g.info.Name, why)
	}
}

// Off is a lane that is not configured: every message skips it with
// [ErrLaneOff], so a chain moves on. Name it in the tree so the tree does
// not change when the lane's keys arrive.
func Off(name string) Sender { return off(name) }

type off string

func (o off) Name() string    { return string(o) }
func (o off) Lanes() []string { return []string{string(o)} }
func (o off) Send(context.Context, Message) ([]Receipt, error) {
	return []Receipt{{Lane: string(o), Status: StatusSkipped, Error: ErrLaneOff.Error()}}, ErrLaneOff
}

// redact takes addr (and its digits without the +) out of a provider's
// error text, for a code's records.
func redact(text, addr string) string {
	if addr == "" {
		return text
	}
	text = strings.ReplaceAll(text, addr, "[address]")
	return strings.ReplaceAll(text, strings.TrimPrefix(addr, "+"), "[address]")
}
