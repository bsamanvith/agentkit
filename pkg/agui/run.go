package agui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
)

// run serves one AG-UI run of agent ag: a new message, an action on a
// surface, or the answers to the interrupts of the run before.
func (s *Server) run(ctx context.Context, ag wire.Agent, in *RunAgentInput, em *emitter) error {
	conv, err := s.thread(ctx, ag.Name, in)
	if err != nil {
		return err
	}
	if len(in.Resume) > 0 {
		return s.resume(ctx, ag, conv, in, em)
	}
	rr := wire.RunRequest{Agent: ag.Name, ConversationID: conv}
	act, err := userAction(in.ForwardedProps)
	switch {
	case err != nil:
		return err
	case act != nil:
		if conv == "" {
			return &runError{code: CodeInvalidInput, msg: "an A2UI action continues a thread of this agent: send it with the thread's messages"}
		}
		rr.UIAction = act
	default:
		if rr.Message, err = prompt(in.Messages); err != nil {
			return err
		}
	}
	st, err := s.api.Stream(ctx, rr)
	if err != nil {
		return s.startError(ctx, err)
	}
	defer st.Close()
	em.bind(st.ConversationID(), st.RunID())
	if err := em.start(); err != nil {
		return err
	}
	return s.follow(ctx, st, newTranslator(em, st.RunID(), ag.Model))
}

// follow translates a stream's events until the run's last one.
func (s *Server) follow(ctx context.Context, st *agentclient.Stream, t *translator) error {
	for {
		e, err := st.Next()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(err, io.EOF) {
				err = errors.New("the run's stream ended before its last event")
			}
			s.cfg.log.Error("agui: following a run", "run", t.runID, "error", err)
			return &runError{code: wire.CodeInternal, msg: "the run's stream broke; GET /v1/runs/" + t.runID + " has its state"}
		}
		ended, err := t.handle(e)
		if err != nil || ended {
			return err
		}
	}
}

// thread finds the conversation an AG-UI thread continues: the one the
// threadId names, if it names one of this agent's; else the one of the run
// behind the last message this server gave the client; else none (a new
// conversation). AG-UI clients make up their thread IDs, so the messages
// they send back are what ties their runs together.
func (s *Server) thread(ctx context.Context, agent string, in *RunAgentInput) (string, error) {
	c, err := s.api.GetConversation(ctx, in.ThreadID)
	switch {
	case err == nil && c.Agent == agent:
		return c.ID, nil
	case err == nil:
		return "", &runError{code: CodeInvalidInput, msg: "the thread is a conversation of another agent"}
	case !isNotFound(err):
		return "", s.apiError(ctx, err)
	}
	tried := 0
	for i := len(in.Messages) - 1; i >= 0 && tried < 3; i-- {
		var m Message
		if json.Unmarshal(in.Messages[i], &m) != nil {
			continue
		}
		runID, ok := runOfMessage(m.ID)
		if !ok {
			continue
		}
		tried++
		r, err := s.api.GetRun(ctx, runID)
		switch {
		case err == nil && r.Agent == agent:
			return r.ConversationID, nil
		case err != nil && !isNotFound(err):
			return "", s.apiError(ctx, err)
		}
	}
	return "", nil
}

// prompt is the text of the request's last message, which must be the
// user's: the conversation before it is the agent API's already.
func prompt(msgs []json.RawMessage) (string, error) {
	if len(msgs) == 0 {
		return "", &runError{code: CodeInvalidInput, msg: "no message to answer: the last message must be the user's"}
	}
	var m Message
	if err := json.Unmarshal(msgs[len(msgs)-1], &m); err != nil {
		return "", &runError{code: CodeInvalidInput, msg: "the last message is not a message: " + err.Error()}
	}
	if m.Role != "user" {
		msg := "the last message must be the user's, not a " + m.Role + " message"
		if m.Role == "tool" {
			msg += ": the agent's tools run on the server, and frontend tools are not offered to it; answer an interrupt with resume"
		}
		return "", &runError{code: CodeInvalidInput, msg: msg}
	}
	var text string
	if json.Unmarshal(m.Content, &text) != nil {
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal(m.Content, &parts); err != nil {
			return "", &runError{code: CodeInvalidInput, msg: "the user's message has no content"}
		}
		var b strings.Builder
		for _, p := range parts {
			if p.Type != "text" {
				return "", &runError{code: CodeUnsupported, msg: "the agent reads text: a " + p.Type + " part is not supported"}
			}
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(p.Text)
		}
		text = b.String()
	}
	if strings.TrimSpace(text) == "" {
		return "", &runError{code: CodeInvalidInput, msg: "the user's message is empty"}
	}
	return text, nil
}

// userAction reads an action on an A2UI surface from forwardedProps.
func userAction(props json.RawMessage) (*wire.UIAction, error) {
	if len(props) == 0 {
		return nil, nil
	}
	var p struct {
		A2UIAction *struct {
			UserAction *UserAction `json:"userAction"`
		} `json:"a2uiAction"`
	}
	if json.Unmarshal(props, &p) != nil || p.A2UIAction == nil {
		return nil, nil // forwardedProps of something else
	}
	a := p.A2UIAction.UserAction
	if a == nil || a.Name == "" || a.SurfaceID == "" {
		return nil, &runError{code: CodeInvalidInput, msg: "forwardedProps.a2uiAction.userAction needs a name and a surfaceId"}
	}
	act := &wire.UIAction{SurfaceID: a.SurfaceID, ComponentID: a.SourceComponentID, Action: a.Name}
	if c := bytes.TrimSpace(a.Context); len(c) > 0 && string(c) != "null" {
		if c[0] != '{' {
			return nil, &runError{code: CodeInvalidInput, msg: "an A2UI action's context is an object"}
		}
		act.Context = c
	}
	return act, nil
}

// resume answers the interrupts of the thread's suspended run and streams
// the rest of that run: the answered calls' results first (their start is
// the run before's), as the spec's tool-bound interrupts have it.
func (s *Server) resume(ctx context.Context, ag wire.Agent, conv string, in *RunAgentInput, em *emitter) error {
	runID := ""
	byCall := map[string]ResumeEntry{}
	for _, r := range in.Resume {
		run, call, ok := parseInterruptID(r.InterruptID)
		switch {
		case !ok:
			return &runError{code: CodeUnknownInterrupt, msg: "resume: " + quote(r.InterruptID) + " is not an interrupt of this server"}
		case runID != "" && run != runID:
			return &runError{code: CodeInvalidInput, msg: "resume: the interrupts are of more than one run"}
		case r.Status != ResumeResolved && r.Status != ResumeCancelled:
			return &runError{code: CodeInvalidInput, msg: "resume: status must be resolved or cancelled"}
		}
		if _, dup := byCall[call]; dup {
			return &runError{code: CodeInvalidInput, msg: "resume: " + quote(r.InterruptID) + " is answered twice"}
		}
		runID, byCall[call] = run, r
	}
	run, err := s.api.GetRun(ctx, runID)
	switch {
	case isNotFound(err):
		return &runError{code: CodeUnknownInterrupt, msg: "resume: no run " + quote(runID)}
	case err != nil:
		return s.apiError(ctx, err)
	case run.Agent != ag.Name || (conv != "" && run.ConversationID != conv):
		return &runError{code: CodeUnknownInterrupt, msg: "resume: the interrupts are not this thread's"}
	case run.Status != wire.StatusSuspended:
		return &runError{code: CodeUnknownInterrupt, msg: "resume: the run is " + run.Status + " and waits for no answers"}
	}
	waiting := map[string]wire.Interrupt{}
	for _, i := range run.Interrupts {
		waiting[i.ID] = i
	}
	var answers []wire.Answer
	cancel := false
	for call, r := range byCall {
		i, ok := waiting[call]
		if !ok {
			return &runError{code: CodeUnknownInterrupt, msg: "resume: the run does not wait for " + quote(r.InterruptID)}
		}
		a, cancels, err := answerOf(i, r)
		if err != nil {
			return err
		}
		cancel = cancel || cancels
		answers = append(answers, a)
	}
	for _, i := range run.Interrupts {
		if _, ok := byCall[i.ID]; !ok && i.Answer == nil {
			return &runError{code: CodeInvalidInput, msg: "resume must answer every open interrupt: " + quote(interruptID(run.ID, i.ID)) + " has no answer"}
		}
	}
	// Where the run went on hold: its events so far, to the last.
	last, turn, err := s.lastEvent(ctx, run.ID)
	if err != nil {
		return err
	}
	em.bind(run.ConversationID, run.ID)
	if err := em.start(); err != nil {
		return err
	}
	if cancel {
		// A question nobody answers leaves the run nothing to go on with.
		r, err := s.api.Cancel(ctx, run.ID)
		if err != nil {
			return s.apiError(ctx, err)
		}
		at := s.cfg.clock.Now()
		if r.FinishedAt != nil {
			at = *r.FinishedAt
		}
		return em.finish(at, &Outcome{Type: OutcomeCancelled}, nil, nil)
	}
	if _, err := s.api.Answer(ctx, run.ID, answers...); err != nil {
		var ae *agentclient.APIError
		if errors.As(err, &ae) && ae.Status == http.StatusConflict {
			return &runError{code: CodeUnknownInterrupt, msg: "resume: " + ae.Message}
		}
		return s.apiError(ctx, err)
	}
	st, err := s.api.Events(ctx, run.ID, last)
	if err != nil {
		return s.apiError(ctx, err)
	}
	defer st.Close()
	t := newTranslator(em, run.ID, ag.Model)
	t.answered = map[string]bool{}
	for call := range byCall {
		t.answered[call] = true
	}
	t.resumedAt, t.done = turn, turn-1
	return s.follow(ctx, st, t)
}

// lastEvent reads a suspended run's events: the ID of the last and the turn
// it was in.
func (s *Server) lastEvent(ctx context.Context, runID string) (string, int, error) {
	st, err := s.api.Events(ctx, runID, "")
	if err != nil {
		return "", 0, s.apiError(ctx, err)
	}
	defer st.Close()
	last, turn := "", 0
	for {
		e, err := st.Next()
		if errors.Is(err, io.EOF) {
			return last, turn, nil
		}
		if err != nil {
			if ctx.Err() != nil {
				return "", 0, ctx.Err()
			}
			return "", 0, s.apiError(ctx, err)
		}
		last, turn = e.ID, max(turn, e.Turn)
	}
}

// answerOf is the agent API's answer to interrupt i from a resume entry:
// {"approved": bool, "reason": ...} (or a bare bool) for an approval,
// {"text": ...} (or a bare string) for a question. A cancelled approval is a
// denial; a cancelled question cancels the run.
func answerOf(i wire.Interrupt, r ResumeEntry) (wire.Answer, bool, error) {
	a := wire.Answer{ID: i.ID}
	bad := func(want string) (wire.Answer, bool, error) {
		return a, false, &runError{code: CodeInvalidInput, msg: "resume: the payload of " + quote(r.InterruptID) + " must be " + want}
	}
	if i.Kind == wire.InterruptApproval {
		if r.Status == ResumeCancelled {
			a.Reason = "cancelled by the user"
			return a, false, nil
		}
		var p struct {
			Approved   *bool           `json:"approved"`
			Reason     string          `json:"reason"`
			EditedArgs json.RawMessage `json:"editedArgs"`
		}
		var approved bool
		switch {
		case json.Unmarshal(r.Payload, &approved) == nil:
			a.Approved = approved
		case json.Unmarshal(r.Payload, &p) == nil && p.Approved != nil:
			if len(p.EditedArgs) > 0 && string(p.EditedArgs) != "null" {
				return a, false, &runError{code: CodeUnsupported, msg: "resume: approving with edited arguments is not supported"}
			}
			a.Approved, a.Reason = *p.Approved, p.Reason
		default:
			return bad(`{"approved": true or false}`)
		}
		return a, false, nil
	}
	if r.Status == ResumeCancelled {
		return a, true, nil
	}
	var p struct {
		Text *string `json:"text"`
	}
	switch {
	case json.Unmarshal(r.Payload, &a.Text) == nil:
	case json.Unmarshal(r.Payload, &p) == nil && p.Text != nil:
		a.Text = *p.Text
	default:
		return bad(`{"text": "..."}`)
	}
	return a, false, nil
}

// startError maps the agent API's refusal to start a run.
func (s *Server) startError(ctx context.Context, err error) error {
	var ae *agentclient.APIError
	if errors.As(err, &ae) && ae.Status == http.StatusConflict && ae.Code == wire.CodeBusy && ae.RunID != "" {
		if r, err := s.api.GetRun(ctx, ae.RunID); err == nil && r.Status == wire.StatusSuspended {
			return &runError{code: CodePendingInterrupts, msg: "the thread's run waits for answers to its interrupts: send them in resume"}
		}
	}
	return s.apiError(ctx, err)
}

// apiError maps an error of an agent API call to the run's RUN_ERROR: the
// API's code and message.
func (s *Server) apiError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var ae *agentclient.APIError
	if !errors.As(err, &ae) || ae.Code == "" {
		s.cfg.log.Error("agui: calling the agent API", "error", err)
		return &runError{code: wire.CodeInternal, msg: "calling the agent failed; see the server log"}
	}
	return &runError{code: ae.Code, msg: ae.Message}
}

func isNotFound(err error) bool {
	var ae *agentclient.APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

func quote(s string) string {
	if len(s) > 64 {
		s = s[:64]
	}
	b, _ := json.Marshal(s)
	return string(b)
}
