package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
)

// Exit codes of ask (and chat, where they apply).
const (
	exitDone        = 0   // the run answered (run_done)
	exitRunFailed   = 1   // the run failed (run_error); its code is on stderr
	exitClient      = 2   // a usage error, an HTTP error, or a broken stream
	exitRetryable   = 3   // conversation_busy, over_capacity or shutting_down: try again later
	exitInterrupted = 130 // Ctrl-C (SIGINT) or SIGTERM
)

// maxAskInput limits a message read from stdin.
const maxAskInput = 8 << 20

// ask runs one message to its end and prints the answer, or with -json every
// event as one JSON line (the SSE data, as the server sent it).
func (c cli) ask(ctx context.Context, args []string) int {
	fs := c.newFlagSet("agentkit ask", `"<message>" | -`)
	var f clientFlags
	f.register(fs, c)
	asJSON := fs.Bool("json", false, "print every event of the run as one JSON line (JSONL), instead of the answer")
	verbose := fs.Bool("v", false, "show the conversation, tool calls, results and usage on stderr")
	retries := fs.Int("retry", 0, "when the conversation is busy or the server over capacity, wait (Retry-After) and retry up to `N` times")
	if err := fs.Parse(args); err != nil {
		return exitClient
	}
	if fs.NArg() != 1 || *retries < 0 {
		fs.Usage()
		return exitClient
	}
	message := fs.Arg(0)
	if message == "-" {
		b, err := io.ReadAll(io.LimitReader(c.stdin(), maxAskInput+1))
		if err != nil {
			fmt.Fprintln(c.stderr, "agentkit ask: reading stdin:", err)
			return exitClient
		}
		if len(b) > maxAskInput {
			fmt.Fprintf(c.stderr, "agentkit ask: the message on stdin is over %d bytes\n", maxAskInput)
			return exitClient
		}
		message = strings.TrimSuffix(string(b), "\n")
	}
	client, err := c.client(f)
	if err != nil {
		fmt.Fprintln(c.stderr, "agentkit ask:", err)
		return exitClient
	}

	req := agentclient.RunRequest{Message: message, ConversationID: f.conversation, Agent: f.agent}
	var s *agentclient.Stream
	for attempt := 0; ; attempt++ {
		s, err = client.Stream(ctx, req)
		if err == nil {
			break
		}
		if !retryable(err) || attempt == *retries || ctx.Err() != nil {
			return c.requestFailed(ctx, err, f.conversation)
		}
		wait := defaultRetryWait
		var ae *agentclient.APIError
		if errors.As(err, &ae) && ae.RetryAfter > 0 {
			wait = ae.RetryAfter
		}
		fmt.Fprintf(c.stderr, "agentkit ask: %v; retrying in %s (%d of %d)\n", err, wait, attempt+1, *retries)
		if err := c.sleep(ctx, wait); err != nil {
			return exitInterrupted
		}
	}
	defer s.Close()

	progress := newRenderer(c.stderr, style{}, false)
	var end wire.Event
	for {
		e, err := s.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if ctx.Err() != nil {
				fmt.Fprintln(c.stderr, "agentkit ask: interrupted; the run was cancelled")
				return exitInterrupted
			}
			fmt.Fprintln(c.stderr, "agentkit ask:", err)
			return exitClient
		}
		if *asJSON {
			// One write per line, before the next Next (which invalidates
			// RawData), so a reader sees each event as it comes.
			line := append(append([]byte(nil), s.RawData()...), '\n')
			if _, err := c.stdout.Write(line); err != nil {
				fmt.Fprintln(c.stderr, "agentkit ask: writing stdout:", err)
				return exitClient
			}
		}
		if *verbose {
			if e.Type == wire.TypeRunStart {
				fmt.Fprintf(c.stderr, "  · conversation %s, run %s\n", e.ConversationID, e.RunID)
			}
			progress.event(e)
		}
		if e.Type == wire.TypeRunDone || e.Type == wire.TypeRunError {
			end = e
		}
	}

	if end.Type == wire.TypeRunError {
		code, msg := wire.CodeInternal, ""
		if end.Error != nil {
			code, msg = end.Error.Code, end.Error.Message
		}
		fmt.Fprintf(c.stderr, "agentkit ask: run failed: %s: %s\n", code, msg)
		return exitRunFailed
	}
	if !*asJSON {
		answer := end.Answer
		if !strings.HasSuffix(answer, "\n") {
			answer += "\n"
		}
		if _, err := io.WriteString(c.stdout, answer); err != nil {
			fmt.Fprintln(c.stderr, "agentkit ask: writing stdout:", err)
			return exitClient
		}
	}
	return exitDone
}

// retryable reports whether a refused run may start if asked again later.
func retryable(err error) bool {
	return errors.Is(err, agentclient.ErrConversationBusy) ||
		errors.Is(err, agentclient.ErrOverCapacity) ||
		errors.Is(err, agentclient.ErrShuttingDown)
}

// requestFailed reports a run the server refused, or could not be asked, and
// returns ask's exit code.
func (c cli) requestFailed(ctx context.Context, err error, conversation string) int {
	switch {
	case ctx.Err() != nil:
		fmt.Fprintln(c.stderr, "agentkit ask: interrupted")
		return exitInterrupted
	case retryable(err):
		fmt.Fprintln(c.stderr, "agentkit ask:", err)
		return exitRetryable
	case errors.Is(err, agentclient.ErrConversationInterrupted):
		fmt.Fprintf(c.stderr, "agentkit ask: %v; resume it with: agentkit ask -conversation %s \"\"\n", err, conversation)
	case errors.Is(err, agentclient.ErrUnauthorized):
		fmt.Fprintf(c.stderr, "agentkit ask: %v (set $%s to the server's API token)\n", err, envAPIToken)
	default:
		fmt.Fprintln(c.stderr, "agentkit ask:", err)
	}
	return exitClient
}
