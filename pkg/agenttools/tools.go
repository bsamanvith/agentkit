package agenttools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// Limits on what the tools return, so a long run or conversation stays
// readable for the model or person calling them.
const (
	// MaxInputBytes is how much of a tool call's input a summary shows.
	MaxInputBytes = 200
	// MaxResultBytes is how much of a tool result (or its error) is shown.
	MaxResultBytes = 1000
)

// AgentsInput is the input of agents_list: nothing.
type AgentsInput struct{}

// AgentsOutput is the output of agents_list.
type AgentsOutput struct {
	Agents []Agent `json:"agents"`
}

// Agent is an agent the caller may run.
type Agent struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Default marks the agent agent_run uses when it names none.
	Default bool `json:"default"`
	// Toolsets are the toolsets whose tools the agent has.
	Toolsets []string `json:"toolsets"`
}

// RunInput is the input of agent_run.
type RunInput struct {
	Agent          string `json:"agent,omitempty" jsonschema:"the agent to run, from agents_list; empty for the default agent, or the conversation's own"`
	Message        string `json:"message" jsonschema:"the user's message to the agent"`
	ConversationID string `json:"conversation_id,omitempty" jsonschema:"continue this conversation (from an earlier result); empty starts a new one"`
}

// RunGetInput is the input of run_get.
type RunGetInput struct {
	RunID string `json:"run_id" jsonschema:"the run, from agent_run"`
}

// Run is a run, as agent_run and run_get return it.
type Run struct {
	RunID          string `json:"run_id"`
	ConversationID string `json:"conversation_id"`
	Agent          string `json:"agent,omitempty"`
	// Status is running, done, failed, canceled, rejected or suspended.
	Status string `json:"status"`
	Answer string `json:"answer,omitempty"`
	// ToolCalls are the run's tool calls, in order, once it has ended.
	ToolCalls []ToolCall `json:"tool_calls"`
	Usage     llm.Usage  `json:"usage"`
	Turns     int        `json:"turns"`
	// Error is why a run that ended did not answer.
	Error *RunError `json:"error,omitempty"`
	// Note says what to do next, for a run still going.
	Note string `json:"note,omitempty"`
}

// ToolCall summarizes one tool call of a run.
type ToolCall struct {
	Name string `json:"name"`
	// Input is the call's JSON input, cut to MaxInputBytes.
	Input string `json:"input"`
	OK    bool   `json:"ok"`
	// Error is what the tool answered when it failed, cut to MaxResultBytes.
	Error string `json:"error,omitempty"`
}

// RunError is why a run ended without answering.
type RunError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable,omitempty"`
}

// ConversationsInput is the input of conversations_list.
type ConversationsInput struct {
	Agent  string `json:"agent,omitempty" jsonschema:"only this agent's conversations"`
	Limit  int    `json:"limit,omitempty" jsonschema:"page size, 1 to 100 (default 20)"`
	Cursor string `json:"cursor,omitempty" jsonschema:"next_cursor of the previous page"`
}

// ConversationsOutput is the output of conversations_list: newest first.
type ConversationsOutput struct {
	Conversations []ConversationSummary `json:"conversations"`
	NextCursor    string                `json:"next_cursor,omitempty"`
}

// ConversationSummary is a conversation in a list.
type ConversationSummary struct {
	ID    string `json:"id"`
	Agent string `json:"agent"`
	// Title is the start of the first message.
	Title string `json:"title"`
	// Status is idle or interrupted.
	Status       string `json:"status"`
	MessageCount int    `json:"message_count"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

// ConversationGetInput is the input of conversation_get.
type ConversationGetInput struct {
	ConversationID string `json:"conversation_id" jsonschema:"the conversation, from agent_run or conversations_list"`
}

// Conversation is a conversation with its messages.
type Conversation struct {
	ID        string    `json:"id"`
	Agent     string    `json:"agent"`
	Status    string    `json:"status"`
	CreatedAt string    `json:"created_at"`
	UpdatedAt string    `json:"updated_at"`
	Messages  []Message `json:"messages"`
}

// Message is one message of a conversation: its text, the tool calls it
// asks for (assistant) and the tool results it carries (user).
type Message struct {
	Role        string       `json:"role"`
	At          string       `json:"at"`
	Text        string       `json:"text,omitempty"`
	ToolCalls   []MessageUse `json:"tool_calls,omitempty"`
	ToolResults []ResultText `json:"tool_results,omitempty"`
}

// MessageUse is a tool call in a message.
type MessageUse struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Input string `json:"input"` // cut to MaxInputBytes
}

// ResultText is a tool result in a message.
type ResultText struct {
	ToolUseID string `json:"tool_use_id"`
	OK        bool   `json:"ok"`
	Content   string `json:"content"` // cut to MaxResultBytes
}

// tools holds one toolset's client.
type tools struct {
	f    *Factory
	api  *agentclient.Client
	wait time.Duration
}

func newTools(f *Factory, api *agentclient.Client, wait time.Duration) ([]tool.Tool, error) {
	t := &tools{f: f, api: api, wait: wait}
	read := tool.WithMetadata(tool.Metadata{Kind: tool.KindRead, Risk: tool.RiskLow})
	list, err := tool.Func("agents_list", "List the agents you may run: name, description, whether it is the default, and its toolsets.", t.agents, read)
	if err != nil {
		return nil, err
	}
	run, err := tool.Func("agent_run",
		fmt.Sprintf("Run an agent on a message and wait for its answer (up to %v). Returns the answer, run_id, conversation_id, status, a summary of its tool calls and the token usage. Pass conversation_id to continue a conversation. If the run takes longer, the status is \"running\": call run_get with the run_id later.", wait),
		t.run, tool.WithMetadata(tool.Metadata{Kind: tool.KindExecute, Risk: tool.RiskMedium}))
	if err != nil {
		return nil, err
	}
	get, err := tool.Func("run_get", "Get a run by run_id: its status and, once it has ended, its answer, tool calls and usage.", t.getRun, read)
	if err != nil {
		return nil, err
	}
	convs, err := tool.Func("conversations_list", "List your conversations, newest first, optionally one agent's. Pass next_cursor as cursor for the next page.", t.conversations, read)
	if err != nil {
		return nil, err
	}
	conv, err := tool.Func("conversation_get", "Get a conversation and its messages: text, tool calls and tool results.", t.conversation, read)
	if err != nil {
		return nil, err
	}
	return []tool.Tool{list, run, get, convs, conv}, nil
}

func (t *tools) agents(ctx context.Context, _ AgentsInput) (AgentsOutput, error) {
	list, err := t.api.Agents(ctx)
	if err != nil {
		return AgentsOutput{}, apiError(err)
	}
	out := AgentsOutput{Agents: make([]Agent, 0, len(list))}
	for _, a := range list {
		ag := Agent{Name: a.Name, Description: a.Description, Default: a.Default, Toolsets: make([]string, 0, len(a.Toolsets))}
		for _, ts := range a.Toolsets {
			ag.Toolsets = append(ag.Toolsets, ts.Name)
		}
		out.Agents = append(out.Agents, ag)
	}
	return out, nil
}

// run starts a run, follows its events in a goroutine (the API interrupts a
// run nobody follows), and waits for it to end, for the wait, or for the
// caller to leave.
func (t *tools) run(ctx context.Context, in RunInput) (Run, error) {
	if t.f.ctx.Err() != nil {
		return Run{}, ErrClosed
	}
	sctx, cancel := t.f.detach(ctx)
	// Until the run's response starts, the caller leaving (or its call
	// timeout) ends the request; from then on the follower outlives the call.
	stop := context.AfterFunc(ctx, cancel)
	s, err := t.api.Stream(sctx, wire.RunRequest{Agent: in.Agent, Message: in.Message, ConversationID: in.ConversationID})
	if !stop() { // ctx is done: sctx was canceled
		if err == nil {
			s.Close()
		}
		cancel()
		return Run{}, ctx.Err()
	}
	if err != nil {
		cancel()
		return Run{}, apiError(err)
	}
	ids := runIDs{run: s.RunID(), conversation: s.ConversationID()}
	done := make(chan struct{})
	follow := func() {
		defer close(done)
		defer cancel()
		defer s.Close()
		for {
			e, err := s.Next()
			if err != nil {
				if !errors.Is(err, io.EOF) && !errors.Is(err, agentclient.ErrClosed) && sctx.Err() == nil {
					t.f.cfg.log.Warn("following a run", "run_id", ids.get().run, "err", err)
				}
				return
			}
			if e.Type == wire.TypeRunStart {
				ids.fill(e.RunID, e.ConversationID)
			}
		}
	}
	if !t.f.spawn(follow) {
		s.Close()
		cancel()
		return Run{}, ErrClosed
	}
	timer := t.f.cfg.clock.NewTimer(t.wait)
	defer timer.Stop()
	select {
	case <-done:
		id := ids.get()
		r, err := t.api.GetRun(ctx, id.run)
		if err != nil {
			return Run{}, apiError(err)
		}
		return toRun(r), nil
	case <-timer.C():
		id := ids.get()
		return Run{
			RunID: id.run, ConversationID: id.conversation, Agent: in.Agent, Status: wire.StatusRunning, ToolCalls: []ToolCall{},
			Note: fmt.Sprintf("The run is still going after %v. Call run_get with run_id %q to see it end; send no new message to the conversation until then.", t.wait, id.run),
		}, nil
	case <-ctx.Done():
		s.Close() // the follower ends; the run goes on for the API's reconnect grace
		<-done
		return Run{}, ctx.Err()
	}
}

// runIDs are a run's IDs: from the response headers, else from run_start.
type runIDs struct {
	mu                sync.Mutex
	run, conversation string
}

type idPair struct{ run, conversation string }

func (r *runIDs) fill(run, conversation string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.run == "" {
		r.run = run
	}
	if r.conversation == "" {
		r.conversation = conversation
	}
}

func (r *runIDs) get() idPair {
	r.mu.Lock()
	defer r.mu.Unlock()
	return idPair{r.run, r.conversation}
}

func (t *tools) getRun(ctx context.Context, in RunGetInput) (Run, error) {
	if !isID(in.RunID) {
		return Run{}, apiError(&agentclient.APIError{Status: http.StatusNotFound, Code: wire.CodeNotFound, Message: fmt.Sprintf("no run %q", in.RunID)})
	}
	r, err := t.api.GetRun(ctx, in.RunID)
	if err != nil {
		return Run{}, apiError(err)
	}
	return toRun(r), nil
}

func (t *tools) conversations(ctx context.Context, in ConversationsInput) (ConversationsOutput, error) {
	list, err := t.api.Conversations(ctx, agentclient.ConversationsQuery{Agent: in.Agent, Limit: in.Limit, Cursor: in.Cursor})
	if err != nil {
		return ConversationsOutput{}, apiError(err)
	}
	out := ConversationsOutput{Conversations: make([]ConversationSummary, 0, len(list.Conversations)), NextCursor: list.NextCursor}
	for _, c := range list.Conversations {
		out.Conversations = append(out.Conversations, ConversationSummary{
			ID: c.ID, Agent: c.Agent, Title: c.Title, Status: c.Status, MessageCount: c.MessageCount,
			CreatedAt: stamp(c.CreatedAt), UpdatedAt: stamp(c.UpdatedAt),
		})
	}
	return out, nil
}

func (t *tools) conversation(ctx context.Context, in ConversationGetInput) (Conversation, error) {
	if !isID(in.ConversationID) {
		return Conversation{}, apiError(&agentclient.APIError{Status: http.StatusNotFound, Code: wire.CodeNotFound, Message: fmt.Sprintf("no conversation %q", in.ConversationID)})
	}
	c, err := t.api.GetConversation(ctx, in.ConversationID)
	if err != nil {
		return Conversation{}, apiError(err)
	}
	out := Conversation{
		ID: c.ID, Agent: c.Agent, Status: c.Status, CreatedAt: stamp(c.CreatedAt), UpdatedAt: stamp(c.UpdatedAt),
		Messages: make([]Message, 0, len(c.Messages)),
	}
	for _, m := range c.Messages {
		msg := Message{Role: string(m.Message.Role), At: stamp(m.At)}
		var text []string
		for _, b := range m.Message.Content {
			switch b := b.(type) {
			case llm.Text:
				text = append(text, b.Text)
			case llm.ToolUse:
				msg.ToolCalls = append(msg.ToolCalls, MessageUse{ID: b.ID, Name: b.Name, Input: cut(string(b.Input), MaxInputBytes)})
			case llm.ToolResult:
				msg.ToolResults = append(msg.ToolResults, ResultText{ToolUseID: b.ToolUseID, OK: !b.IsError, Content: cut(b.Content, MaxResultBytes)})
			}
		}
		msg.Text = strings.Join(text, "\n")
		out.Messages = append(out.Messages, msg)
	}
	return out, nil
}

// toRun is the tools' form of a run: its tool calls from its record.
func toRun(r wire.Run) Run {
	out := Run{
		RunID: r.ID, ConversationID: r.ConversationID, Agent: r.Agent, Status: r.Status,
		Answer: r.Answer, Usage: r.Usage, Turns: r.Turns, ToolCalls: []ToolCall{},
	}
	if r.Error != nil {
		out.Error = &RunError{Code: r.Error.Code, Message: r.Error.Message, Retryable: r.Error.Retryable}
	}
	if r.Status == wire.StatusRunning {
		out.Note = fmt.Sprintf("The run is still going. Call run_get with run_id %q again later.", r.ID)
	}
	if r.Record != nil {
		out.ToolCalls = toolCalls(r.Record.Turns)
	}
	return out
}

// toolCalls lists the calls each turn's reply asked for, with how each
// ended (its result, in the turn's next message). A call with no result
// (the run stopped first) is not ok.
func toolCalls(turns []wire.TurnRecord) []ToolCall {
	calls := []ToolCall{}
	for _, turn := range turns {
		results := map[string]llm.ToolResult{}
		for _, m := range turn.Messages {
			for _, b := range m.Message.Content {
				if r, ok := b.(llm.ToolResult); ok {
					results[r.ToolUseID] = r
				}
			}
		}
		for _, m := range turn.Messages {
			for _, b := range m.Message.Content {
				u, ok := b.(llm.ToolUse)
				if !ok {
					continue
				}
				c := ToolCall{Name: u.Name, Input: cut(string(u.Input), MaxInputBytes)}
				switch r, ok := results[u.ID]; {
				case !ok:
					c.Error = "no result: the run stopped before the call ended"
				case r.IsError:
					c.Error = cut(r.Content, MaxResultBytes)
				default:
					c.OK = true
				}
				calls = append(calls, c)
			}
		}
	}
	return calls
}

// isID reports whether s can name a run or a conversation in a path of the
// agent API: not "." or "..", which the API's router would clean into
// another path (GET /v1/conversations/. is the list of conversations).
// agentclient escapes everything else ("/", "?", "#", "%").
func isID(s string) bool { return s != "." && s != ".." }

// apiError is the tool error of a request the agent API refused, carrying
// its message, which says what to do.
func apiError(err error) error {
	var ae *agentclient.APIError
	if errors.As(err, &ae) {
		msg := ae.Message
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", ae.Status)
		}
		if ae.RunID != "" {
			msg += fmt.Sprintf(" (run_id %q)", ae.RunID)
		}
		if ae.Code != "" {
			msg = ae.Code + ": " + msg
		}
		return fmt.Errorf("agent API: %s", msg)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return fmt.Errorf("agent API: %w", err)
}

// cut shortens s to at most n bytes, on a rune boundary, marking the cut.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := n
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i] + "…"
}

// stamp formats a time for the tools' output; empty for the zero time.
func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
