package agentmsg

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// Addresses.
const (
	// AgentPrefix starts the address of one of the server's agents:
	// "agent:<name>".
	AgentPrefix = "agent:"
	// A2APrefix starts the address of a remote A2A agent, which is not
	// served yet (E13's T068).
	A2APrefix = "a2a:"
	// System is the sender of the notices the dispatcher sends.
	System = "agentkit"
)

// Codes of the errors a send can fail with: what the sender is told, and
// what a stopped thread's [store.Thread.StopCode] says.
const (
	CodeNotAllowed     = "not_allowed"     // the recipient is not in the sender's can_message
	CodeBadAddress     = "bad_address"     // not "agent:<name>" of an agent the server runs
	CodeA2AUnsupported = "a2a_unsupported" // an "a2a:" address, not served yet
	CodeNoThread       = "no_thread"       // a thread that does not exist, or the sender is not part of
	CodeHopLimit       = "hop_limit"       // the thread has as many messages as it may
	CodeTokenBudget    = "token_budget"    // the thread's runs used its token budget
	CodeTimeBudget     = "time_budget"     // the thread is older than its time budget
	CodeThreadStopped  = "thread_stopped"  // the thread was stopped before
	CodeRateLimited    = "rate_limited"    // the sender sent too many messages too fast; retry later
	CodeNoSender       = "no_sender"       // the tool runs outside an agent's run
)

// Error is why a send failed. Code is one of the Code constants.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// ErrInvalidConfig reports an unusable argument to [New] or
// [Messenger.Dispatcher].
var ErrInvalidConfig = errors.New("invalid agent messaging configuration")

// Agent is an agent as messaging knows it.
type Agent struct {
	// Name is the agent's name; its address is "agent:<name>".
	Name string
	// Description says what it is for, in agents_list.
	Description string
	// CanMessage names the agents it may message. An agent with none gets
	// no tools, but can still be messaged.
	CanMessage []string
	// Toolsets names every toolset the agent may use, those added at run
	// time too. An API key may run an agent only if it may use all of them,
	// so an agent may message only agents whose toolsets it has itself:
	// whoever may run the sender may run the recipient, and messaging never
	// lends a caller tools its key does not allow.
	Toolsets []string
}

// Limits bound a thread and an agent's sends. Zero fields take the
// defaults.
type Limits struct {
	// MaxHops is the most messages a thread may have (default
	// [DefaultMaxHops]).
	MaxHops int
	// MaxTokens is the most tokens the runs a thread starts may use in all
	// (default [DefaultMaxTokens]).
	MaxTokens int64
	// MaxDuration is the longest a thread may go on, from its first message
	// (default [DefaultMaxDuration]).
	MaxDuration time.Duration
	// SendEvery and SendBurst rate-limit each agent's sends, per tenant: a
	// burst of SendBurst, then one every SendEvery (defaults
	// [DefaultSendEvery], [DefaultSendBurst]).
	SendEvery time.Duration
	SendBurst int
}

// The defaults of [Limits].
const (
	DefaultMaxHops     = 8
	DefaultMaxTokens   = 500_000
	DefaultMaxDuration = time.Hour
	DefaultSendEvery   = 2 * time.Second
	DefaultSendBurst   = 10
)

func (l Limits) withDefaults() Limits {
	if l.MaxHops == 0 {
		l.MaxHops = DefaultMaxHops
	}
	if l.MaxTokens == 0 {
		l.MaxTokens = DefaultMaxTokens
	}
	if l.MaxDuration == 0 {
		l.MaxDuration = DefaultMaxDuration
	}
	if l.SendEvery == 0 {
		l.SendEvery = DefaultSendEvery
	}
	if l.SendBurst == 0 {
		l.SendBurst = DefaultSendBurst
	}
	return l
}

// Store is what messaging keeps its messages in: the message log, and the
// conversations of the runs it starts, where it checks what it delivered.
// Every backend of package store is one.
type Store interface {
	store.MessageLog
	store.ConversationStore
}

// Messenger sends messages between agents: the tools ([Messenger.Tools])
// and the dispatcher that delivers them ([Messenger.Dispatcher]). It is
// safe for concurrent use. Make one with [New].
type Messenger struct {
	st     Store
	agents map[string]Agent
	limits Limits
	clk    clock.Clock
	newID  func(prefix string) string
	poll   time.Duration
	wait   time.Duration // default wait of agent_message
	maxW   time.Duration // longest wait of agent_message

	mu      sync.Mutex
	buckets map[string]*bucket // by tenant and agent
	changed chan struct{}      // closed and replaced when this process writes a message
}

// Option configures [New].
type Option func(*Messenger)

// WithLimits sets the limits (zero fields keep the defaults).
func WithLimits(l Limits) Option { return func(m *Messenger) { m.limits = l } }

// WithClock sets the clock of budgets, rate limits, retries and waits
// (default [clock.Real]).
func WithClock(c clock.Clock) Option { return func(m *Messenger) { m.clk = c } }

// WithIDs sets the function that makes thread and message IDs from a
// prefix ("thr_", "msg_"); the default is random.
func WithIDs(f func(prefix string) string) Option { return func(m *Messenger) { m.newID = f } }

// WithPoll sets how often a waiting sender and an idle dispatcher read the
// store for what other processes wrote (default 1s). Writes in this process
// wake them at once.
func WithPoll(d time.Duration) Option { return func(m *Messenger) { m.poll = d } }

// WithWait sets how long agent_message waits for a reply when the call gives
// no timeout (default 2 minutes), and the longest it waits whatever the call
// says (default 10 minutes).
func WithWait(def, max time.Duration) Option {
	return func(m *Messenger) { m.wait, m.maxW = def, max }
}

var agentName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

// New returns a messenger over st for agents. It fails with
// [ErrInvalidConfig] for a nil store, an agent with a bad or repeated name,
// a CanMessage naming an agent not in agents or one with a toolset the
// sender does not have, or an option with an unusable value.
func New(st Store, agents []Agent, opts ...Option) (*Messenger, error) {
	m := &Messenger{
		st: st, agents: map[string]Agent{}, clk: clock.Real(), newID: randomID,
		poll: time.Second, wait: 2 * time.Minute, maxW: 10 * time.Minute,
		buckets: map[string]*bucket{}, changed: make(chan struct{}),
	}
	for _, o := range opts {
		o(m)
	}
	m.limits = m.limits.withDefaults()
	l := m.limits
	switch {
	case st == nil:
		return nil, fmt.Errorf("%w: no store", ErrInvalidConfig)
	case m.clk == nil, m.newID == nil:
		return nil, fmt.Errorf("%w: nil clock or ID function", ErrInvalidConfig)
	case l.MaxHops < 1, l.MaxTokens < 1, l.MaxDuration <= 0, l.SendEvery <= 0, l.SendBurst < 1:
		return nil, fmt.Errorf("%w: limits must be positive", ErrInvalidConfig)
	case m.poll <= 0, m.wait <= 0, m.maxW < m.wait:
		return nil, fmt.Errorf("%w: the poll and the waits must be positive, the longest wait at least the default", ErrInvalidConfig)
	}
	for _, a := range agents {
		if !agentName.MatchString(a.Name) {
			return nil, fmt.Errorf("%w: agent name %q", ErrInvalidConfig, a.Name)
		}
		if _, dup := m.agents[a.Name]; dup {
			return nil, fmt.Errorf("%w: agent %q twice", ErrInvalidConfig, a.Name)
		}
		a.CanMessage, a.Toolsets = slices.Clone(a.CanMessage), slices.Clone(a.Toolsets)
		m.agents[a.Name] = a
	}
	for _, a := range m.agents {
		for _, to := range a.CanMessage {
			r, ok := m.agents[to]
			if !ok {
				return nil, fmt.Errorf("%w: agent %q may message %q, which is no agent", ErrInvalidConfig, a.Name, to)
			}
			for _, ts := range r.Toolsets {
				if !slices.Contains(a.Toolsets, ts) {
					return nil, fmt.Errorf("%w: agent %q may message %q, which uses toolset %q that %q does not: a key that may run %q could use it through %q", ErrInvalidConfig, a.Name, to, ts, a.Name, a.Name, to)
				}
			}
		}
	}
	return m, nil
}

// Limits returns the limits in force, defaults filled in.
func (m *Messenger) Limits() Limits { return m.limits }

type senderKey struct{}

// sender is who sends: the agent and the conversation its run is in.
type sender struct{ agent, conversation string }

// WithSender returns ctx with the agent that runs, and its conversation:
// the sender of the messages its tools send. agentkit serve sets it for
// every run through agentapi.WithRunContext.
func WithSender(ctx context.Context, agent, conversationID string) context.Context {
	return context.WithValue(ctx, senderKey{}, sender{agent, conversationID})
}

func senderOf(ctx context.Context) (sender, bool) {
	s, ok := ctx.Value(senderKey{}).(sender)
	return s, ok && s.agent != "" && s.conversation != ""
}

// Address returns the address of the named agent.
func Address(agent string) string { return AgentPrefix + agent }

// parseAddress returns the agent an address names.
func (m *Messenger) parseAddress(to string) (string, error) {
	switch {
	case strings.HasPrefix(to, A2APrefix):
		return "", &Error{CodeA2AUnsupported, "a2a: addresses (remote A2A agents) are not served yet; message one of this server's agents as agent:<name>"}
	case !strings.HasPrefix(to, AgentPrefix):
		return "", &Error{CodeBadAddress, fmt.Sprintf("%q is not an address: want agent:<name>, as agents_list shows", to)}
	}
	name := strings.TrimPrefix(to, AgentPrefix)
	if _, ok := m.agents[name]; !ok {
		return "", &Error{CodeBadAddress, fmt.Sprintf("there is no agent %q", name)}
	}
	return name, nil
}

// allowed reports whether agent from may message agent to.
func (m *Messenger) allowed(from, to string) bool {
	return slices.Contains(m.agents[from].CanMessage, to)
}

// conversationOf returns the conversation agent processes thread's messages
// in: where it sent from or received in before, or a new one for the
// thread and agent.
func (m *Messenger) conversationOf(ctx context.Context, thread, agent string) (string, error) {
	addr := Address(agent)
	msgs, err := m.threadMessages(ctx, thread)
	if err != nil {
		return "", err
	}
	for _, msg := range msgs {
		switch {
		case msg.From == addr && msg.FromConversation != "":
			return msg.FromConversation, nil
		case msg.To == addr:
			return msg.ToConversation, nil
		}
	}
	return threadConversation(thread, agent), nil
}

// threadConversation is the conversation made for agent's messages in
// thread.
func threadConversation(thread, agent string) string { return "conv_" + thread + "_" + agent }

// threadMessages returns every message of the thread, in order.
func (m *Messenger) threadMessages(ctx context.Context, thread string) ([]store.Message, error) {
	var out []store.Message
	for after := 0; ; {
		ms, err := m.st.ThreadMessages(ctx, thread, after, store.MaxPageLimit)
		if err != nil {
			return nil, err
		}
		out = append(out, ms...)
		if len(ms) < store.MaxPageLimit {
			return out, nil
		}
		after = ms[len(ms)-1].Hop
	}
}

// send stores msg after checking its thread's limits, counting tokens the
// thread is about to add. A message already stored (a retry) is returned as
// it is. A send over a limit stops the thread: every conversation in it but
// exclude gets a notice, and the send fails with the limit's [*Error].
func (m *Messenger) send(ctx context.Context, msg store.Message, tokens int64, exclude string) (store.Message, error) {
	if stored, err := m.st.GetMessage(ctx, msg.ID); err == nil {
		return stored, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return store.Message{}, err
	}
	th, err := m.st.GetThread(ctx, msg.ThreadID)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return store.Message{}, err
	case th.Status == store.ThreadStopped:
		return store.Message{}, &Error{CodeThreadStopped, fmt.Sprintf("thread %s was stopped (%s: %s); start a new thread", th.ID, th.StopCode, th.StopReason)}
	default:
		var over *Error
		switch {
		case th.Hops >= m.limits.MaxHops:
			over = &Error{CodeHopLimit, fmt.Sprintf("thread %s has %d messages, the most a thread may have; it is stopped", th.ID, th.Hops)}
		case th.Tokens+tokens >= m.limits.MaxTokens:
			over = &Error{CodeTokenBudget, fmt.Sprintf("thread %s used %d tokens, its budget is %d; it is stopped", th.ID, th.Tokens+tokens, m.limits.MaxTokens)}
		case m.clk.Now().Sub(th.CreatedAt) >= m.limits.MaxDuration:
			over = &Error{CodeTimeBudget, fmt.Sprintf("thread %s started %s ago, its time budget is %s; it is stopped", th.ID, m.clk.Now().Sub(th.CreatedAt).Round(time.Second), m.limits.MaxDuration)}
		}
		if over != nil {
			if err := m.stop(ctx, th.ID, over, exclude); err != nil {
				return store.Message{}, err
			}
			return store.Message{}, over
		}
	}
	out, err := m.st.SendMessage(ctx, msg)
	if errors.Is(err, store.ErrFinal) { // stopped in between
		return store.Message{}, &Error{CodeThreadStopped, fmt.Sprintf("thread %s was stopped; start a new thread", msg.ThreadID)}
	}
	if err != nil {
		return store.Message{}, err
	}
	m.wake()
	return out, nil
}

// stop stops the thread with why, and sends a notice saying so to every
// conversation in it but exclude (whose sender is told by its error).
func (m *Messenger) stop(ctx context.Context, thread string, why *Error, exclude string) error {
	th, err := m.st.StopThread(ctx, thread, why.Code, why.Message)
	if err != nil {
		return err
	}
	msgs, err := m.threadMessages(ctx, thread)
	if err != nil {
		return err
	}
	type party struct{ addr, conv string }
	var parties []party
	add := func(addr, conv string) {
		if conv == "" || conv == exclude || addr == System || slices.Contains(parties, party{addr, conv}) {
			return
		}
		parties = append(parties, party{addr, conv})
	}
	for _, msg := range msgs {
		add(msg.From, msg.FromConversation)
		add(msg.To, msg.ToConversation)
	}
	data, _ := json.Marshal(noticeData{Code: th.StopCode, Message: th.StopReason}) // plain strings: cannot fail
	for _, p := range parties {
		notice := store.Message{
			ID: thread + ".stop." + p.conv, ThreadID: thread, From: System, To: p.addr, ToConversation: p.conv,
			Kind: store.MessageNotice, Text: "This thread was stopped: " + th.StopReason, Data: data,
		}
		if _, err := m.st.SendMessage(ctx, notice); err != nil {
			return err
		}
	}
	m.wake()
	return nil
}

// noticeData is the Data of a stop notice.
type noticeData struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// wake tells this process's waiters and dispatchers that a message was
// written.
func (m *Messenger) wake() {
	m.mu.Lock()
	close(m.changed)
	m.changed = make(chan struct{})
	m.mu.Unlock()
}

// changes returns a channel closed at the next write of a message in this
// process.
func (m *Messenger) changes() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.changed
}

// bucket is a token bucket of one agent's sends.
type bucket struct {
	tokens float64
	at     time.Time
}

// take takes one send of agent from its bucket, or tells how long until one
// is free.
func (m *Messenger) take(ctx context.Context, agent string) error {
	t, _ := tenancy.From(ctx)
	k := t.ID() + "\x00" + agent
	now := m.clk.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.buckets[k]
	if b == nil {
		b = &bucket{tokens: float64(m.limits.SendBurst), at: now}
		m.buckets[k] = b
	}
	b.tokens = min(float64(m.limits.SendBurst), b.tokens+float64(now.Sub(b.at))/float64(m.limits.SendEvery))
	b.at = now
	if b.tokens < 1 {
		wait := time.Duration((1 - b.tokens) * float64(m.limits.SendEvery))
		return &Error{CodeRateLimited, fmt.Sprintf("agent %s sent too many messages too fast; retry in %s", agent, wait.Round(time.Millisecond))}
	}
	b.tokens--
	return nil
}

func randomID(prefix string) string {
	var b [10]byte
	_, _ = rand.Read(b[:])
	return prefix + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
}
