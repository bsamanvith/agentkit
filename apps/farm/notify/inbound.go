package notify

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/clock"
)

// Reporter is what lanes' webhooks and pollers report: a delivery's new
// status, a person saying STOP or START, a chat to link to a person.
// [Inbound] is the implementation over a store.
type Reporter interface {
	// Status moves the delivery with lane's providerID to status.
	Status(ctx context.Context, lane, providerID, status, errText string)
	// Stop records that the person at addr said STOP on lane.
	Stop(ctx context.Context, lane string, k AddressKind, addr string)
	// Start records that the person at addr said START on lane.
	Start(ctx context.Context, lane string, k AddressKind, addr string)
	// Link sets the address of kind k of the person with link code code to
	// addr and opts them in to lane; it returns their name.
	Link(ctx context.Context, lane, code string, k AddressKind, addr string) (string, error)
}

// Inbound is a [Reporter] over a store.
type Inbound struct {
	store Store
	clock clock.Clock
	log   *slog.Logger
}

// NewInbound returns a reporter that writes to store.
func NewInbound(store Store, c clock.Clock, log *slog.Logger) *Inbound {
	if c == nil {
		c = clock.Real()
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Inbound{store: store, clock: c, log: log}
}

var _ Reporter = (*Inbound)(nil)

// Status implements Reporter.
func (in *Inbound) Status(ctx context.Context, lane, providerID, status, errText string) {
	if providerID == "" || Rank(status) == 0 {
		return
	}
	if _, err := in.store.SetStatus(ctx, lane, providerID, status, errText, in.clock.Now().UTC()); err != nil {
		in.log.Error("notify: delivery status", "lane", lane, "id", providerID, "err", err)
	}
}

func (in *Inbound) consent(ctx context.Context, lane string, k AddressKind, addr string, on bool, source string) {
	r, err := in.store.FindRecipient(ctx, k, addr)
	if errors.Is(err, ErrNotFound) {
		in.log.Info("notify: a message from someone we do not know", "lane", lane)
		return
	}
	if err == nil {
		_, err = in.store.Update(ctx, r.ID, func(r *Recipient) error {
			if r.Consent == nil {
				r.Consent = map[string]Consent{}
			}
			r.Consent[lane] = Consent{OptedIn: on, At: in.clock.Now().UTC(), Source: source}
			return nil
		})
	}
	if err != nil {
		in.log.Error("notify: record consent", "lane", lane, "err", err)
		return
	}
	in.log.Info("notify: consent", "lane", lane, "recipient", r.ID, "opted_in", on, "source", source)
}

// Stop implements Reporter.
func (in *Inbound) Stop(ctx context.Context, lane string, k AddressKind, addr string) {
	in.consent(ctx, lane, k, addr, false, "stop")
}

// Start implements Reporter.
func (in *Inbound) Start(ctx context.Context, lane string, k AddressKind, addr string) {
	in.consent(ctx, lane, k, addr, true, "start")
}

// Link implements Reporter. The code works once.
func (in *Inbound) Link(ctx context.Context, lane, code string, k AddressKind, addr string) (string, error) {
	if len(code) < 8 {
		return "", ErrNotFound
	}
	r, err := in.store.FindRecipient(ctx, "link", code)
	if err != nil {
		return "", err
	}
	r, err = in.store.Update(ctx, r.ID, func(r *Recipient) error {
		if r.LinkCode != code {
			return ErrNotFound
		}
		switch k {
		case AddrTelegram:
			r.Telegram = addr
		case AddrPhone:
			r.Phone = addr
		case AddrEmail:
			r.Email = addr
		default:
			return ErrInvalid
		}
		r.LinkCode = ""
		if r.Consent == nil {
			r.Consent = map[string]Consent{}
		}
		r.Consent[lane] = Consent{OptedIn: true, At: in.clock.Now().UTC(), Source: "link"}
		return nil
	})
	if err != nil {
		return "", err
	}
	in.log.Info("notify: linked", "lane", lane, "recipient", r.ID)
	return r.Name, nil
}

// IsStop reports whether a person's message asks to stop: STOP,
// UNSUBSCRIBE and the like, in English, Telugu and Hindi.
func IsStop(text string) bool {
	t := strings.ToLower(strings.TrimSpace(strings.Trim(strings.TrimSpace(text), ".!/")))
	switch t {
	case "stop", "stop all", "unsubscribe", "cancel", "end", "quit", "optout", "opt out", "opt-out",
		"ఆపు", "ఆపండి", "बंद", "बंद करें", "रोकें":
		return true
	}
	return false
}

// IsStart reports whether a person's message asks to start again.
func IsStart(text string) bool {
	t := strings.ToLower(strings.TrimSpace(strings.Trim(strings.TrimSpace(text), ".!/")))
	switch t {
	case "start", "unstop", "subscribe", "ప్రారంభించు", "शुरू":
		return true
	}
	return false
}
