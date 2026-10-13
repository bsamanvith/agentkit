package stream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"

	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// TypeResync is the event [WatchUser] sends after notifications were lost (the
// topic's history was trimmed or lost): the client refetches what it shows.
// Its ID is where the watch goes on from, so a client that reconnects with it
// as its Last-Event-ID does not get the event again.
const TypeResync = "resync"

// maxStaleRetries bounds how often Notify picks a new ID when other
// publishers keep taking the one it chose.
const maxStaleRetries = 8

// Notifier publishes notifications on users' topics. Any process may publish
// to any user's topic. It is safe for concurrent use.
type Notifier struct {
	bus bus.Bus
	cfg config
}

// NewNotifier returns a notifier over b.
func NewNotifier(b bus.Bus, opts ...Option) (*Notifier, error) {
	if b == nil {
		return nil, fmt.Errorf("%w: nil bus", ErrInvalid)
	}
	return &Notifier{bus: b, cfg: newConfig(opts)}, nil
}

// Notify publishes a notification to user userID in ctx's tenant and returns
// its ID. IDs are the publish time in Unix milliseconds, with N telling apart
// the notifications of one millisecond; when another process got there first,
// Notify takes the next ID after the topic's last. Notifications are not
// stored: an error means it may not have gone out.
func (n *Notifier) Notify(ctx context.Context, userID, typ string, data json.RawMessage) (bus.ID, error) {
	msg, err := encode(typ, data)
	if err != nil {
		return bus.ID{}, err
	}
	topic, err := userTopic(ctx, userID)
	if err != nil {
		return bus.ID{}, err
	}
	id := bus.ID{Seq: uint64(max(n.cfg.clk.Now().UnixMilli(), 1))}
	for range maxStaleRetries {
		err = n.bus.Publish(ctx, topic, bus.Message{ID: id, Data: msg})
		if !errors.Is(err, bus.ErrStale) {
			break
		}
		last, lerr := n.bus.Last(ctx, topic)
		if lerr != nil {
			return bus.ID{}, fmt.Errorf("stream: notify: %w", lerr)
		}
		id = after(id, last)
	}
	if err != nil {
		return bus.ID{}, fmt.Errorf("stream: notify: %w", err)
	}
	return id, nil
}

// after returns the ID to try when id was stale and last is the topic's last.
func after(id, last bus.ID) bus.ID {
	if id.Seq > last.Seq {
		return id
	}
	return bus.ID{Seq: last.Seq, N: last.N + 1}
}

func userTopic(ctx context.Context, userID string) (bus.Topic, error) {
	t, err := tenancy.Require(ctx)
	if err != nil {
		return bus.Topic{}, err
	}
	topic, err := bus.UserTopic(t, userID)
	if err != nil {
		return bus.Topic{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return topic, nil
}

// WatchUser follows the notifications of user userID in ctx's tenant after
// the given ID, or from now for the zero ID. When the ones after the given
// ID are no longer kept, or some are lost on the way, it sends a
// [TypeResync] event and goes on from the topic's newest. The sequence ends
// only with an error: ctx's when it is done, a bus error, or [ErrInvalid].
func WatchUser(ctx context.Context, b bus.Bus, userID string, from bus.ID) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		if b == nil {
			yield(Event{}, fmt.Errorf("%w: nil bus", ErrInvalid))
			return
		}
		topic, err := userTopic(ctx, userID)
		if err != nil {
			yield(Event{}, err)
			return
		}
		cursor := from
		if cursor.IsZero() {
			if cursor, err = b.Last(ctx, topic); err != nil {
				yield(Event{}, fmt.Errorf("stream: watch user: %w", err))
				return
			}
		}
		for {
			err := followUser(ctx, b, topic, &cursor, yield)
			if errors.Is(err, errStop) {
				return
			}
			if !errors.Is(err, bus.ErrGap) {
				yield(Event{}, err)
				return
			}
			last, err := b.Last(ctx, topic)
			if err != nil {
				yield(Event{}, fmt.Errorf("stream: watch user: %w", err))
				return
			}
			if last.Compare(cursor) > 0 {
				cursor = last
			}
			if !yield(Event{ID: cursor, Type: TypeResync}, nil) {
				return
			}
		}
	}
}

func followUser(ctx context.Context, b bus.Bus, topic bus.Topic, cursor *bus.ID, yield func(Event, error) bool) error {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	sub, err := b.Subscribe(sctx, topic, *cursor)
	if err != nil {
		return fmt.Errorf("stream: watch user: %w", err)
	}
	defer func() {
		cancel()
		for range sub.C() {
		}
	}()
	for m := range sub.C() {
		e, err := decode(m)
		if err != nil {
			return err
		}
		*cursor = e.ID
		if !yield(e, nil) {
			return errStop
		}
	}
	if err := sub.Err(); err != nil && !errors.Is(err, bus.ErrGap) {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("stream: watch user: %w", err)
	} else if err != nil {
		return bus.ErrGap
	}
	return ctx.Err()
}
