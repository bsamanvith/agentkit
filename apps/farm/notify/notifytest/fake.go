// Package notifytest has the conformance suites every notify.Store and
// notify.Lane passes, and a fake lane for tests.
package notifytest

import (
	"context"
	"fmt"
	"sync"

	"github.com/bsamanvith/agentkit/apps/farm/notify"
)

// Fake is a lane for tests: it records what it sends, and fails as told.
type Fake struct {
	info notify.LaneInfo

	mu    sync.Mutex
	sent  []notify.Message
	fails []error
	n     int
}

// NewFake returns a fake lane named name that sends to addresses of kind k.
func NewFake(name string, k notify.AddressKind) *Fake {
	return &Fake{info: notify.LaneInfo{Name: name, Address: k}}
}

// Paid marks it as a paid lane.
func (f *Fake) Paid() *Fake { f.info.Paid = true; return f }

// Info implements notify.Lane.
func (f *Fake) Info() notify.LaneInfo { return f.info }

// FailNext makes the next sends fail with errs, one each, in order.
func (f *Fake) FailNext(errs ...error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fails = append(f.fails, errs...)
}

// Deliver implements notify.Lane.
func (f *Fake) Deliver(ctx context.Context, m notify.Message) (notify.Receipt, error) {
	if err := ctx.Err(); err != nil {
		return notify.Receipt{}, notify.Temporary(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.fails) > 0 {
		err := f.fails[0]
		f.fails = f.fails[1:]
		if err != nil {
			return notify.Receipt{}, err
		}
	}
	if m.Recipient.Address(f.info.Address) == "" {
		return notify.Receipt{}, notify.Rejected(fmt.Errorf("%s: no address", f.info.Name))
	}
	f.n++
	f.sent = append(f.sent, m)
	return notify.Receipt{Status: notify.StatusSent, ProviderID: fmt.Sprintf("%s-%d", f.info.Name, f.n)}, nil
}

// Sent returns the messages it sent.
func (f *Fake) Sent() []notify.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]notify.Message(nil), f.sent...)
}

// Count is how many messages it sent.
func (f *Fake) Count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}
