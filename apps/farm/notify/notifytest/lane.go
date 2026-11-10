package notifytest

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/notify"
)

// Failure kinds a lane's provider can be told to answer with.
const (
	FailTemporary = "temporary" // a 5xx, a rate limit
	FailRejected  = "rejected"  // a bad number, an unknown template
	FailGone      = "gone"      // blocked, unsubscribed, expired
)

// LaneHarness is a lane over a fake of its provider.
type LaneHarness struct {
	Lane notify.Lane
	// To is a recipient with an address on the lane.
	To notify.Recipient
	// Fail makes the provider answer the next send with a failure of kind;
	// it returns false for a kind the provider cannot give.
	Fail func(kind string) bool
	// Got is what the provider was sent so far, one string per message:
	// the request, as text.
	Got func() []string
}

func msg(id string) notify.Message {
	return notify.Message{
		ID: id, To: "r1", Kind: notify.KindAlert, Severity: notify.Urgent,
		Title: "Heavy rain Thursday", Body: "Clear the drains on the east plot.",
		CreatedAt: time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC),
	}
}

// RunLane runs the suite on lanes from open, each over a fresh fake.
func RunLane(t *testing.T, open func(t *testing.T) LaneHarness) {
	t.Helper()
	ctx := context.Background()

	t.Run("info", func(t *testing.T) {
		h := open(t)
		info := h.Lane.Info()
		if info.Name == "" || info.Address == "" || info.Note == "" {
			t.Errorf("Info = %+v: want a name, an address kind and a note", info)
		}
		if h.To.Address(info.Address) == "" {
			t.Fatalf("the harness's recipient has no %s address", info.Address)
		}
	})

	t.Run("sends the message to the address", func(t *testing.T) {
		h := open(t)
		m := msg("m1")
		m.Recipient = h.To
		rc, err := h.Lane.Deliver(ctx, m)
		if err != nil {
			t.Fatalf("Deliver: %v", err)
		}
		if !notify.Accepted(rc.Status) && rc.Status != "" {
			t.Errorf("receipt status %q: want sent, delivered or empty", rc.Status)
		}
		got := h.Got()
		if len(got) != 1 {
			t.Fatalf("the provider got %d messages, want 1", len(got))
		}
		addr := h.To.Address(h.Lane.Info().Address)
		addr = strings.TrimPrefix(addr, "+")
		if h.Lane.Info().Address != notify.AddrPush && !strings.Contains(got[0], addr) {
			t.Errorf("the request does not name the address %q:\n%s", addr, got[0])
		}
		if !strings.Contains(got[0], "Heavy rain") {
			t.Errorf("the request does not carry the title:\n%s", got[0])
		}
	})

	t.Run("no address is refused before the provider", func(t *testing.T) {
		h := open(t)
		m := msg("m2")
		m.Recipient = notify.Recipient{ID: "r1"}
		if _, err := h.Lane.Deliver(ctx, m); err == nil || errors.Is(err, notify.ErrTemporary) {
			t.Errorf("Deliver with no address: %v, want a final error", err)
		}
		if n := len(h.Got()); n != 0 {
			t.Errorf("the provider got %d messages", n)
		}
	})

	for _, c := range []struct {
		kind string
		want error
	}{
		{FailTemporary, notify.ErrTemporary},
		{FailRejected, notify.ErrRejected},
		{FailGone, notify.ErrGone},
	} {
		t.Run("a "+c.kind+" failure", func(t *testing.T) {
			h := open(t)
			if !h.Fail(c.kind) {
				t.Skipf("the provider has no %s failure", c.kind)
			}
			m := msg("m3")
			m.Recipient = h.To
			_, err := h.Lane.Deliver(ctx, m)
			if !errors.Is(err, c.want) {
				t.Errorf("Deliver: %v, want %v", err, c.want)
			}
			for _, other := range []error{notify.ErrTemporary, notify.ErrRejected, notify.ErrGone} {
				if !errors.Is(other, c.want) && errors.Is(err, other) {
					t.Errorf("Deliver: %v is also %v", err, other)
				}
			}
		})
	}

	t.Run("a cancelled context", func(t *testing.T) {
		h := open(t)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		m := msg("m4")
		m.Recipient = h.To
		if _, err := h.Lane.Deliver(cctx, m); err == nil || errors.Is(err, notify.ErrRejected) {
			t.Errorf("Deliver with a cancelled context: %v, want a temporary error", err)
		}
	})
}
