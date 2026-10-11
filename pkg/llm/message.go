package llm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"unicode/utf8"
)

// ErrInvalidMessage reports a message that cannot be encoded to or decoded
// from JSON: an unknown role or block type, a missing field, a nil block, or
// an [Opaque] block whose data is not valid JSON.
var ErrInvalidMessage = errors.New("invalid message")

// Role says who wrote a [Message].
type Role string

// The roles of a conversation. System prompts are not messages; they are
// [Request.System].
const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

func (r Role) valid() bool { return r == RoleUser || r == RoleAssistant }

// Message is one turn of a conversation.
type Message struct {
	Role    Role
	Content []Block
}

// User returns a user message with the given blocks.
func User(blocks ...Block) Message { return Message{Role: RoleUser, Content: blocks} }

// Assistant returns an assistant message with the given blocks.
func Assistant(blocks ...Block) Message { return Message{Role: RoleAssistant, Content: blocks} }

// Text returns the text of the message: its [Text] blocks, concatenated.
func (m Message) Text() string {
	var b []byte
	for _, blk := range m.Content {
		if t, ok := blk.(Text); ok {
			b = append(b, t.Text...)
		}
	}
	return string(b)
}

// Clone returns a deep copy of m.
func (m Message) Clone() Message {
	if m.Content != nil {
		content := make([]Block, len(m.Content))
		for i, b := range m.Content {
			if b != nil {
				content[i] = b.clone()
			}
		}
		m.Content = content
	}
	return m
}

// Block is one piece of a message's content. The set of blocks is closed:
// [Text], [ToolUse], [ToolResult] and [Opaque].
type Block interface {
	// wire returns the JSON-encodable form of the block, tagged with its type.
	wire() (any, error)
	clone() Block
}

// Text is plain text.
type Text struct {
	Text string
}

// ToolUse is the model asking for a tool to be run.
type ToolUse struct {
	// ID links the call to its [ToolResult]. It is chosen by the provider.
	ID string
	// Name is the tool's name, as in tool.Definition.
	Name string
	// Input is the tool's input: valid JSON, normally an object. Marshaling
	// keeps it semantically identical but compact and with <, > and & written
	// as \u escapes, so Input of a decoded message is already in that form.
	Input json.RawMessage
}

// ToolResult is the answer to a [ToolUse], sent in a user message.
type ToolResult struct {
	// ToolUseID is the ID of the [ToolUse] this answers.
	ToolUseID string
	// Content is what the model is shown. For a failure, it describes the error.
	Content string
	// IsError marks a failed tool call, so the model can react to it.
	IsError bool
}

// Opaque is a block of one provider that agentkit does not interpret: Claude's
// thinking and redacted-thinking blocks (which carry signatures), compaction
// blocks, and so on.
//
// An adapter returns the Data of its own provider's Opaque blocks in the next
// request exactly as it received it, and leaves blocks of other providers out.
// Data therefore survives JSON byte for byte, however it is encoded (see
// [Opaque.MarshalJSON]).
type Opaque struct {
	// Provider names the adapter that produced the block, for example "anthropic".
	Provider string
	// Data is the provider's block as JSON in UTF-8. It is never modified.
	Data json.RawMessage
}

const (
	typeText       = "text"
	typeToolUse    = "tool_use"
	typeToolResult = "tool_result"
	typeOpaque     = "opaque"
)

type textWire struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolUseWire struct {
	Type  string          `json:"type"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type toolResultWire struct {
	Type      string `json:"type"`
	ToolUseID string `json:"tool_use_id"`
	Content   string `json:"content"`
	IsError   bool   `json:"is_error,omitempty"`
}

// opaqueWire carries Data as a JSON string. A raw JSON value embedded in the
// output would be compacted and HTML-escaped by encoding/json (even when it
// comes from a MarshalJSON method) and re-indented by MarshalIndent, which
// would change bytes that must stay identical. A string is decoded back to
// exactly the bytes it was made from, under any encoder settings.
type opaqueWire struct {
	Type     string `json:"type"`
	Provider string `json:"provider"`
	Data     string `json:"data"`
}

// Block implementations.

func (t Text) clone() Block { return t }

func (t Text) wire() (any, error) {
	return textWire{Type: typeText, Text: t.Text}, nil
}

func (u ToolUse) clone() Block {
	u.Input = bytes.Clone(u.Input)
	return u
}

func (u ToolUse) wire() (any, error) {
	input := u.Input
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	if !json.Valid(input) {
		return nil, fmt.Errorf("%w: input of tool use %q is not valid JSON", ErrInvalidMessage, u.ID)
	}
	return toolUseWire{Type: typeToolUse, ID: u.ID, Name: u.Name, Input: input}, nil
}

func (r ToolResult) clone() Block { return r }
func (r ToolResult) wire() (any, error) {
	return toolResultWire{Type: typeToolResult, ToolUseID: r.ToolUseID, Content: r.Content, IsError: r.IsError}, nil
}

func (o Opaque) clone() Block {
	o.Data = bytes.Clone(o.Data)
	return o
}

func (o Opaque) wire() (any, error) {
	if err := checkOpaque(o.Provider, o.Data); err != nil {
		return nil, err
	}
	return opaqueWire{Type: typeOpaque, Provider: o.Provider, Data: string(o.Data)}, nil
}

func checkOpaque(provider string, data []byte) error {
	switch {
	case provider == "":
		return fmt.Errorf("%w: opaque block has no provider", ErrInvalidMessage)
	case !utf8.Valid(data) || !json.Valid(data):
		return fmt.Errorf("%w: opaque block of %q: data is not valid JSON in UTF-8", ErrInvalidMessage, provider)
	}
	return nil
}

// MarshalJSON encodes the block with a "type" discriminator.
func (t Text) MarshalJSON() ([]byte, error) { return marshalBlock(t) }

// MarshalJSON encodes the block with a "type" discriminator.
func (u ToolUse) MarshalJSON() ([]byte, error) { return marshalBlock(u) }

// MarshalJSON encodes the block with a "type" discriminator.
func (r ToolResult) MarshalJSON() ([]byte, error) { return marshalBlock(r) }

// MarshalJSON encodes the block with a "type" discriminator. Data is written
// as a JSON string holding its exact bytes, so that decoding gives back
// identical bytes whatever the encoder does to whitespace or HTML characters.
func (o Opaque) MarshalJSON() ([]byte, error) { return marshalBlock(o) }

func marshalBlock(b Block) ([]byte, error) {
	w, err := b.wire()
	if err != nil {
		return nil, err
	}
	return json.Marshal(w)
}

// MarshalJSON encodes the message as {"role": ..., "content": [...]}. It fails
// with [ErrInvalidMessage] for an unknown role or a nil block.
func (m Message) MarshalJSON() ([]byte, error) {
	if !m.Role.valid() {
		return nil, fmt.Errorf("%w: unknown role %q", ErrInvalidMessage, m.Role)
	}
	content := m.Content
	if content == nil {
		content = []Block{}
	}
	for i, b := range content {
		if b == nil {
			return nil, fmt.Errorf("%w: block %d is nil", ErrInvalidMessage, i)
		}
	}
	return json.Marshal(struct {
		Role    Role    `json:"role"`
		Content []Block `json:"content"`
	}{m.Role, content})
}

// UnmarshalJSON decodes a message written by MarshalJSON. Unknown roles, block
// types and fields are errors ([ErrInvalidMessage]): content is never dropped
// silently.
func (m *Message) UnmarshalJSON(data []byte) error {
	var w struct {
		Role    Role              `json:"role"`
		Content []json.RawMessage `json:"content"`
	}
	if err := strictDecode(data, &w); err != nil {
		return err
	}
	if !w.Role.valid() {
		return fmt.Errorf("%w: unknown role %q", ErrInvalidMessage, w.Role)
	}
	var content []Block
	for _, raw := range w.Content {
		b, err := UnmarshalBlock(raw)
		if err != nil {
			return err
		}
		content = append(content, b)
	}
	*m = Message{Role: w.Role, Content: content}
	return nil
}

// UnmarshalBlock decodes one block written by the block's MarshalJSON. It
// returns the block as a value ([Text], [ToolUse], [ToolResult] or [Opaque]),
// and [ErrInvalidMessage] for an unknown or missing "type".
func UnmarshalBlock(data []byte) (Block, error) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, fmt.Errorf("%w: block: %w", ErrInvalidMessage, err)
	}
	switch head.Type {
	case typeText:
		var w textWire
		if err := strictDecode(data, &w); err != nil {
			return nil, err
		}
		return Text{Text: w.Text}, nil
	case typeToolUse:
		var w toolUseWire
		if err := strictDecode(data, &w); err != nil {
			return nil, err
		}
		return decodeToolUse(w)
	case typeToolResult:
		var w toolResultWire
		if err := strictDecode(data, &w); err != nil {
			return nil, err
		}
		return ToolResult{ToolUseID: w.ToolUseID, Content: w.Content, IsError: w.IsError}, nil
	case typeOpaque:
		var w opaqueWire
		if err := strictDecode(data, &w); err != nil {
			return nil, err
		}
		if err := checkOpaque(w.Provider, []byte(w.Data)); err != nil {
			return nil, err
		}
		return Opaque{Provider: w.Provider, Data: json.RawMessage(w.Data)}, nil
	default:
		return nil, fmt.Errorf("%w: unknown block type %q", ErrInvalidMessage, head.Type)
	}
}

// decodeToolUse puts Input in the form Marshal writes (compact, HTML
// characters escaped), so that decode, encode and decode again is stable.
func decodeToolUse(w toolUseWire) (Block, error) {
	input := w.Input
	if len(input) == 0 || string(input) == "null" {
		input = json.RawMessage(`{}`)
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, input); err != nil {
		return nil, fmt.Errorf("%w: tool use input: %w", ErrInvalidMessage, err)
	}
	var escaped bytes.Buffer
	json.HTMLEscape(&escaped, compact.Bytes())
	return ToolUse{ID: w.ID, Name: w.Name, Input: slices.Clone(escaped.Bytes())}, nil
}

// strictDecode decodes exactly one JSON value into v and rejects unknown
// fields and trailing data.
func strictDecode(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidMessage, err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing data after JSON value", ErrInvalidMessage)
	}
	return nil
}
