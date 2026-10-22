package agentmsg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// Runs is what the dispatcher starts and follows runs with. *agentapi.Server
// is one.
type Runs interface {
	// Deliver starts a background run of agent on input in the
	// conversation conversationID, of the tenant in ctx, making the
	// conversation (titled title) if there is none, and returns the run as
	// stored with its prompt. A conversation with an unfinished run gives an
	// error matching [runner.ErrRunUnfinished].
	Deliver(ctx context.Context, agent, conversationID, title, input string) (store.Run, error)
	// RunResult returns the run, with FinishedAt set once it has ended for
	// good, with its answer or error.
	RunResult(ctx context.Context, runID string) (wire.Run, error)
}

// Dispatcher delivers the messages of every tenant: it claims the next due
// message ([store.MessageAdmin.ClaimDueMessage]) and acts on it, until none
// is due, then waits for a write in this process or its poll. Run as many as
// you like, in as many processes: leases share the work. Make one with
// [Messenger.Dispatcher].
type Dispatcher struct {
	m     *Messenger
	admin store.MessageAdmin
	runs  Runs
	owner string
	ttl   time.Duration
	retry time.Duration
	log   *slog.Logger
}

// DispatcherOption configures [Messenger.Dispatcher].
type DispatcherOption func(*Dispatcher)

// WithOwner sets the dispatcher's lease owner (default random).
func WithOwner(owner string) DispatcherOption { return func(d *Dispatcher) { d.owner = owner } }

// WithLease sets how long a claim lasts before another dispatcher may take
// the message over (default 1 minute).
func WithLease(ttl time.Duration) DispatcherOption { return func(d *Dispatcher) { d.ttl = ttl } }

// WithRetry sets how long a message waits before it is tried again when its
// recipient is busy, and between looks at the run that answers a request
// (default 1s).
func WithRetry(d time.Duration) DispatcherOption { return func(dp *Dispatcher) { dp.retry = d } }

// WithLogger sets the logger (default: none).
func WithLogger(l *slog.Logger) DispatcherOption { return func(d *Dispatcher) { d.log = l } }

// Dispatcher returns a dispatcher of m's messages, which claims them through
// admin and runs their recipients through runs.
func (m *Messenger) Dispatcher(admin store.MessageAdmin, runs Runs, opts ...DispatcherOption) (*Dispatcher, error) {
	d := &Dispatcher{m: m, admin: admin, runs: runs, ttl: time.Minute, retry: time.Second, log: slog.New(slog.DiscardHandler)}
	for _, o := range opts {
		o(d)
	}
	if d.owner == "" {
		d.owner = m.newID("dispatch_")
	}
	switch {
	case admin == nil, runs == nil:
		return nil, fmt.Errorf("%w: a dispatcher needs the message admin and the runs", ErrInvalidConfig)
	case d.ttl <= 0, d.retry <= 0, d.log == nil:
		return nil, fmt.Errorf("%w: the lease and the retry must be positive, the logger not nil", ErrInvalidConfig)
	}
	return d, nil
}

// Run delivers messages until ctx is done, and then returns nil. A failure
// to read the store is logged, and tried again at the next poll.
func (d *Dispatcher) Run(ctx context.Context) error {
	for {
		changed := d.m.changes()
		for {
			worked, err := d.Step(ctx)
			if ctx.Err() != nil {
				return nil
			}
			if err != nil {
				d.log.Error("delivering messages", "err", err)
				break
			}
			if !worked {
				break
			}
		}
		t := d.m.clk.NewTimer(d.m.poll)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-changed:
		case <-t.C():
		}
		t.Stop()
	}
}

// Step claims the next due message and acts on it. It reports false when no
// message was due.
func (d *Dispatcher) Step(ctx context.Context) (bool, error) {
	tm, err := d.admin.ClaimDueMessage(ctx, d.owner, d.ttl)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	ctx = tenancy.With(ctx, tm.Tenant)
	m := tm.Message
	if m.State == store.MessageQueued {
		err = d.deliver(ctx, m)
	} else {
		err = d.answer(ctx, m)
	}
	if err != nil {
		return true, fmt.Errorf("message %s of tenant %s: %w", m.ID, tm.Tenant.ID(), err)
	}
	return true, nil
}

// update records u on m and wakes this process.
func (d *Dispatcher) update(ctx context.Context, m store.Message, u store.MessageUpdate) error {
	if _, err := d.m.st.UpdateMessage(ctx, m.ID, d.owner, u); err != nil {
		return err
	}
	d.m.wake()
	return nil
}

// later is the update that leaves m as it is, due again after the retry.
func (d *Dispatcher) later(m store.Message) store.MessageUpdate {
	return store.MessageUpdate{State: m.State, NextAttemptAt: d.m.clk.Now().Add(d.retry)}
}

// deliver hands a queued message to its recipient: a background run in its
// conversation, unless one was started for it before (by a dispatcher that
// stopped before it recorded so), then waits for the run's end (answer).
func (d *Dispatcher) deliver(ctx context.Context, m store.Message) error {
	agent := strings.TrimPrefix(m.To, AgentPrefix)
	runID, err := d.delivered(ctx, m)
	if err != nil {
		return err
	}
	if runID == "" {
		run, err := d.runs.Deliver(ctx, agent, m.ToConversation, "Thread "+m.ThreadID+" from "+m.From, envelope(m))
		switch {
		case errors.Is(err, runner.ErrRunUnfinished):
			return d.update(ctx, m, d.later(m)) // its recipient is busy: in turn, once it is free
		case err != nil:
			if uerr := d.update(ctx, m, d.later(m)); uerr != nil {
				return errors.Join(err, uerr)
			}
			return err
		}
		runID = run.ID
		d.log.Info("message delivered", "message_id", m.ID, "thread_id", m.ThreadID, "to", m.To, "run_id", runID)
	}
	// Every kind waits for its run's end, whose tokens the thread counts: a
	// reply's run (a wake-up) and a notice's are the thread's too.
	return d.update(ctx, m, store.MessageUpdate{State: store.MessageRunning, RunID: runID, NextAttemptAt: d.m.clk.Now().Add(d.retry)})
}

// maxClockSkew is how far apart the clocks of the replicas that write one
// store may be: how far before a message's first delivery [delivered] still
// looks for its run's prompt, which another replica's clock may have
// stamped.
const maxClockSkew = time.Minute

// delivered returns the run started for m, if its conversation has a prompt
// carrying m since m was first delivered (allowing for skewed clocks); ""
// if none has.
func (d *Dispatcher) delivered(ctx context.Context, m store.Message) (string, error) {
	since := m.DeliveredAt.Add(-maxClockSkew)
	p := store.Page{Order: store.Descending, Limit: 50}
	for {
		page, err := d.m.st.History(ctx, m.ToConversation, p)
		if err != nil {
			return "", err
		}
		for _, e := range page.Entries {
			if e.CreatedAt.Before(since) {
				return "", nil
			}
			if e.Kind != store.KindPrompt || len(e.Messages) == 0 {
				continue
			}
			if env, ok := ParseEnvelope(textOf(e.Messages[0])); ok && env.ID == m.ID {
				return e.RunID, nil
			}
		}
		if page.Next == "" {
			return "", nil
		}
		p.Cursor = page.Next
	}
}

func textOf(msg llm.Message) string {
	var b strings.Builder
	for _, bl := range msg.Content {
		if t, ok := bl.(llm.Text); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

// answer acts on a message whose run is going on: once the run has ended,
// its tokens count in the thread, and a request's answer goes back to the
// sender as the reply.
func (d *Dispatcher) answer(ctx context.Context, m store.Message) error {
	run, err := d.runs.RunResult(ctx, m.RunID)
	if errors.Is(err, store.ErrNotFound) { // its conversation was deleted
		return d.update(ctx, m, store.MessageUpdate{State: store.MessageDone})
	}
	if err != nil {
		return err
	}
	if run.FinishedAt == nil {
		return d.update(ctx, m, d.later(m))
	}
	tokens := int64(run.Usage.InputTokens + run.Usage.OutputTokens + run.Usage.CacheReadTokens + run.Usage.CacheWriteTokens)
	if m.Kind == store.MessageRequest && m.FromConversation != "" {
		reply := store.Message{
			ID: m.ID + ".reply", ThreadID: m.ThreadID, From: m.To, To: m.From,
			FromConversation: m.ToConversation, ToConversation: m.FromConversation,
			Kind: store.MessageReply, Text: run.Answer, InReplyTo: m.ID,
		}
		if run.Error != nil {
			reply.Data = errorData(run.Error)
		}
		var over *Error
		if _, err := d.m.send(ctx, reply, tokens, ""); err != nil && !errors.As(err, &over) {
			return err
		}
		// Over a limit: the thread is stopped, and both sides were sent a
		// notice instead of the reply.
	}
	return d.update(ctx, m, store.MessageUpdate{State: store.MessageDone, Tokens: tokens})
}

// errorData is the Data of a reply to a request whose run failed.
func errorData(e *wire.Error) []byte {
	b, _ := json.Marshal(noticeData{Code: e.Code, Message: e.Message}) // plain strings: cannot fail
	return b
}
