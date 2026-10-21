package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
)

const chatHelp = `  /new      start a new conversation
  /conv     print the conversation's ID
  /history  show the conversation so far
  /details  show the last tool call and its result in full
  /quit     quit (so does Ctrl-D)
  When a tool asks you something, or a call needs your approval, the next
  line answers it (y, or n and why, for an approval).
  Ctrl-C stops the answer that is coming; at the prompt it quits.
`

// chat is a REPL: each line is a message to the agent, whose answer streams
// back with its tool calls and usage; lines starting with / are commands.
func (c cli) chat(ctx context.Context, args []string) int {
	fs := c.newFlagSet("agentkit chat", "")
	var f clientFlags
	f.register(fs, c)
	if err := fs.Parse(args); err != nil {
		return exitClient
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return exitClient
	}
	client, err := c.client(f)
	if err != nil {
		fmt.Fprintln(c.stderr, "agentkit chat:", err)
		return exitClient
	}
	interrupts := c.term.interrupts
	if interrupts == nil { // main leaves SIGINT to chat
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt)
		defer signal.Stop(ch)
		interrupts = ch
	}
	st := c.newStyle(c.stdout)
	s := &chatSession{
		client: client, agent: f.agent, conv: f.conversation,
		out: c.stdout, st: st, interrupts: interrupts,
		r: newRenderer(c.stdout, st, true),
	}
	return s.loop(ctx, c.stdin(), f.server)
}

// chatSession is the state of one chat.
type chatSession struct {
	client     *agentclient.Client
	agent      string
	out        io.Writer
	st         style
	interrupts <-chan os.Signal
	r          *renderer

	conv        string // the conversation; empty until the next message starts one
	interrupted bool   // the conversation's last run may have stopped before answering

	// waiting is what the last run waits for, in order: the next line
	// answers the first. run is that run, and last its last event.
	waiting []wire.Interrupt
	run     string
	last    string
}

func (s *chatSession) loop(ctx context.Context, in io.Reader, server string) int {
	lines := make(chan string)
	quit := make(chan struct{})
	defer close(quit)
	go func() {
		defer close(lines)
		br := bufio.NewReader(in)
		for {
			line, err := br.ReadString('\n')
			if line != "" {
				select {
				case lines <- line:
				case <-quit:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	banner := "agentkit chat · " + server
	if s.agent != "" {
		banner += " · agent " + s.agent
	}
	if s.conv != "" {
		banner += " · conversation " + s.conv
	}
	s.say("%s · /help for commands, Ctrl-D quits", banner)
	for {
		_, _ = io.WriteString(s.out, s.st.bold("> "))
		var line string
		select {
		case l, ok := <-lines:
			if !ok { // Ctrl-D, or the end of piped input
				_, _ = io.WriteString(s.out, "\n")
				return exitDone
			}
			line = strings.TrimRight(l, "\r\n")
		case <-s.interrupts:
			_, _ = io.WriteString(s.out, "\n")
			return exitInterrupted
		case <-ctx.Done():
			_, _ = io.WriteString(s.out, "\n")
			return exitInterrupted
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			if s.interrupted && s.conv != "" {
				s.send(ctx, "") // resume the interrupted run
			}
		case strings.HasPrefix(trimmed, "/"):
			if s.command(ctx, trimmed) {
				return exitDone
			}
		case len(s.waiting) > 0:
			s.answer(ctx, trimmed)
		default:
			s.send(ctx, line)
		}
	}
}

// say writes a line of chat's own, dimmed on a terminal.
func (s *chatSession) say(format string, a ...any) {
	s.r.line(s.st.dim(fmt.Sprintf(format, a...)))
}

// command runs a / command and reports whether chat should quit.
func (s *chatSession) command(ctx context.Context, line string) bool {
	switch name := strings.Fields(line)[0]; name {
	case "/quit", "/exit":
		return true
	case "/new":
		s.conv, s.interrupted, s.waiting = "", false, nil
		s.say("new conversation: the next message starts it")
	case "/conv":
		if s.conv == "" {
			s.say("no conversation yet: the next message starts one")
		} else {
			s.r.line(s.conv)
		}
	case "/history":
		if s.conv == "" {
			s.say("no conversation yet: the next message starts one")
			return false
		}
		conv, err := s.client.GetConversation(ctx, s.conv)
		if err != nil {
			s.r.line(s.st.red("error: " + err.Error()))
			return false
		}
		s.r.printf("%s", history(conv, s.st))
	case "/details":
		s.r.printf("%s", details(s.r.lastCall, s.r.lastResult))
	case "/help":
		s.r.printf("%s", chatHelp)
	default:
		s.say("unknown command %s; /help lists them", name)
	}
	return false
}

// send runs one message (empty to resume an interrupted run) and shows the
// answer as it streams. Ctrl-C meanwhile stops the run on the server, which
// keeps it to resume.
func (s *chatSession) send(ctx context.Context, message string) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		cancelled atomic.Bool
		runID     string // set before opened closes
	)
	stop, exited, opened := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(exited)
		select {
		case <-s.interrupts:
			cancelled.Store(true)
			// A Ctrl-C just after Enter: let the stream open first (briefly),
			// so chat learns the run to stop and the conversation to resume,
			// instead of losing them with the request.
			select {
			case <-opened:
				if runID != "" {
					// Closing the stream alone would leave the run going for
					// the server's reconnect grace. The stop answers once
					// the run has ended, which chat waits for anyway.
					stopped := make(chan struct{})
					go func() {
						defer close(stopped)
						_, _ = s.client.Stop(ctx, runID)
					}()
					defer func() { <-stopped }()
				}
			case <-time.After(openGrace):
			case <-stop:
			}
			cancel()
		case <-stop:
		}
	}()
	defer func() { close(stop); <-exited }()

	stream, err := s.client.Stream(runCtx, agentclient.RunRequest{Message: message, ConversationID: s.conv, Agent: s.agent})
	if err == nil {
		runID = stream.RunID()
	}
	close(opened)
	if err == nil && s.conv == "" {
		// From the headers, before any event: a Ctrl-C before the first one
		// still knows which conversation to check and resume.
		s.conv = stream.ConversationID()
	}
	if err != nil {
		if cancelled.Load() {
			s.say("cancelled")
			return
		}
		s.refused(err, message)
		return
	}
	defer stream.Close()
	s.interrupted = false
	s.follow(ctx, stream, &cancelled)
}

// follow shows a run's events as they stream, and remembers what the run
// waits for when it ends waiting for input.
func (s *chatSession) follow(ctx context.Context, stream *agentclient.Stream, cancelled *atomic.Bool) {
	s.r.block = [2]int{}
	s.run, s.waiting = stream.RunID(), nil
	var waiting []wire.Interrupt
	for {
		e, err := stream.Next()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			if cancelled.Load() {
				s.cancelled(ctx)
				return
			}
			s.r.line(s.st.red("  ✗ " + err.Error()))
			return
		}
		if s.conv == "" { // from the response headers, or else run_start
			s.conv = stream.ConversationID()
		}
		if e.Type == wire.TypeRunError && cancelled.Load() {
			s.cancelled(ctx) // the stop's end, not a failure
			return
		}
		s.last = e.ID
		switch {
		case e.Type == wire.TypeInterrupt && e.Interrupt != nil:
			waiting = append(waiting, *e.Interrupt)
		case e.Type == wire.TypeTurnDone:
			waiting = nil
		case e.Type == wire.TypeRunError && e.Error != nil && e.Error.Code == wire.CodeSuspended:
			s.waiting = waiting
		}
		s.r.event(e)
	}
}

// answer sends line as the answer to the first interrupt the run waits for.
// Once the run has all its answers, it goes on, and chat follows it.
func (s *chatSession) answer(ctx context.Context, line string) {
	in := s.waiting[0]
	run, err := s.client.Answer(ctx, s.run, answerOf(in, line))
	if err != nil {
		s.r.line(s.st.red("error: " + err.Error()))
		if errors.Is(err, agentclient.ErrRunState) {
			s.waiting = nil // the run waits for nothing more
		}
		return
	}
	if run.Status == wire.StatusSuspended {
		// Ask the next one the run still waits for.
		s.waiting = slices.DeleteFunc(slices.Clone(run.Interrupts), func(in wire.Interrupt) bool { return in.Answer != nil })
		if len(s.waiting) > 0 {
			s.r.line(s.st.bold("  ? " + interruptLine(s.waiting[0])))
		}
		return
	}
	s.waiting = nil
	// The run goes on: Ctrl-C stops it on the server, as for a message.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var cancelled atomic.Bool
	runID := s.run
	stop, exited := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(exited)
		select {
		case <-s.interrupts:
			cancelled.Store(true)
			stopped := make(chan struct{})
			go func() {
				defer close(stopped)
				_, _ = s.client.Stop(ctx, runID)
			}()
			defer func() { <-stopped }()
			cancel()
		case <-stop:
		}
	}()
	defer func() { close(stop); <-exited }()
	stream, err := s.client.Events(runCtx, runID, s.last)
	if err != nil {
		if cancelled.Load() {
			s.cancelled(ctx)
			return
		}
		s.r.line(s.st.red("error: " + err.Error()))
		return
	}
	defer stream.Close()
	s.follow(ctx, stream, &cancelled)
}

// cancelled waits until the server has ended the cancelled run (so that the
// next message does not find the conversation busy), then says whether it
// can be resumed.
func (s *chatSession) cancelled(ctx context.Context) {
	status := ""
	for deadline := time.Now().Add(settleTimeout); s.conv != ""; {
		conv, err := s.client.GetConversation(ctx, s.conv)
		if err != nil {
			break
		}
		if status = conv.Status; status != wire.StatusRunning || time.Now().After(deadline) {
			break
		}
		select {
		case <-time.After(settlePoll):
		case <-ctx.Done():
			return
		}
	}
	s.interrupted = status == wire.StatusInterrupted
	if s.interrupted {
		s.say("cancelled before it answered: Enter on an empty line resumes the run; /new starts a new conversation")
	} else {
		s.say("cancelled")
	}
}

// How long chat waits for the server to end a cancelled run (longer than the
// server's reconnect grace, which ends a run chat could not stop), and how
// often it asks; and how long a Ctrl-C waits for a run's stream to open
// before it gives up on the request.
const (
	settleTimeout = 15 * time.Second
	settlePoll    = 50 * time.Millisecond
	openGrace     = 2 * time.Second
)

// refused explains a run the server did not start.
func (s *chatSession) refused(err error, message string) {
	switch {
	case errors.Is(err, agentclient.ErrConversationInterrupted):
		s.interrupted = true
		s.say("the last run stopped before it answered: Enter on an empty line resumes it; /new starts a new conversation")
	case message == "" && errors.Is(err, agentclient.ErrInvalidRequest):
		s.interrupted = false
		s.say("nothing to resume")
	case errors.Is(err, agentclient.ErrConversationBusy):
		s.say("a run of this conversation is going on; try again when it has ended")
	case errors.Is(err, agentclient.ErrOverCapacity), errors.Is(err, agentclient.ErrShuttingDown):
		s.r.line(s.st.red("the server is busy, try again in a moment: " + err.Error()))
	default:
		s.r.line(s.st.red("error: " + withTokenHint(err)))
	}
}
