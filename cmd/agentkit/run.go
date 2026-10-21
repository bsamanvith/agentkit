package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/pkg/harness"
)

func (c cli) run(ctx context.Context, args []string) error {
	fs := c.newFlagSet("agentkit run", `"<prompt>"`)
	var mf modelFlags
	mf.register(fs)
	system := fs.String("system", "You are a helpful assistant. Use the tools when they help.", "system prompt")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 || strings.TrimSpace(fs.Arg(0)) == "" {
		fs.Usage()
		return errUsage
	}
	h, err := c.harness(ctx, mf, *system)
	if err != nil {
		return err
	}
	p := &printer{w: c.stdout}
	for ev, err := range h.Run(ctx, harness.Input{Message: harness.UserText(fs.Arg(0))}) {
		if err != nil {
			p.end()
			return err
		}
		p.event("", ev)
	}
	return nil
}

func (c cli) harness(ctx context.Context, mf modelFlags, system string) (*harness.Harness, error) {
	model, err := c.newModel(mf)
	if err != nil {
		return nil, err
	}
	tools, err := c.demoTools(ctx)
	if err != nil {
		return nil, err
	}
	return harness.New(model, tools, harness.WithSystem(system), harness.WithMaxTurns(mf.maxTurns))
}

// printer writes harness events to a terminal: the model's text as it
// streams, each tool call with its timing, and usage per turn. It is safe for
// concurrent use; agents running in parallel interleave line by line.
type printer struct {
	w       io.Writer
	mu      sync.Mutex
	midLine bool
	calls   map[string]string // path and call index → tool name
}

func (p *printer) event(path string, ev harness.Event) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch ev.Kind {
	case harness.EventRunStart:
		if path != "" {
			p.line("── %s ──", path)
		}
	case harness.EventTextDelta:
		fmt.Fprint(p.w, ev.Text)
		p.midLine = !strings.HasSuffix(ev.Text, "\n")
	case harness.EventToolStart:
		if p.calls == nil {
			p.calls = map[string]string{}
		}
		p.calls[fmt.Sprint(path, ev.Call)] = ev.ToolUse.Name
		p.line("→ %s %s", ev.ToolUse.Name, ev.ToolUse.Input)
	case harness.EventToolPolicy:
		d := ev.Decision
		p.line("! %s: %s by %s/%s: %s", ev.ToolUse.Name, d.Action, d.Policy, d.Rule, d.Reason)
	case harness.EventInterrupt:
		in := ev.Interrupt
		if in.Kind == harness.InterruptApproval {
			p.line("? %s waits for an approval (%s): %s", in.Name, in.Prompt, in.Input)
		} else {
			p.line("? %s asks: %s %q", in.Name, in.Prompt, in.Choices)
		}
	case harness.EventToolDone:
		name := p.calls[fmt.Sprint(path, ev.Call)]
		status := ""
		if ev.ToolResult.IsError {
			status = " failed"
		}
		p.line("← %s%s (%s): %s", name, status, ms(ev.Duration), clip(ev.ToolResult.Content, 200))
	case harness.EventTurnDone:
		r := ev.TurnRecord
		p.line("· turn %d: %s · model %s · in %d · out %d · cache read %d · cache write %d",
			r.Turn, r.Stop.Kind, ms(r.DoneAt.Sub(r.RequestedAt)),
			r.Usage.InputTokens, r.Usage.OutputTokens, r.Usage.CacheReadTokens, r.Usage.CacheWriteTokens)
	case harness.EventRunDone:
		u := ev.Result.Usage
		p.line("· done: %d turns · in %d · out %d · cache read %d · cache write %d",
			ev.Result.Turns, u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheWriteTokens)
	}
}

// end finishes a line of streamed text, before an error is printed.
func (p *printer) end() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.midLine {
		fmt.Fprintln(p.w)
		p.midLine = false
	}
}

// line prints one line of its own, after any streamed text.
func (p *printer) line(format string, args ...any) {
	if p.midLine {
		fmt.Fprintln(p.w)
		p.midLine = false
	}
	fmt.Fprintf(p.w, format+"\n", args...)
}

func ms(d time.Duration) string { return d.Round(time.Millisecond).String() }

func clip(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", `\n`)
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
