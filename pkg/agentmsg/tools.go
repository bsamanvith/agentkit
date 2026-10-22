package agentmsg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// The tools' names.
const (
	ListToolName    = "agents_list"
	MessageToolName = "agent_message"
	ReadToolName    = "thread_read"
)

// ToolNames are the names of the tools an agent that may message gets.
var ToolNames = []string{ListToolName, MessageToolName, ReadToolName}

// maxText is the longest message text agent_message takes, in bytes.
const maxText = 64 << 10

// waitOwner leases the replies a waiting sender takes.
const waitTTL = time.Minute

// Tools returns the tools of the named agent: agents_list, agent_message and
// thread_read, or none for an agent with no CanMessage. The tools find the
// sender in their context ([WithSender]).
func (m *Messenger) Tools(agent string) ([]tool.Tool, error) {
	a, ok := m.agents[agent]
	if !ok {
		return nil, fmt.Errorf("%w: no agent %q", ErrInvalidConfig, agent)
	}
	if len(a.CanMessage) == 0 {
		return nil, nil
	}
	list, err := tool.Func(ListToolName,
		"List the agents you may message with agent_message, with what each is for.",
		func(ctx context.Context, _ struct{}) (agentsOut, error) { return m.list(agent), nil },
		tool.WithMetadata(tool.Metadata{Kind: tool.KindRead, Risk: tool.RiskLow}))
	if err != nil {
		return nil, err
	}
	send, err := tool.Func(MessageToolName,
		"Send a message to another agent (an address from agents_list, agent:<name>). "+
			"Without a thread it starts a new one; give the thread of an earlier message to continue it. "+
			"With wait, the call waits for the reply (the recipient's final answer) and returns it, "+
			"or returns status timeout with the thread if it does not come in time; the reply then arrives later as a new message. "+
			"Without wait, it returns the thread at once, and the reply arrives later as a new message that starts a new turn.",
		func(ctx context.Context, in messageIn) (messageOut, error) { return m.message(ctx, agent, in) },
		tool.WithMetadata(tool.Metadata{Kind: tool.KindSend, Risk: tool.RiskLow}))
	if err != nil {
		return nil, err
	}
	read, err := tool.Func(ReadToolName,
		"Read the messages of a thread you are part of, oldest first.",
		func(ctx context.Context, in readIn) (threadOut, error) { return m.read(ctx, agent, in) },
		tool.WithMetadata(tool.Metadata{Kind: tool.KindRead, Risk: tool.RiskLow}))
	if err != nil {
		return nil, err
	}
	return []tool.Tool{list, send, read}, nil
}

type agentsOut struct {
	Agents []agentOut `json:"agents"`
}

type agentOut struct {
	Address     string `json:"address"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

func (m *Messenger) list(agent string) agentsOut {
	out := agentsOut{Agents: []agentOut{}}
	names := slices.Clone(m.agents[agent].CanMessage)
	slices.Sort(names)
	for _, n := range slices.Compact(names) {
		out.Agents = append(out.Agents, agentOut{Address: Address(n), Name: n, Description: m.agents[n].Description})
	}
	return out
}

type messageIn struct {
	To      string `json:"to" jsonschema:"the recipient's address, agent:<name>, as agents_list shows it"`
	Text    string `json:"text" jsonschema:"the message: self-contained, since the recipient knows nothing else of your conversation"`
	Thread  string `json:"thread,omitempty" jsonschema:"the thread to continue (from an earlier message or reply); leave out to start a new thread"`
	Wait    bool   `json:"wait,omitempty" jsonschema:"wait for the reply and return it (default false: return at once; the reply comes later as a new message)"`
	Timeout int    `json:"timeout,omitempty" jsonschema:"with wait, the most seconds to wait (default 120)"`
}

type messageOut struct {
	// Status is "sent" (no wait), "replied" or "timeout".
	Status  string    `json:"status"`
	Thread  string    `json:"thread"`
	Message string    `json:"message"`
	Reply   *replyOut `json:"reply,omitempty"`
}

type replyOut struct {
	From  string     `json:"from"`
	Text  string     `json:"text"`
	Error *ErrorData `json:"error,omitempty"`
}

// message is agent_message: it sends, and with Wait waits for the reply.
func (m *Messenger) message(ctx context.Context, agent string, in messageIn) (messageOut, error) {
	from, ok := senderOf(ctx)
	if !ok || from.agent != agent {
		return messageOut{}, &Error{CodeNoSender, "agent_message runs only in a run of the agent it belongs to"}
	}
	to, err := m.parseAddress(strings.TrimSpace(in.To))
	if err != nil {
		return messageOut{}, err
	}
	switch {
	case !m.allowed(agent, to):
		return messageOut{}, &Error{CodeNotAllowed, fmt.Sprintf("agent %s may not message %s; agents_list shows whom it may", agent, Address(to))}
	case strings.TrimSpace(in.Text) == "":
		return messageOut{}, fmt.Errorf("%w: the text is empty", tool.ErrInvalidInput)
	case len(in.Text) > maxText:
		return messageOut{}, fmt.Errorf("%w: the text is longer than %d bytes", tool.ErrInvalidInput, maxText)
	case in.Timeout < 0:
		return messageOut{}, fmt.Errorf("%w: a negative timeout", tool.ErrInvalidInput)
	}
	thread := in.Thread
	if thread == "" {
		// A run that a message started continues that message's thread:
		// otherwise each run of a loop between agents could start a fresh
		// thread, and the loop would never meet the hop limit.
		if thread, err = m.triggeredBy(ctx, from); err != nil {
			return messageOut{}, err
		}
	}
	toConv := ""
	if thread == "" {
		thread = m.newID("thr_")
		toConv = threadConversation(thread, to)
	} else {
		if !m.partOf(ctx, thread, agent) {
			return messageOut{}, &Error{CodeNoThread, fmt.Sprintf("no thread %q that agent %s is part of", thread, agent)}
		}
		if toConv, err = m.conversationOf(ctx, thread, to); err != nil {
			return messageOut{}, err
		}
	}
	if err := m.take(ctx, agent); err != nil {
		return messageOut{}, err
	}
	sent, err := m.send(ctx, store.Message{
		ID: m.newID("msg_"), ThreadID: thread, From: Address(agent), To: Address(to),
		FromConversation: from.conversation, ToConversation: toConv, Kind: store.MessageRequest, Text: in.Text,
	}, 0, from.conversation)
	if err != nil {
		return messageOut{}, err
	}
	out := messageOut{Status: "sent", Thread: thread, Message: sent.ID}
	if !in.Wait {
		return out, nil
	}
	wait := m.wait
	if in.Timeout > 0 {
		wait = min(time.Duration(in.Timeout)*time.Second, m.maxW)
	}
	return m.await(ctx, sent, from.conversation, wait, out)
}

// triggeredBy returns the thread of the message that started the sender's
// run, or "" when no message did. A conversation runs one run at a time, so
// the run's prompt is the conversation's latest; the run was started by a
// message when that prompt carries a message of the log sent to this
// conversation.
func (m *Messenger) triggeredBy(ctx context.Context, from sender) (string, error) {
	p := store.Page{Order: store.Descending, Limit: 50}
	for {
		page, err := m.st.History(ctx, from.conversation, p)
		if errors.Is(err, store.ErrNotFound) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		for _, e := range page.Entries {
			if e.Kind != store.KindPrompt {
				continue
			}
			if len(e.Messages) == 0 {
				return "", nil
			}
			env, ok := ParseEnvelope(textOf(e.Messages[0]))
			if !ok {
				return "", nil
			}
			msg, err := m.st.GetMessage(ctx, env.ID)
			switch {
			case errors.Is(err, store.ErrNotFound):
				return "", nil
			case err != nil:
				return "", err
			case msg.ToConversation != from.conversation:
				return "", nil
			}
			return msg.ThreadID, nil
		}
		if page.Next == "" {
			return "", nil
		}
		p.Cursor = page.Next
	}
}

// partOf reports whether agent sent or received a message of thread.
func (m *Messenger) partOf(ctx context.Context, thread, agent string) bool {
	msgs, err := m.threadMessages(ctx, thread)
	if err != nil {
		return false
	}
	addr := Address(agent)
	return slices.ContainsFunc(msgs, func(msg store.Message) bool { return msg.From == addr || msg.To == addr })
}

// await waits up to wait for the reply to sent in conversation conv, and
// takes it: it is processed here, by the waiting call, and never wakes the
// sender. A stop notice to conv ends the wait with the stop's error.
func (m *Messenger) await(ctx context.Context, sent store.Message, conv string, wait time.Duration, out messageOut) (messageOut, error) {
	owner := m.newID("wait_")
	deadline := m.clk.NewTimer(wait)
	defer deadline.Stop()
	for {
		changed := m.changes()
		msgs, err := m.st.ThreadMessages(ctx, sent.ThreadID, sent.Hop, store.MaxPageLimit)
		if err != nil {
			return messageOut{}, err
		}
		for _, msg := range msgs {
			isReply := msg.Kind == store.MessageReply && msg.InReplyTo == sent.ID
			isStop := msg.Kind == store.MessageNotice
			if msg.ToConversation != conv || msg.State != store.MessageQueued || (!isReply && !isStop) {
				continue
			}
			taken, err := m.takeMessage(ctx, msg.ID, owner)
			switch {
			case errors.Is(err, store.ErrLeased), errors.Is(err, store.ErrFinal):
				continue // the dispatcher has it; look again later
			case err != nil:
				return messageOut{}, err
			}
			e, _ := ParseEnvelope(envelope(taken))
			if isStop {
				code := CodeThreadStopped
				if e.Error != nil {
					code = e.Error.Code
				}
				return messageOut{}, &Error{code, taken.Text}
			}
			out.Status, out.Reply = "replied", &replyOut{From: taken.From, Text: taken.Text, Error: e.Error}
			return out, nil
		}
		poll := m.clk.NewTimer(m.poll)
		select {
		case <-changed:
		case <-poll.C():
		case <-deadline.C():
			poll.Stop()
			out.Status = "timeout"
			return out, nil
		case <-ctx.Done():
			poll.Stop()
			return messageOut{}, ctx.Err()
		}
		poll.Stop()
	}
}

// takeMessage claims message id as owner and marks it done: processed by
// the call that took it.
func (m *Messenger) takeMessage(ctx context.Context, id, owner string) (store.Message, error) {
	msg, err := m.st.ClaimMessage(ctx, id, owner, waitTTL)
	if err != nil {
		return store.Message{}, err
	}
	if _, err := m.st.UpdateMessage(ctx, id, owner, store.MessageUpdate{State: store.MessageDone}); err != nil {
		return store.Message{}, err
	}
	return msg, nil
}

type readIn struct {
	Thread string `json:"thread" jsonschema:"the thread's ID, from agent_message or a message you received"`
}

type threadOut struct {
	Thread   string       `json:"thread"`
	Status   string       `json:"status"`
	Error    *ErrorData   `json:"error,omitempty"`
	Messages []messageRow `json:"messages"`
}

type messageRow struct {
	ID        string          `json:"id"`
	Hop       int             `json:"hop"`
	From      string          `json:"from"`
	To        string          `json:"to"`
	Kind      string          `json:"kind"`
	InReplyTo string          `json:"in_reply_to,omitempty"`
	Text      string          `json:"text"`
	Data      json.RawMessage `json:"data,omitempty"`
}

// read is thread_read.
func (m *Messenger) read(ctx context.Context, agent string, in readIn) (threadOut, error) {
	if _, ok := senderOf(ctx); !ok {
		return threadOut{}, &Error{CodeNoSender, "thread_read runs only in a run of the agent it belongs to"}
	}
	th, err := m.st.GetThread(ctx, in.Thread)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !m.partOf(ctx, in.Thread, agent)) {
		return threadOut{}, &Error{CodeNoThread, fmt.Sprintf("no thread %q that agent %s is part of", in.Thread, agent)}
	}
	if err != nil {
		return threadOut{}, err
	}
	msgs, err := m.threadMessages(ctx, in.Thread)
	if err != nil {
		return threadOut{}, err
	}
	out := threadOut{Thread: th.ID, Status: string(th.Status), Messages: []messageRow{}}
	if th.Status == store.ThreadStopped {
		out.Error = &ErrorData{Code: th.StopCode, Message: th.StopReason}
	}
	for _, msg := range msgs {
		out.Messages = append(out.Messages, messageRow{
			ID: msg.ID, Hop: msg.Hop, From: msg.From, To: msg.To, Kind: string(msg.Kind),
			InReplyTo: msg.InReplyTo, Text: msg.Text, Data: msg.Data,
		})
	}
	return out, nil
}
