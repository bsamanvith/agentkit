package client

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// pins remember the fingerprint each wrapped tool had when the model was
// given its definition, and disable a tool once the server's definition
// differs from it: the model would otherwise call it on a description it
// never saw (a rug pull).
type pins struct {
	mu       sync.Mutex
	approved map[string]string // tool name -> fingerprint the model saw
	disabled map[string]string // tool name -> why
}

func (p *pins) set(name, fp string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.approved == nil {
		p.approved = map[string]string{}
	}
	p.approved[name] = fp
}

// why reports why a tool is disabled, or "".
func (p *pins) why(name string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.disabled[name]
}

// Recheck lists the server's tools again and compares each wrapped tool
// with the definition it was wrapped from. A tool whose fingerprint changed,
// or that the server no longer lists, is disabled: calling it fails with
// [tool.ErrToolFailed] saying so. A tool whose definition is back to the
// wrapped one is enabled again. It returns the names disabled now, sorted.
// Tools the server added are not wrapped, so not offered.
func (c *Client) Recheck(ctx context.Context) ([]string, error) {
	defs, err := c.Tools(ctx)
	if err != nil {
		return nil, err
	}
	now := map[string]*mcp.Tool{}
	for _, d := range defs {
		if d != nil {
			now[d.Name] = d
		}
	}
	p := &c.pins
	p.mu.Lock()
	defer p.mu.Unlock()
	disabled := map[string]string{}
	for name, want := range p.approved {
		d, ok := now[name]
		if !ok {
			disabled[name] = "the MCP server no longer lists this tool"
			continue
		}
		fp, err := Fingerprint(d)
		if err != nil || fp != want {
			disabled[name] = "the MCP server changed this tool's definition since it was loaded; it is disabled until an admin reviews the change and the toolset is reloaded"
		}
	}
	p.disabled = disabled
	names := make([]string, 0, len(disabled))
	for n := range disabled {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}

// PinError reports a tool left out because its fingerprint is not the pinned
// one, or because it is not pinned at all.
type PinError struct {
	Tool, Want, Got string // Want is "" for a tool with no pin
}

func (e *PinError) Error() string {
	if e.Want == "" {
		return fmt.Sprintf("tool %q is not pinned (fingerprint %s)", e.Tool, e.Got)
	}
	return fmt.Sprintf("tool %q changed since it was pinned: fingerprint %s, pinned %s", e.Tool, e.Got, e.Want)
}
