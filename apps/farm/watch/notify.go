package watch

import (
	"context"
	"errors"
	"fmt"

	"github.com/bsamanvith/agentkit/apps/farm/automation"
	"github.com/bsamanvith/agentkit/apps/farm/notify"
)

// Messages is the automations' channel to the message lanes: each new
// notice is queued for every person who hears about its plot, one message
// each, with the notice's ID in the message's ID so queueing it again
// does nothing. The dispatcher then sends it through the lanes' tree; the
// notice is in the inbox whatever happens.
func Messages(d *notify.Dispatcher, link func(farm string) string) automation.Channel {
	return messages{d: d, link: link}
}

type messages struct {
	d    *notify.Dispatcher
	link func(farm string) string
}

func (messages) Name() string { return "messages" }

func (c messages) Deliver(ctx context.Context, n automation.Notice) error {
	rs, err := c.d.Store().RecipientsFor(ctx, n.Farm)
	if err != nil {
		return err
	}
	var errs []error
	for _, r := range rs {
		m := notify.Message{
			ID: n.ID + ":" + r.ID, To: r.ID, Farm: n.Farm, Notice: n.ID, Kind: notify.KindAlert,
			Severity: n.Severity, Title: n.Title, Body: n.Body, CreatedAt: n.CreatedAt,
		}
		if c.link != nil {
			m.Link = c.link(n.Farm)
		}
		if _, err := c.d.Enqueue(ctx, m); err != nil {
			errs = append(errs, fmt.Errorf("to %s: %w", r.ID, err))
		}
	}
	return errors.Join(errs...)
}
