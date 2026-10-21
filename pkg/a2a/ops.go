package a2a

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
)

// The operations, the same for both bindings. A task is a run of the agent
// API and a contextId is a conversation: every task started here is a run
// in GET /v1/runs and its conversation in GET /v1/conversations.

// op says what an agent API call was for, to map its 404.
type op int

const (
	opAgent   op = iota // the agent itself
	opTask              // a task (a run)
	opContext           // a contextId (a conversation)
)

// fromAPI maps an error of an agent API call to the A2A error the client
// gets. A credential the API refused is answered with its HTTP status in
// either binding.
func (s *Server) fromAPI(ctx context.Context, err error, o op) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var ae *agentclient.APIError
	if !errors.As(err, &ae) {
		s.cfg.log.Error("a2a: calling the agent API", "error", err)
		return ErrInternal.with("calling the agent failed; see the server log")
	}
	switch ae.Status {
	case http.StatusUnauthorized:
		he := &httpError{status: ae.Status, msg: "missing or invalid credentials"}
		if c := callerFrom(ctx); c != nil {
			he.challenge = *c.challenge
		}
		return he
	case http.StatusForbidden:
		return &httpError{status: ae.Status, msg: ae.Message}
	case http.StatusNotFound:
		switch o {
		case opTask:
			return ErrTaskNotFound
		case opContext:
			return ErrInvalidParams.with("unknown contextId: start a conversation without one")
		}
		return &httpError{status: http.StatusNotFound, msg: "not found"}
	case http.StatusConflict:
		switch ae.Code {
		case wire.CodeAgentMismatch:
			return ErrInvalidParams.with("unknown contextId: start a conversation without one")
		case wire.CodeBusy:
			return ErrUnsupported.with("a task of this context is still working: wait for it to end", "taskId", ae.RunID)
		case wire.CodeRunFinal:
			return ErrTaskNotCancelable.with("the task has ended")
		}
		return ErrUnsupported.with(ae.Message)
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge:
		return ErrInvalidParams.with(ae.Message)
	case http.StatusServiceUnavailable:
		e := ErrInternal.with(ae.Message)
		e.RetryAfter = ae.RetryAfter
		if e.RetryAfter == 0 {
			e.RetryAfter = time.Second
		}
		return e
	}
	s.cfg.log.Error("a2a: the agent API answered", "status", ae.Status, "code", ae.Code)
	return ErrInternal.with("the agent failed; see the server log")
}

// getTask returns the run of a task of agent: one of another agent is not
// found, as one of another tenant is (the agent API's 404).
func (s *Server) getRun(ctx context.Context, agent, id string) (wire.Run, error) {
	if id == "" {
		return wire.Run{}, ErrInvalidParams.with("params.id is required")
	}
	r, err := s.api.GetRun(ctx, id)
	if err != nil {
		if err := s.fromAPI(ctx, err, opTask); !errors.Is(err, ErrTaskNotFound) {
			return wire.Run{}, err
		}
		return wire.Run{}, s.taskNotFound(id)
	}
	if r.Agent != agent {
		return wire.Run{}, s.taskNotFound(id)
	}
	return r, nil
}

// taskNotFound is the TaskNotFoundError of id, with the spec's metadata
// (9.5): the task's ID and the time.
func (s *Server) taskNotFound(id string) error {
	return ErrTaskNotFound.with("Task not found", "taskId", id, "timestamp", s.cfg.clock.Now().UTC().Format(timestampLayout))
}

// given reports whether a JSON value was given.
func given(v []byte) bool { return len(v) > 0 && string(v) != "null" }

func checkHistory(n *int32) error {
	if n != nil && *n < 0 {
		return ErrInvalidParams.with("historyLength must not be negative")
	}
	return nil
}

func (s *Server) getTask(ctx context.Context, agent string, req GetTaskRequest) (Task, error) {
	if err := checkHistory(req.HistoryLength); err != nil {
		return Task{}, err
	}
	r, err := s.getRun(ctx, agent, req.ID)
	if err != nil {
		return Task{}, err
	}
	return taskOf(announced(r), true), nil
}

func (s *Server) cancelTask(ctx context.Context, agent string, req TaskIDRequest) (Task, error) {
	r, err := s.getRun(ctx, agent, req.ID)
	if err != nil {
		return Task{}, err
	}
	if terminal(stateOf(r.Status)) {
		return Task{}, ErrTaskNotCancelable.with("the task is " + stateOf(r.Status) + ": it cannot be canceled")
	}
	r, err = s.api.Cancel(ctx, r.ID)
	if err != nil {
		return Task{}, s.fromAPI(ctx, err, opTask)
	}
	return taskOf(r, true), nil
}

// prepare checks a message and turns it into the run request it starts.
func (s *Server) prepare(ctx context.Context, agent string, req SendMessageRequest) (wire.RunRequest, error) {
	if req.Message == nil {
		return wire.RunRequest{}, ErrInvalidParams.with("params.message is required")
	}
	if c := req.Configuration; c != nil {
		if given(c.TaskPushNotificationConfig) || given(c.PushNotificationConfig) {
			return wire.RunRequest{}, ErrPushNotSupported
		}
		if !acceptsText(c.AcceptedOutputModes) {
			return wire.RunRequest{}, ErrContentType.with("the agent answers in text/plain only")
		}
		if err := checkHistory(c.HistoryLength); err != nil {
			return wire.RunRequest{}, err
		}
	}
	text, err := promptOf(req.Message)
	if err != nil {
		return wire.RunRequest{}, err
	}
	m := req.Message
	if m.TaskID != "" {
		// A message to a task continues it, which a run cannot be: it is
		// answered, or still going on. The next task of the conversation is
		// a message with the contextId only.
		r, err := s.getRun(ctx, agent, m.TaskID)
		if err != nil {
			return wire.RunRequest{}, err
		}
		if m.ContextID != "" && m.ContextID != r.ConversationID {
			return wire.RunRequest{}, ErrInvalidParams.with("the message's contextId is not its task's")
		}
		state := stateOf(r.Status)
		if terminal(state) {
			return wire.RunRequest{}, ErrUnsupported.with("the task is "+state+" and takes no more messages: send the message with its contextId to start a new task", "taskId", r.ID)
		}
		return wire.RunRequest{}, ErrUnsupported.with("the task is "+state+": it takes no messages while it works", "taskId", r.ID)
	}
	return wire.RunRequest{Message: text, ConversationID: m.ContextID, Agent: agent}, nil
}

// follower follows a run the server started to its end.
type follower struct {
	runID, convID string
	done          chan struct{} // closed when the run's stream has ended
	final         *wire.Event   // its last event, set before done is closed; nil if the stream broke
}

// start starts a run and follows it to its end in the background, so that
// it goes on when the A2A client goes away (a task outlives its streams).
func (s *Server) start(ctx context.Context, rr wire.RunRequest) (*follower, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrInternal.with("the server is shutting down")
	}
	s.runs.Add(1)
	s.mu.Unlock()
	// The follower's call carries the caller's credential once, to start
	// the run; it keeps nothing of the request but that.
	fctx := s.base
	if c := callerFrom(ctx); c != nil {
		fctx = context.WithValue(fctx, callerKey{}, c)
	}
	st, err := s.api.Stream(fctx, rr)
	if err != nil {
		s.runs.Done()
		o := opAgent
		if rr.ConversationID != "" {
			o = opContext
		}
		return nil, s.fromAPI(ctx, err, o)
	}
	f := &follower{runID: st.RunID(), convID: st.ConversationID(), done: make(chan struct{})}
	go func() {
		defer s.runs.Done()
		defer close(f.done)
		defer st.Close()
		for {
			e, err := st.Next()
			if err != nil {
				return
			}
			if e.Type == wire.TypeRunDone || e.Type == wire.TypeRunError {
				f.final = &e
			}
		}
	}()
	return f, nil
}

func (s *Server) sendMessage(ctx context.Context, agent string, req SendMessageRequest) (Task, error) {
	rr, err := s.prepare(ctx, agent, req)
	if err != nil {
		return Task{}, err
	}
	f, err := s.start(ctx, rr)
	if err != nil {
		return Task{}, err
	}
	if req.Configuration == nil || !req.Configuration.ReturnImmediately {
		select {
		case <-f.done:
		case <-ctx.Done():
			return Task{}, ctx.Err()
		}
		if f.final != nil {
			// The run's last event, not GET /v1/runs: the run's status can
			// be stored a moment before its last event.
			return taskOf(endOf(*f.final, f.runID, f.convID), true), nil
		}
	}
	r, err := s.getRun(ctx, agent, f.runID)
	if err != nil {
		return Task{}, err
	}
	return taskOf(announced(r), true), nil
}

// announced is the run as far as its events say: a run whose status is
// stored as ended, but whose last event is not in its log yet, has no
// answer yet (the agent API's GET /v1/runs/{id} then has no record of its
// end), so it is still working for A2A.
func announced(r wire.Run) wire.Run {
	if r.Record != nil && r.Record.FinishedAt.IsZero() && terminal(stateOf(r.Status)) {
		r.Status, r.Answer, r.Error, r.FinishedAt = wire.StatusRunning, "", nil, nil
	}
	return r
}

// emitFunc sends one item of a stream.
type emitFunc func(StreamResponse) error

func (s *Server) streamMessage(ctx context.Context, agent string, req SendMessageRequest, emit emitFunc) error {
	rr, err := s.prepare(ctx, agent, req)
	if err != nil {
		return err
	}
	f, err := s.start(ctx, rr)
	if err != nil {
		return err
	}
	return s.follow(ctx, f.runID, f.convID, nil, emit)
}

func (s *Server) subscribe(ctx context.Context, agent string, req TaskIDRequest, emit emitFunc) error {
	r, err := s.getRun(ctx, agent, req.ID)
	if err != nil {
		return err
	}
	t := taskOf(announced(r), false)
	if terminal(t.Status.State) {
		return ErrUnsupported.with("the task is "+t.Status.State+": there is nothing to subscribe to", "taskId", r.ID)
	}
	return s.follow(ctx, r.ID, r.ConversationID, &t, emit)
}

// follow streams a run's events as A2A updates: first the task (first, or
// as of the run's start), then its answer as it is written, then its last
// status. The answer is the artifact "answer": each turn's text replaces the
// one before (append false on a turn's first chunk), because only the last
// turn's text is the answer, and the whole answer is sent again as the last
// chunk.
func (s *Server) follow(ctx context.Context, runID, convID string, first *Task, emit emitFunc) error {
	es, err := s.api.Events(ctx, runID, "")
	if err != nil {
		return s.fromAPI(ctx, err, opTask)
	}
	defer es.Close()
	if first != nil {
		if err := emit(StreamResponse{Task: first}); err != nil {
			return err
		}
	}
	turn := -1
	for {
		e, err := es.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.cfg.log.Error("a2a: following a run", "run", runID, "error", err)
			return ErrInternal.with("the task's stream broke; GetTask has its state")
		}
		switch e.Type {
		case wire.TypeRunStart:
			if first == nil {
				ts := Timestamp(e.At)
				t := Task{ID: runID, ContextID: cmp.Or(e.ConversationID, convID), Status: TaskStatus{State: StateWorking, Timestamp: &ts}}
				convID = t.ContextID
				if err := emit(StreamResponse{Task: &t}); err != nil {
					return err
				}
				first = &t
			}
		case wire.TypeTextDelta:
			if e.Text == "" {
				continue
			}
			up := &TaskArtifactUpdateEvent{TaskID: runID, ContextID: convID, Artifact: Artifact{ArtifactID: answerArtifactID, Name: "answer", Parts: []Part{TextPart(e.Text)}}, Append: e.Turn == turn}
			turn = e.Turn
			if err := emit(StreamResponse{ArtifactUpdate: up}); err != nil {
				return err
			}
		case wire.TypeRunDone, wire.TypeRunError:
			r := endOf(e, runID, convID)
			if r.Answer != "" {
				up := &TaskArtifactUpdateEvent{TaskID: runID, ContextID: convID, Artifact: answerArtifact(r.Answer), LastChunk: true}
				if err := emit(StreamResponse{ArtifactUpdate: up}); err != nil {
					return err
				}
			}
			return emit(StreamResponse{StatusUpdate: &TaskStatusUpdateEvent{TaskID: runID, ContextID: convID, Status: statusOf(r)}})
		}
	}
}

// endOf is the run as its last event leaves it, with the status the agent
// API gives an interactive run that ends so.
func endOf(e wire.Event, runID, convID string) wire.Run {
	at := e.At
	r := wire.Run{ID: runID, ConversationID: convID, FinishedAt: &at, Status: wire.StatusFailed, Error: e.Error}
	if e.Type == wire.TypeRunDone {
		r.Status, r.Answer = wire.StatusDone, e.Answer
		return r
	}
	if e.Error == nil {
		r.Error = &wire.Error{Code: wire.CodeInternal, Message: "the run failed"}
		return r
	}
	switch e.Error.Code {
	case wire.CodeRunInterrupted, wire.CodeTimeout, wire.CodeShutdown:
		r.Status = wire.StatusInterrupted
	case wire.CodeCancelled:
		r.Status = wire.StatusCanceled
	case wire.CodeRejected:
		r.Status = wire.StatusRejected
	case wire.CodeSuspended:
		r.Status = wire.StatusSuspended
	}
	return r
}

// listTasks lists the agent's tasks the caller sees, the most recently
// changed first. The agent API lists runs per conversation, so this reads
// the conversations (or the one of contextId) and their runs, up to
// WithMaxListScan runs, and pages through what it read with an offset.
func (s *Server) listTasks(ctx context.Context, agent string, req ListTasksRequest) (ListTasksResponse, error) {
	size := int32(DefaultPageSize)
	if req.PageSize != nil {
		if *req.PageSize < 1 || *req.PageSize > MaxPageSize {
			return ListTasksResponse{}, ErrInvalidParams.with("pageSize must be between 1 and " + strconv.Itoa(MaxPageSize))
		}
		size = *req.PageSize
	}
	if err := checkHistory(req.HistoryLength); err != nil {
		return ListTasksResponse{}, err
	}
	state := ""
	if req.Status != "" && req.Status != "TASK_STATE_UNSPECIFIED" {
		st, ok := parseState(req.Status)
		if !ok {
			return ListTasksResponse{}, ErrInvalidParams.with("status: unknown task state " + quote(req.Status))
		}
		state = st
	}
	offset := 0
	if req.PageToken != "" {
		b, err := base64.RawURLEncoding.DecodeString(req.PageToken)
		n, err2 := strconv.Atoi(string(b))
		if err != nil || err2 != nil || n < 0 {
			return ListTasksResponse{}, ErrInvalidParams.with("pageToken is not one this server gave")
		}
		offset = n
	}
	runs, err := s.runsOf(ctx, agent, req.ContextID)
	if err != nil {
		return ListTasksResponse{}, err
	}
	runs = slices.DeleteFunc(runs, func(r wire.Run) bool {
		if state != "" && stateOf(r.Status) != state {
			return true
		}
		return req.StatusTimestampAfter != nil && !statusTime(r).After(time.Time(*req.StatusTimestampAfter))
	})
	slices.SortStableFunc(runs, func(a, b wire.Run) int {
		if c := statusTime(b).Compare(statusTime(a)); c != 0 {
			return c
		}
		if c := b.CreatedAt.Compare(a.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(b.ID, a.ID)
	})
	resp := ListTasksResponse{Tasks: []Task{}, PageSize: size, TotalSize: int32(len(runs))}
	withArtifacts := req.IncludeArtifacts != nil && *req.IncludeArtifacts
	for i := offset; i < len(runs) && i < offset+int(size); i++ {
		resp.Tasks = append(resp.Tasks, taskOf(runs[i], withArtifacts))
	}
	if next := offset + int(size); next < len(runs) {
		resp.NextPageToken = base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(next)))
	}
	return resp, nil
}

// runsOf reads the runs of the agent's conversations (or of one), newest
// conversations first, up to the scan limit.
func (s *Server) runsOf(ctx context.Context, agent, contextID string) ([]wire.Run, error) {
	var runs []wire.Run
	readConv := func(id string) error {
		cursor := ""
		for len(runs) < s.cfg.maxListScan {
			page, err := s.api.Runs(ctx, agentclient.RunsQuery{ConversationID: id, Limit: MaxPageSize, Cursor: cursor})
			if err != nil {
				return err
			}
			for _, r := range page.Runs {
				if r.Agent == agent && len(runs) < s.cfg.maxListScan {
					runs = append(runs, r)
				}
			}
			if page.NextCursor == "" {
				return nil
			}
			cursor = page.NextCursor
		}
		return nil
	}
	if contextID != "" {
		if err := readConv(contextID); err != nil {
			var ae *agentclient.APIError
			if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
				return nil, nil // no such context: no tasks
			}
			return nil, s.fromAPI(ctx, err, opContext)
		}
		return runs, nil
	}
	cursor := ""
	for len(runs) < s.cfg.maxListScan {
		page, err := s.api.Conversations(ctx, agentclient.ConversationsQuery{Agent: agent, Limit: MaxPageSize, Cursor: cursor})
		if err != nil {
			return nil, s.fromAPI(ctx, err, opAgent)
		}
		for _, c := range page.Conversations {
			if err := readConv(c.ID); err != nil {
				var ae *agentclient.APIError
				if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
					continue // deleted meanwhile
				}
				return nil, s.fromAPI(ctx, err, opAgent)
			}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	return runs, nil
}
