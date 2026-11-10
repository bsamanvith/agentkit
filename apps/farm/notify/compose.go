package notify

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// FanOut sends to all of senders at once. It succeeds when at least one
// takes the message and none failed for a while; when one failed for a
// while it returns an ErrTemporary, so the dispatcher tries again, and the
// lanes that already sent do not send again.
func FanOut(senders ...Sender) Sender { return fanOut(senders) }

type fanOut []Sender

func (f fanOut) Name() string    { return "fanout(" + names(f) + ")" }
func (f fanOut) Lanes() []string { return lanes(f) }

func (f fanOut) Send(ctx context.Context, m Message) ([]Receipt, error) {
	rcs := make([][]Receipt, len(f))
	errs := make([]error, len(f))
	var wg sync.WaitGroup
	for i, s := range f {
		wg.Go(func() { rcs[i], errs[i] = s.Send(ctx, m) })
	}
	wg.Wait()
	var all []Receipt
	sent := false
	for i := range f {
		all = append(all, rcs[i]...)
		if errs[i] == nil {
			sent = true
		}
	}
	if err := firstOf(errs, ErrTemporary); err != nil {
		return all, err
	}
	if sent {
		return all, nil
	}
	return all, outcome(errs)
}

// Chain tries senders in order and stops at the first that takes the
// message. One that is skipped (off, no address or consent, over its cap)
// or fails passes to the next. When a retry finds the message already sent
// on one of a sender's lanes, the chain stops there: a fallback never sends
// on top of a lane that got through.
func Chain(senders ...Sender) Sender { return chain(senders) }

type chain []Sender

func (c chain) Name() string    { return "chain(" + names(c) + ")" }
func (c chain) Lanes() []string { return lanes(c) }

func (c chain) Send(ctx context.Context, m Message) ([]Receipt, error) {
	for _, s := range c {
		for _, l := range s.Lanes() {
			if slices.Contains(m.Sent, l) {
				return []Receipt{{Lane: l, Status: StatusSent, Repeat: true}}, nil
			}
		}
	}
	var all []Receipt
	errs := make([]error, 0, len(c))
	for _, s := range c {
		rcs, err := s.Send(ctx, m)
		all = append(all, rcs...)
		if err == nil {
			return all, nil
		}
		if ctx.Err() != nil {
			return all, Temporary(ctx.Err())
		}
		errs = append(errs, err)
	}
	return all, outcome(errs)
}

// outcome is the error of senders that all failed: temporary when one was,
// skipped when all were, else rejected.
func outcome(errs []error) error {
	if err := firstOf(errs, ErrTemporary); err != nil {
		return err
	}
	for _, err := range errs {
		if !errors.Is(err, ErrSkipped) {
			return errors.Join(errs...)
		}
	}
	if len(errs) == 0 {
		return fmt.Errorf("%w: no lanes", ErrSkipped)
	}
	return errors.Join(errs...)
}

func firstOf(errs []error, target error) error {
	for _, err := range errs {
		if errors.Is(err, target) {
			return err
		}
	}
	return nil
}

func names(ss []Sender) string {
	n := make([]string, len(ss))
	for i, s := range ss {
		n[i] = s.Name()
	}
	return strings.Join(n, ",")
}

func lanes(ss []Sender) []string {
	var out []string
	for _, s := range ss {
		for _, l := range s.Lanes() {
			if !slices.Contains(out, l) {
				out = append(out, l)
			}
		}
	}
	return out
}

// Route sends messages of a kind and severity ("" for any) with Send.
type Route struct {
	Kind     string
	Severity string
	Send     Sender
}

// Router picks a route for each message: the one naming its kind and its
// severity, else its kind, else its severity, else fallback. A nil
// fallback sends nothing beyond the inbox: the message is skipped.
func Router(routes []Route, fallback Sender) Sender {
	return &router{routes: routes, fallback: fallback}
}

type router struct {
	routes   []Route
	fallback Sender
}

func (r *router) Name() string {
	parts := make([]string, 0, len(r.routes)+1)
	for _, rt := range r.routes {
		parts = append(parts, routeKey(rt.Kind, rt.Severity)+"="+rt.Send.Name())
	}
	if r.fallback != nil {
		parts = append(parts, "default="+r.fallback.Name())
	}
	return strings.Join(parts, "; ")
}

func routeKey(kind, sev string) string {
	switch {
	case kind != "" && sev != "":
		return kind + "." + sev
	case kind != "":
		return kind
	}
	return sev
}

func (r *router) Lanes() []string {
	ss := make([]Sender, 0, len(r.routes)+1)
	for _, rt := range r.routes {
		ss = append(ss, rt.Send)
	}
	if r.fallback != nil {
		ss = append(ss, r.fallback)
	}
	return lanes(ss)
}

// Pick is the sender for m, nil when none.
func (r *router) Pick(m Message) Sender {
	best, score := r.fallback, 0
	for _, rt := range r.routes {
		s := 0
		switch {
		case rt.Kind == m.Kind && rt.Severity == m.Severity:
			s = 3
		case rt.Kind == m.Kind && rt.Severity == "":
			s = 2
		case rt.Kind == "" && rt.Severity == m.Severity:
			s = 1
		}
		if s > score {
			best, score = rt.Send, s
		}
	}
	return best
}

func (r *router) Send(ctx context.Context, m Message) ([]Receipt, error) {
	s := r.Pick(m)
	if s == nil {
		return nil, fmt.Errorf("%w: no route for a %s %s", ErrSkipped, m.Severity, m.Kind)
	}
	return s.Send(ctx, m)
}
