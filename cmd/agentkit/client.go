package main

// What agentkit chat, ask and create share: the server and token, exit codes
// and errors, and how a run's events and messages are shown to a person.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/agentclient"
	"github.com/bsamanvith/agentkit/pkg/llm"
)

const (
	// envServer is the agent API chat and ask talk to, if -server is not
	// given.
	envServer     = "AGENTKIT_SERVER"
	defaultServer = "http://127.0.0.1:8080"
	// defaultRetryWait is how long ask -retry waits when the server sends no
	// Retry-After (a busy conversation).
	defaultRetryWait = time.Second
)

// clientTerm is what chat and ask take from the terminal, replaced in tests.
type clientTerm struct {
	stdin io.Reader // nil: os.Stdin
	// interrupts delivers Ctrl-C to chat; nil: SIGINT.
	interrupts <-chan os.Signal
	// sleep waits for ask -retry; nil: a timer.
	sleep func(ctx context.Context, d time.Duration) error
}

func (c cli) stdin() io.Reader {
	if c.term.stdin == nil {
		return os.Stdin
	}
	return c.term.stdin
}

func (c cli) sleep(ctx context.Context, d time.Duration) error {
	if c.term.sleep != nil {
		return c.term.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Exit codes of the client commands (ask, chat and create, where they apply).
const (
	exitDone        = 0   // the run answered (run_done), or the command did its work
	exitRunFailed   = 1   // the run failed (run_error); its code is on stderr
	exitClient      = 2   // a usage error, an HTTP error, or a broken stream
	exitRetryable   = 3   // conversation_busy, over_capacity or shutting_down: try again later
	exitInterrupted = 130 // Ctrl-C (SIGINT) or SIGTERM
)

// withTokenHint is err's message, with how to set the token when the server
// refused it.
func withTokenHint(err error) string {
	if errors.Is(err, agentclient.ErrUnauthorized) {
		return fmt.Sprintf("%v (set $%s to the server's API token)", err, envAPIToken)
	}
	return err.Error()
}

// clientFlags are the flags of chat, ask and create.
type clientFlags struct {
	server       string
	agent        string
	conversation string
}

func (f *clientFlags) register(fs *flag.FlagSet, c cli) {
	f.registerServer(fs, c)
	fs.StringVar(&f.conversation, "conversation", "", "continue this conversation (default: start a new one)")
}

// registerServer registers -server and -agent only.
func (f *clientFlags) registerServer(fs *flag.FlagSet, c cli) {
	server := c.env(envServer)
	if server == "" {
		server = defaultServer
	}
	fs.StringVar(&f.server, "server", server, "the agent API's base URL, from $"+envServer+" if set")
	fs.StringVar(&f.agent, "agent", "", "the agent of a new conversation (default: the server's default agent)")
}

// client returns the agent API client: the token comes from
// $AGENTKIT_API_TOKEN and is never printed. It has no timeout, which would
// also cut streams; runs are bounded by their context.
func (c cli) client(f clientFlags) (*agentclient.Client, error) {
	opts := []agentclient.Option{agentclient.WithUserAgent("agentkit-cli/" + version)}
	if token := c.env(envAPIToken); token != "" {
		opts = append(opts, agentclient.WithToken(token))
	}
	return agentclient.New(f.server, opts...)
}

// style colours output for a terminal, and is the identity otherwise.
type style struct{ on bool }

// newStyle styles w if it is a terminal, unless $NO_COLOR is set or $TERM is
// "dumb". Only a terminal gets escape sequences.
func (c cli) newStyle(w io.Writer) style {
	f, ok := w.(*os.File)
	if !ok || c.env("NO_COLOR") != "" || c.env("TERM") == "dumb" {
		return style{}
	}
	fi, err := f.Stat()
	return style{on: err == nil && fi.Mode()&os.ModeCharDevice != 0}
}

func (s style) wrap(code, text string) string {
	if !s.on || text == "" {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func (s style) dim(text string) string  { return s.wrap("2", text) }
func (s style) bold(text string) string { return s.wrap("1", text) }
func (s style) red(text string) string  { return s.wrap("31", text) }

// Lengths of what one line shows of a tool's input and result.
const (
	inputWidth  = 100
	resultWidth = 120
)

// oneLine collapses whitespace and cuts s to at most n runes, marking the cut.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// renderer shows a run's events as they come: the text (if text is set),
// each tool call and its result on one line, then the run's usage or error.
type renderer struct {
	w     io.Writer
	st    style
	text  bool
	ended bool // the last write ended a line (or nothing was written)
	block [2]int

	lastCall, lastResult *wire.Event // for chat's /details
}

func newRenderer(w io.Writer, st style, text bool) *renderer {
	return &renderer{w: w, st: st, text: text, ended: true}
}

func (r *renderer) printf(format string, a ...any) {
	s := fmt.Sprintf(format, a...)
	if s == "" {
		return
	}
	_, _ = io.WriteString(r.w, s)
	r.ended = strings.HasSuffix(s, "\n")
}

// line writes a line of its own, ending the text before it if needed.
func (r *renderer) line(s string) {
	if !r.ended {
		r.printf("\n")
	}
	r.printf("%s\n", s)
}

func (r *renderer) event(e wire.Event) {
	switch e.Type {
	case wire.TypeTextDelta:
		if !r.text {
			return
		}
		if b := [2]int{e.Turn, e.Block}; b != r.block && !r.ended {
			r.printf("\n") // a new text block starts a new line
		}
		r.block = [2]int{e.Turn, e.Block}
		r.printf("%s", e.Text)
	case wire.TypeToolCall:
		call := e
		r.lastCall, r.lastResult = &call, nil
		r.line(r.st.dim("  → " + e.Name + " " + oneLine(string(e.Input), inputWidth)))
	case wire.TypeToolResult:
		res := e
		r.lastResult = &res
		mark := "←"
		if e.IsError {
			mark = "✗"
		}
		r.line(r.st.dim(fmt.Sprintf("  %s %s", mark, oneLine(e.Content, resultWidth))))
	case wire.TypeRunDone:
		r.line(r.st.dim("  · " + usageLine(e.Usage, e.Turns)))
	case wire.TypeRunError:
		code, msg := "internal", ""
		if e.Error != nil {
			code, msg = e.Error.Code, e.Error.Message
		}
		r.line(r.st.red("  ✗ run failed: " + code + ": " + msg))
		r.line(r.st.dim("  · " + usageLine(e.Usage, e.Turns)))
	}
}

// usageLine is a run's turns and tokens: "2 turns · 1234 in, 900 cached · 56 out".
func usageLine(u *llm.Usage, turns int) string {
	var b strings.Builder
	if turns == 1 {
		b.WriteString("1 turn")
	} else {
		fmt.Fprintf(&b, "%d turns", turns)
	}
	if u == nil {
		return b.String()
	}
	fmt.Fprintf(&b, " · %d in", u.InputTokens)
	if u.CacheReadTokens > 0 {
		fmt.Fprintf(&b, ", %d cached", u.CacheReadTokens)
	}
	if u.CacheWriteTokens > 0 {
		fmt.Fprintf(&b, ", %d to cache", u.CacheWriteTokens)
	}
	fmt.Fprintf(&b, " · %d out", u.OutputTokens)
	return b.String()
}

// details shows a tool call and its result in full.
func details(call, result *wire.Event) string {
	if call == nil {
		return "no tool call yet\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (call %s)\ninput:\n", call.Name, call.ToolUseID)
	var in bytes.Buffer
	if json.Indent(&in, call.Input, "  ", "  ") == nil {
		fmt.Fprintf(&b, "  %s\n", in.String())
	} else {
		fmt.Fprintf(&b, "  %s\n", call.Input)
	}
	switch {
	case result == nil:
		b.WriteString("no result (the run stopped before the call ended)\n")
	case result.IsError:
		fmt.Fprintf(&b, "error (%.0f ms):\n%s\n", result.DurationMS, result.Content)
	default:
		fmt.Fprintf(&b, "result (%.0f ms):\n%s\n", result.DurationMS, result.Content)
	}
	return b.String()
}

// history shows a conversation's messages, one line per block; a block of a
// type this client does not know shows as a placeholder such as "[image]".
func history(conv wire.Conversation, st style) string {
	var b strings.Builder
	fmt.Fprintf(&b, "conversation %s (agent %s, %s), %d messages\n", conv.ID, conv.Agent, conv.Status, len(conv.Messages))
	for _, tm := range conv.Messages {
		who := "you"
		if tm.Message.Role == llm.RoleAssistant {
			who = "agent"
		}
		for _, blk := range tm.Message.Content {
			switch v := blk.(type) {
			case llm.Text:
				fmt.Fprintf(&b, "%s: %s\n", st.bold(who), v.Text)
			case llm.ToolUse:
				fmt.Fprintf(&b, "%s\n", st.dim("  → "+v.Name+" "+oneLine(string(v.Input), inputWidth)))
			case llm.ToolResult:
				mark := "←"
				if v.IsError {
					mark = "✗"
				}
				fmt.Fprintf(&b, "%s\n", st.dim("  "+mark+" "+oneLine(v.Content, resultWidth)))
			case llm.Opaque:
				fmt.Fprintf(&b, "%s\n", st.dim("  ["+blockType(v.Data, "opaque")+"]"))
			case llm.Unknown:
				fmt.Fprintf(&b, "%s\n", st.dim("  ["+v.Type+"]"))
			default:
				fmt.Fprintf(&b, "%s\n", st.dim(fmt.Sprintf("  [%T]", v)))
			}
		}
	}
	return b.String()
}

// blockType is the "type" of a block's JSON, or def.
func blockType(data json.RawMessage, def string) string {
	var v struct{ Type string }
	if json.Unmarshal(data, &v) != nil || v.Type == "" {
		return def
	}
	return v.Type
}
