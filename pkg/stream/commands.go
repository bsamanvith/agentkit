package stream

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// resubscribeDelay is how long an [Inbox] waits before following the
// command topic again after its subscription failed.
const resubscribeDelay = time.Second

// Commands sends commands to runs and opens the inbox the run's process
// reads them from. It is safe for concurrent use.
type Commands struct {
	log store.CommandLog
	bus bus.Bus
	cfg config
}

// NewCommands returns commands over log and b.
func NewCommands(log store.CommandLog, b bus.Bus, opts ...Option) (*Commands, error) {
	if log == nil || b == nil {
		return nil, fmt.Errorf("%w: nil command log or bus", ErrInvalid)
	}
	return &Commands{log: log, bus: b, cfg: newConfig(opts)}, nil
}

// Send stores c as run runID's next command in ctx's tenant (see
// [store.CommandLog.SendCommand], whose errors it returns), then signals the
// run's command topic so the process running it reads it soon. A failed
// signal is logged, not returned: the command is stored, and the process
// reads it at its next turn boundary anyway.
func (c *Commands) Send(ctx context.Context, runID string, cmd store.Command) (store.Command, error) {
	topic, err := commandTopic(ctx, runID)
	if err != nil {
		return store.Command{}, err
	}
	stored, err := c.log.SendCommand(ctx, runID, cmd)
	if err != nil {
		return store.Command{}, fmt.Errorf("stream: send command to run %q: %w", runID, err)
	}
	err = c.bus.Publish(ctx, topic, bus.Message{ID: bus.ID{Seq: uint64(stored.Seq)}, Data: []byte(stored.Type)})
	if err != nil && !errors.Is(err, bus.ErrStale) {
		c.cfg.log.WarnContext(ctx, "stream: signalling a command failed; the run reads it at its next turn",
			slog.String("run", runID), slog.Int64("seq", stored.Seq), slog.Any("err", err))
	}
	return stored, nil
}

func commandTopic(ctx context.Context, runID string) (bus.Topic, error) {
	t, err := tenancy.Require(ctx)
	if err != nil {
		return bus.Topic{}, err
	}
	topic, err := bus.CommandTopic(t, runID)
	if err != nil {
		return bus.Topic{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return topic, nil
}

// Open returns the inbox of run runID in ctx's tenant for owner, the holder
// of the run's lease. The inbox follows the run's command topic until ctx is
// done or Close is called; ctx also scopes its store calls.
//
// The store is the inbox's truth: Pending reads it, so commands sent before
// Open, while the signal was lost, or to a process that crashed before
// consuming them, are all there.
func (c *Commands) Open(ctx context.Context, runID, owner string) (*Inbox, error) {
	topic, err := commandTopic(ctx, runID)
	if err != nil {
		return nil, err
	}
	if owner == "" {
		return nil, fmt.Errorf("%w: empty owner", ErrInvalid)
	}
	ctx, cancel := context.WithCancel(ctx)
	in := &Inbox{c: c, ctx: ctx, cancel: cancel, runID: runID, owner: owner, topic: topic, wake: make(chan struct{}, 1)}
	in.done.Add(1)
	go in.follow()
	return in, nil
}

// Inbox is a run's commands, for the process running it.
type Inbox struct {
	c      *Commands
	ctx    context.Context
	cancel context.CancelFunc
	runID  string
	owner  string
	topic  bus.Topic
	wake   chan struct{}
	done   sync.WaitGroup
}

// Wake receives when a command was signalled since the last receive. It is a
// hint to call Pending sooner: a command can be pending without a wake, and a
// wake can find nothing new.
func (in *Inbox) Wake() <-chan struct{} { return in.wake }

// Pending returns up to limit of the run's commands not yet consumed, in
// order (limit is read as [store.Page.Limit] is).
func (in *Inbox) Pending(limit int) ([]store.Command, error) {
	cmds, err := in.c.log.PendingCommands(in.ctx, in.runID, limit)
	if err != nil {
		return nil, fmt.Errorf("stream: pending commands of run %q: %w", in.runID, err)
	}
	return cmds, nil
}

// Consume marks the run's commands up to and including Seq through as
// consumed, once the run acted on them (see
// [store.CommandLog.ConsumeCommands] for its errors).
func (in *Inbox) Consume(through int64) error {
	if err := in.c.log.ConsumeCommands(in.ctx, in.runID, in.owner, through); err != nil {
		return fmt.Errorf("stream: consume commands of run %q: %w", in.runID, err)
	}
	return nil
}

// Close stops following the command topic and waits for it to stop. The
// inbox's store calls fail after it.
func (in *Inbox) Close() {
	in.cancel()
	in.done.Wait()
}

// follow turns the signals on the command topic into wakes. It starts after
// the topic's last message, since Pending reads what came before, and goes
// on after the newest one on any failure: a lost signal costs only latency.
func (in *Inbox) follow() {
	defer in.done.Done()
	for in.ctx.Err() == nil {
		err := in.followOnce()
		if in.ctx.Err() != nil {
			return
		}
		in.notify() // something may have come in unseen
		if !errors.Is(err, bus.ErrGap) {
			in.c.cfg.log.WarnContext(in.ctx, "stream: following run commands failed",
				slog.String("run", in.runID), slog.Any("err", err))
			t := in.c.cfg.clk.NewTimer(resubscribeDelay)
			select {
			case <-in.ctx.Done():
				t.Stop()
				return
			case <-t.C():
			}
		}
	}
}

func (in *Inbox) followOnce() error {
	last, err := in.c.bus.Last(in.ctx, in.topic)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(in.ctx)
	defer cancel()
	sub, err := in.c.bus.Subscribe(ctx, in.topic, last)
	if err != nil {
		return err
	}
	for range sub.C() {
		in.notify()
	}
	return sub.Err()
}

func (in *Inbox) notify() {
	select {
	case in.wake <- struct{}{}:
	default:
	}
}
