package agentapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/runner"
	"github.com/bsamanvith/agentkit/pkg/store"
	"github.com/bsamanvith/agentkit/pkg/stream"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// Commands to a run another process runs (E18, T090). A stop or cancel that
// reaches a server whose runner does not hold the run is stored in the run's
// command log and signalled on the bus; the process running the run (or the
// one that takes it over) reads it from its inbox and stops or cancels the
// run there, as if the request had reached it. A server with no bus of its
// own ([WithBus]) is one replica: it answers such a request 409.

// The command types.
const (
	commandStop   = "stop"
	commandCancel = "cancel"
)

// poll is how often the server checks the store for what the bus may not
// have brought: events, commands, a run let go.
func (s *Server) poll() time.Duration {
	if s.cfg.streamPoll > 0 {
		return s.cfg.streamPoll
	}
	return stream.DefaultPoll
}

// command sends a stop or cancel to the process running the run, waits for
// its execution to end, and answers with the run.
func (s *Server) command(ctx context.Context, w http.ResponseWriter, r *http.Request, id, typ string) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		key = s.cfg.newID("cmd_")
	}
	run, err := s.store.Get(ctx, id)
	if err != nil {
		s.commandError(w, id, err)
		return
	}
	// Joined before the command goes, so the run's watch is still on when
	// the run ends by it: the command waits on the watch its viewers share.
	jv := s.joinView(ctx, run, bus.ID{Seq: uint64(max(lastStored(ctx, s.store, id), 0))})
	if _, err := s.cmds.Send(ctx, id, store.Command{Key: key, Type: typ}); err != nil {
		s.leaveView(jv)
		if errors.Is(err, store.ErrFinal) && typ == commandCancel {
			if run, gerr := s.store.Get(ctx, id); gerr == nil && run.Status == store.StatusCanceled {
				s.afterCommand(ctx, w, r, id)
				return
			}
		}
		if errors.Is(err, store.ErrConflict) || errors.Is(err, stream.ErrInvalid) {
			writeError(w, http.StatusBadRequest, &Error{Code: CodeInvalidRequest, Message: "Idempotency-Key was used for another command to run " + quote(id)})
			return
		}
		s.commandError(w, id, err)
		return
	}
	s.readView(ctx, jv, func(Event) {})
	s.awaitReleased(ctx, id)
	if ctx.Err() != nil {
		return
	}
	if typ == commandCancel {
		// The holder lets the run go, then cancels it: cancel it here too,
		// so the answer is the run canceled (one of the two finds it final).
		if _, err := s.runs.Cancel(ctx, id); err != nil && !errors.Is(err, store.ErrFinal) {
			s.commandError(w, id, err)
			return
		}
	}
	s.afterCommand(ctx, w, r, id)
}

// lastStored is the Seq of the run's last stored event, or -1.
func lastStored(ctx context.Context, log store.EventLog, id string) int64 {
	seq, err := log.LastEventSeq(ctx, id)
	if err != nil {
		return -1
	}
	return seq
}

// awaitReleased waits until no process holds the run, checking the store
// every poll, or until ctx is done.
func (s *Server) awaitReleased(ctx context.Context, id string) {
	t := time.NewTicker(s.poll()) // real time, as obey's poll
	defer t.Stop()
	for {
		run, err := s.store.Get(ctx, id)
		if err != nil || !held(run, s.cfg.clock.Now()) {
			return
		}
		select {
		case <-t.C:
		case <-ctx.Done():
			return
		}
	}
}

// obey follows the commands of the run h is the hub of, which x executes,
// until ctx is done: a stop or cancel sent through another server stops or
// cancels it here. The returned function stops it and waits for it.
func (s *Server) obey(ctx context.Context, x *runner.Execution, h *hub) func() {
	owner := x.Run.Lease.Owner
	if owner == "" {
		return func() {}
	}
	in, err := s.cmds.Open(ctx, h.runID, owner)
	if err != nil {
		s.log.Error("following a run's commands", "run_id", h.runID, "err", err)
		return func() {}
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer in.Close()
		// The poll is on real time, not the server's clock: it only makes up
		// for a lost signal, and is no part of the run's timing (its lease,
		// timeout or drain), which a fake clock in tests steps through.
		t := time.NewTicker(s.poll())
		defer t.Stop()
		for {
			if s.consume(in, h) {
				return
			}
			select {
			case <-in.Wake():
			case <-t.C:
			case <-ctx.Done():
				return
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

// consume acts on the run's pending commands, and reports whether it
// stopped the run.
func (s *Server) consume(in *stream.Inbox, h *hub) bool {
	cmds, err := in.Pending(store.MaxPageLimit)
	if err != nil || len(cmds) == 0 {
		return false
	}
	verb := ""
	for _, c := range cmds {
		switch c.Type {
		case commandCancel:
			verb = commandCancel
		case commandStop:
			if verb == "" {
				verb = commandStop
			}
		default:
			s.log.Warn("a run's command of an unknown type; dropped", "run_id", h.runID, "type", c.Type)
		}
	}
	if err := in.Consume(cmds[len(cmds)-1].Seq); err != nil {
		s.log.Warn("consuming a run's commands", "run_id", h.runID, "err", err)
		return false
	}
	if verb == "" {
		return false
	}
	s.log.Info("run "+verb+" by a command", "run_id", h.runID)
	ctx := tenancy.With(context.Background(), h.tenant)
	go func() {
		var err error
		if verb == commandCancel {
			_, err = s.runs.Cancel(ctx, h.runID)
		} else {
			_, err = s.runs.Interrupt(ctx, h.runID)
		}
		if err != nil {
			s.log.Warn("acting on a run's command", "run_id", h.runID, "command", verb, "err", err)
		}
	}()
	return true
}
