package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bsamanvith/agentkit/pkg/tool"
)

// TagReadOnlyHint marks a tool whose server claims it only reads
// (readOnlyHint). The claim does not make it kind read: an admin confirms
// that (E19), until then the tool keeps the unclassified default.
const TagReadOnlyHint = "mcp.read_only_hint"

// maxMessageRunes caps an error message from the server.
const maxMessageRunes = 300

// Wrap returns t as a [tool.Tool] that calls it on the server, under the
// same name. It fails with [tool.ErrInvalidDefinition] when the definition
// cannot be one of ours (a name outside ^[a-zA-Z0-9_-]{1,64}$, an input
// schema that is not an object).
func (c *Client) Wrap(t *mcp.Tool) (tool.Tool, error) {
	if t == nil {
		return nil, fmt.Errorf("%w: nil tool", tool.ErrInvalidDefinition)
	}
	def, err := definition(t)
	if err != nil {
		return nil, err
	}
	// Validate it now, as a registry would.
	if _, err := tool.NewRegistry([]tool.Tool{&remote{def: def}}); err != nil {
		return nil, err
	}
	return &remote{c: c, def: def}, nil
}

// definition converts the server's definition into ours.
func definition(t *mcp.Tool) (tool.Definition, error) {
	in := json.RawMessage(`{"type":"object"}`)
	if t.InputSchema != nil {
		b, err := json.Marshal(t.InputSchema)
		if err != nil {
			return tool.Definition{}, fmt.Errorf("%w: tool %q: input schema: %w", tool.ErrInvalidDefinition, t.Name, err)
		}
		in = b
	}
	var out json.RawMessage
	if t.OutputSchema != nil {
		b, err := json.Marshal(t.OutputSchema)
		if err != nil {
			return tool.Definition{}, fmt.Errorf("%w: tool %q: output schema: %w", tool.ErrInvalidDefinition, t.Name, err)
		}
		out = b
	}
	return tool.Definition{
		Name:         t.Name,
		Description:  SanitizeDescription(t.Description),
		InputSchema:  in,
		OutputSchema: out,
		Metadata:     metadata(t),
	}, nil
}

// metadata classifies a tool from its annotations, as claims: they can raise
// its risk (destructiveHint) but not lower it (readOnlyHint becomes a tag).
func metadata(t *mcp.Tool) tool.Metadata {
	var a *tool.Annotations
	if s := t.Annotations; s != nil {
		a = &tool.Annotations{
			Title: s.Title, ReadOnlyHint: s.ReadOnlyHint, DestructiveHint: s.DestructiveHint,
			IdempotentHint: s.IdempotentHint, OpenWorldHint: s.OpenWorldHint,
		}
	}
	if t.Title != "" && (a == nil || a.Title == "") {
		if a == nil {
			a = &tool.Annotations{}
		}
		a.Title = t.Title
	}
	m := tool.FromAnnotations(a)
	if m.Kind == tool.KindRead {
		m.Kind = ""
		m.Tags = append(m.Tags, TagReadOnlyHint)
	}
	return m
}

// remote is a tool on the server.
type remote struct {
	c   *Client
	def tool.Definition
}

func (r *remote) Definition() tool.Definition { return r.def.Clone() }

// Close closes the client the tool calls through, and so every tool of its
// server: the toolset is done.
func (r *remote) Close() error { return r.c.Close() }

func (r *remote) Invoke(ctx context.Context, input json.RawMessage) (json.RawMessage, error) {
	if !isObject(input) {
		return nil, &tool.Error{Tool: r.def.Name, Kind: tool.ErrInvalidInput, Err: errors.New("input must be a JSON object")}
	}
	res, err := r.c.Call(ctx, r.def.Name, input)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, fmt.Errorf("tool %q: %w", r.def.Name, cerr)
		}
		return nil, r.failed(r.c.describe(err))
	}
	return r.output(res)
}

// output converts a result: its structured content when there is some, else
// its content as text, {"text": "..."}.
func (r *remote) output(res *mcp.CallToolResult) (json.RawMessage, error) {
	if res == nil {
		return nil, r.failed("the MCP server sent no result")
	}
	if res.IsError {
		msg := cut(contentText(res.Content))
		if msg == "" {
			msg = "the tool reported an error and gave no message"
		}
		return nil, r.failed(msg)
	}
	var out []byte
	var err error
	if res.StructuredContent != nil {
		out, err = json.Marshal(res.StructuredContent)
	} else {
		out, err = json.Marshal(struct {
			Text string `json:"text"`
		}{contentText(res.Content)})
	}
	switch {
	case err != nil:
		return nil, r.failed("the MCP server's result is not valid JSON")
	case len(out) > r.c.cfg.MaxResultBytes:
		return nil, r.failed(fmt.Sprintf("the result is %d bytes, over this toolset's limit of %d", len(out), r.c.cfg.MaxResultBytes))
	case !isObject(out) && r.def.OutputSchema != nil:
		return nil, r.failed("the MCP server's structured result is not an object")
	}
	return out, nil
}

func (r *remote) failed(msg string) error {
	return &tool.Error{Tool: r.def.Name, Kind: tool.ErrToolFailed, Err: errors.New(msg)}
}

// describe says what went wrong with a call, for the model: no URLs or
// headers, and the server's own message cut short.
func (c *Client) describe(err error) string {
	var rpc *jsonrpc.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Sprintf("the MCP server did not answer within %s", c.cfg.CallTimeout)
	case errors.Is(err, errTooLarge):
		return fmt.Sprintf("the result is over this toolset's limit of %d bytes", c.cfg.MaxResultBytes)
	case errors.As(err, &rpc):
		return "the MCP server refused the call: " + cut(rpc.Message)
	case errors.Is(err, mcp.ErrSessionMissing), errors.Is(err, mcp.ErrConnectionClosed):
		return "the connection to the MCP server was lost; try again"
	case errors.Is(err, ErrClosed):
		return "the MCP toolset is closed"
	}
	return "calling the MCP server failed: " + cut(err.Error())
}

// contentText is a result's content as text: text as it is, anything else as
// a short note of what it was.
func contentText(content []mcp.Content) string {
	parts := make([]string, 0, len(content))
	for _, c := range content {
		switch c := c.(type) {
		case *mcp.TextContent:
			parts = append(parts, c.Text)
		case *mcp.ImageContent:
			parts = append(parts, fmt.Sprintf("[image, %s, %d bytes]", c.MIMEType, len(c.Data)))
		case *mcp.AudioContent:
			parts = append(parts, fmt.Sprintf("[audio, %s, %d bytes]", c.MIMEType, len(c.Data)))
		case *mcp.ResourceLink:
			parts = append(parts, "[resource link: "+c.URI+"]")
		case *mcp.EmbeddedResource:
			if c.Resource != nil && c.Resource.Text != "" {
				parts = append(parts, c.Resource.Text)
			} else if c.Resource != nil {
				parts = append(parts, "[resource: "+c.Resource.URI+"]")
			}
		}
	}
	return strings.Join(parts, "\n")
}

// cut shortens a message from the server to maxMessageRunes runes.
func cut(s string) string {
	s = strings.TrimSpace(strings.ToValidUTF8(s, "\uFFFD"))
	r := []rune(s)
	if len(r) <= maxMessageRunes {
		return s
	}
	return string(r[:maxMessageRunes]) + "…"
}

func isObject(b []byte) bool {
	b = bytes.TrimSpace(b)
	return len(b) > 0 && b[0] == '{' && json.Valid(b)
}

// rawObject passes JSON arguments through without decoding them.
func rawObject(b []byte) json.RawMessage {
	if len(bytes.TrimSpace(b)) == 0 {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(b)
}
