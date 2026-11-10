package notify_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/notify"
)

// TestSendCode: a sign-in code goes at once to an address no recipient has,
// by the first phone lane under its cap, then the next; the person's
// stored consent (here: none) does not matter, the records do not keep the
// address, and the code travels alone for the lanes' templates.
func TestSendCode(t *testing.T) {
	w := newWorld(t, ravi()) // Ravi agreed to nothing
	w.opts["sms"] = []notify.GuardOption{notify.WithCost(25), notify.WithCap(notify.Cap{Day: 1})}
	g := w.guarded()
	phone := notify.Chain(g["sms"], g["whatsapp"])
	const to = "+919876543210"
	for i, want := range []string{"sms", "whatsapp"} {
		rcs, err := notify.SendCode(w.ctx, phone, to, "123456", "123456 is your sign-in code.", t0)
		if err != nil {
			t.Fatalf("code %d: %v", i+1, err)
		}
		if last := rcs[len(rcs)-1]; last.Lane != want || !notify.Accepted(last.Status) {
			t.Errorf("code %d: receipts %+v, want it sent by %s (SMS's cap is one a day)", i+1, rcs, want)
		}
	}
	m := w.lanes["sms"].Sent()[0]
	if m.Kind != notify.KindCode || m.Code != "123456" || m.Body != "123456 is your sign-in code." || m.Severity != notify.Urgent || m.Recipient.Phone != to {
		t.Errorf("the SMS lane got %+v", m)
	}
	if strings.Contains(m.To, "9876543210") || m.To == "ravi" {
		t.Errorf("recipient %q: want one made for the code, named without the number", m.To)
	}
	if n, cost, _ := w.store.Spend(w.ctx, "sms", t0.Add(-1)); n != 1 || cost != 25 {
		t.Errorf("SMS spend %d, %d paise; want the code counted against the cap: 1, 25", n, cost)
	}

	if _, err := notify.SendCode(w.ctx, g["email"], "Ravi@Example.com", "654321", "654321 is your sign-in code.", t0); err != nil {
		t.Fatal(err)
	}
	if e := w.lanes["email"].Sent(); len(e) != 1 || e[0].Title == "" || e[0].Recipient.Email != "Ravi@Example.com" {
		t.Errorf("email lane got %+v, want one message with a subject", e)
	}
	// The email lane cannot reach a phone, nor a phone lane an email.
	if _, err := notify.SendCode(w.ctx, g["email"], to, "1", "1", t0); !errors.Is(err, notify.ErrSkipped) {
		t.Errorf("a phone on the email lane: %v, want ErrSkipped", err)
	}
	if _, err := notify.SendCode(w.ctx, phone, "not an address", "1", "1", t0); !errors.Is(err, notify.ErrInvalid) {
		t.Errorf("a bad address: %v, want ErrInvalid", err)
	}
}
