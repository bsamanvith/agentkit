package llm

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"unicode/utf8"
)

// ErrInvalidMessage reports a message that cannot be encoded to or decoded
// from JSON: an unknown role or block type, a missing field, a nil block, an
// [Opaque] block whose data is not valid JSON, or an [Unknown] block that is
// not a JSON object of its type.
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

// HasUnknown reports whether m has an [Unknown] block: the one block that
// marshals but that strict decoding ([Message.UnmarshalJSON]) does not read
// back. Code that writes messages to read them back strictly, such as a
// store, refuses such a message rather than write what it cannot read.
func (m Message) HasUnknown() bool {
	for _, b := range m.Content {
		if _, ok := b.(Unknown); ok {
			return true
		}
	}
	return false
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
// [Text], [ToolUse], [ToolResult] and [Opaque], and [Unknown] for a block of a
// newer version read by [UnmarshalMessageLenient]. Blocks are values, never
// pointers: marshaling a pointer block is an error.
//
// Strings in blocks are JSON strings, so invalid UTF-8 in them is replaced by
// U+FFFD when a message is marshaled. [Opaque] data is the exception: it is
// rejected if it is not valid UTF-8.
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
	// ID links the call to its [ToolResult]. It is chosen by the provider and
	// must not be empty.
	ID string
	// Name is the tool's name, as in tool.Definition. It must not be empty.
	Name string
	// Input is the tool's input: valid JSON, normally an object, and `{}`
	// (not nil) for a call without arguments. Adapters must not put
	// incomplete JSON here, for example from a reply cut off by the token
	// limit: drop that block and report [StopMaxTokens]. Marshaling
	// keeps it semantically identical but compact and with <, > and & written
	// as \u escapes, so Input of a decoded message is already in that form.
	Input json.RawMessage
}

// ToolResult is the answer to a [ToolUse], sent in a user message.
type ToolResult struct {
	// ToolUseID is the ID of the [ToolUse] this answers. It must not be empty.
	ToolUseID string
	// Content is what the model is shown. For a failure, it describes the error.
	Content string
	// IsError marks a failed tool call, so the model can react to it.
	IsError bool
	// Loads names deferred tools (see [Request.Deferred]) this result makes
	// available from then on, such as the tools a tool search found. An
	// adapter whose provider loads deferred tools itself sends them as
	// references the provider expands (Claude's tool_reference blocks),
	// after Content; the others ignore it. Nil for most results.
	Loads []string
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

// Unknown is a block of a type this version of agentkit does not know, kept
// as its JSON. Only [UnmarshalMessageLenient] makes one, when it reads a
// message written by a newer version (for example a client of the agent API
// reading a newer server's events). Marshaling writes Data back, so the block
// survives a round trip.
//
// An Unknown block is never sent to a model: adapters reject it as an
// unsupported block, and so does the harness. Strict decoding
// ([Message.UnmarshalJSON]) never produces one.
type Unknown struct {
	// Type is the block's "type", one this version does not know.
	Type string
	// Data is the block's JSON: an object whose "type" is Type.
	Data json.RawMessage
}

const (
	typeText       = "text"
	typeToolUse    = "tool_use"
	typeToolResult = "tool_result"
	typeOpaque     = "opaque"
)

// blockTypes are the types of the blocks this package knows.
var blockTypes = []string{typeText, typeToolUse, typeToolResult, typeOpaque}

func knownType(typ string) bool { return slices.Contains(blockTypes, typ) }

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
	Type      string   `json:"type"`
	ToolUseID string   `json:"tool_use_id"`
	Content   string   `json:"content"`
	IsError   bool     `json:"is_error,omitempty"`
	Loads     []string `json:"loads,omitempty"`
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
	if u.ID == "" || u.Name == "" {
		return nil, fmt.Errorf("%w: tool use needs an ID and a name", ErrInvalidMessage)
	}
	input := u.Input
	if len(input) == 0 || string(input) == "null" {
		input = json.RawMessage(`{}`)
	}
	if !json.Valid(input) {
		return nil, fmt.Errorf("%w: input of tool use %q is not valid JSON", ErrInvalidMessage, u.ID)
	}
	return toolUseWire{Type: typeToolUse, ID: u.ID, Name: u.Name, Input: input}, nil
}

func (r ToolResult) clone() Block {
	r.Loads = slices.Clone(r.Loads)
	return r
}

func (r ToolResult) wire() (any, error) {
	if r.ToolUseID == "" {
		return nil, fmt.Errorf("%w: tool result needs a tool use ID", ErrInvalidMessage)
	}
	return toolResultWire{Type: typeToolResult, ToolUseID: r.ToolUseID, Content: r.Content, IsError: r.IsError, Loads: r.Loads}, nil
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

func (u Unknown) clone() Block {
	u.Data = bytes.Clone(u.Data)
	return u
}

func (u Unknown) wire() (any, error) {
	switch {
	case u.Type == "":
		return nil, fmt.Errorf("%w: block has no type", ErrInvalidMessage)
	case knownType(u.Type):
		return nil, fmt.Errorf("%w: unknown block of known type %q", ErrInvalidMessage, u.Type)
	case !utf8.Valid(u.Data):
		return nil, fmt.Errorf("%w: block of type %q is not valid UTF-8", ErrInvalidMessage, u.Type)
	}
	var head struct {
		Type *string `json:"type"`
	}
	if json.Unmarshal(u.Data, &head) != nil || head.Type == nil || *head.Type != u.Type {
		return nil, fmt.Errorf("%w: unknown block of type %q: data is not a JSON object of that type", ErrInvalidMessage, u.Type)
	}
	return u.Data, nil
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

// MarshalJSON writes Data, the block as it was read.
func (u Unknown) MarshalJSON() ([]byte, error) { return marshalBlock(u) }

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
		if reflect.ValueOf(b).Kind() == reflect.Pointer {
			return nil, fmt.Errorf("%w: block %d is a %T; blocks are values", ErrInvalidMessage, i, b)
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
	msg, err := unmarshalMessage(data, false)
	if err != nil {
		return err
	}
	*m = msg
	return nil
}

// UnmarshalMessageLenient decodes a message that a newer version may have
// written, as a reader of a format that only grows must: fields it does not
// know, on the message or on a block, are ignored, and a block of a type it
// does not know is kept as an [Unknown] block. What is malformed rather than
// newer is still an error ([ErrInvalidMessage]): invalid JSON, an unknown role,
// a block without a "type", a known block without its required fields.
//
// Use it for messages read from another process that may run a newer
// version, such as the agent API's events; keep [Message.UnmarshalJSON] for
// what this process wrote and will send to a model.
func UnmarshalMessageLenient(data []byte) (Message, error) {
	return unmarshalMessage(data, true)
}

func unmarshalMessage(data []byte, lenient bool) (Message, error) {
	var w struct {
		Role    Role              `json:"role"`
		Content []json.RawMessage `json:"content"`
	}
	if err := decodeJSON(data, &w, lenient); err != nil {
		return Message{}, err
	}
	if !w.Role.valid() {
		return Message{}, fmt.Errorf("%w: unknown role %q", ErrInvalidMessage, w.Role)
	}
	var content []Block
	for _, raw := range w.Content {
		b, err := unmarshalBlock(raw, lenient)
		if err != nil {
			return Message{}, err
		}
		content = append(content, b)
	}
	return Message{Role: w.Role, Content: content}, nil
}

// UnmarshalBlock decodes one block written by the block's MarshalJSON. It
// returns the block as a value ([Text], [ToolUse], [ToolResult] or [Opaque]),
// and [ErrInvalidMessage] for an unknown or missing "type".
func UnmarshalBlock(data []byte) (Block, error) { return unmarshalBlock(data, false) }

// unmarshalBlock decodes a block; lenient, it ignores unknown fields and
// returns a block of an unknown type as [Unknown].
func unmarshalBlock(data []byte, lenient bool) (Block, error) {
	// A block this package wrote starts with its type, so the type is read
	// from there instead of decoding the block twice. The shortcut holds
	// only if the block then decodes as that type: with a second "type"
	// member, or any error, the block takes the general path, which gives
	// the error, or reads the type as encoding/json does (the last one).
	if typ, ok := leadingType(data); ok {
		if b, decoded, err := decodeBlock(typ, data, lenient); err == nil && decoded == typ {
			return b, nil
		}
	}
	var head struct {
		Type string `json:"type"`
	}
	if err := jsonv2.Unmarshal(data, &head, lenientDecoding); err != nil {
		return nil, fmt.Errorf("%w: block: %w", ErrInvalidMessage, err)
	}
	if knownType(head.Type) {
		b, _, err := decodeBlock(head.Type, data, lenient)
		return b, err
	}
	if !lenient {
		return nil, fmt.Errorf("%w: unknown block type %q", ErrInvalidMessage, head.Type)
	}
	u := Unknown{Type: head.Type, Data: slices.Clone(data)}
	if _, err := u.wire(); err != nil { // a missing type, or not UTF-8
		return nil, err
	}
	return u, nil
}

// leadingType returns the type of a block that starts with {"type":"<t>", as
// every block MarshalJSON writes does, if t is a known type.
func leadingType(data []byte) (string, bool) {
	rest, ok := bytes.CutPrefix(data, []byte(`{"type":"`))
	if !ok {
		return "", false
	}
	end := bytes.IndexByte(rest, '"')
	if end < 0 {
		return "", false
	}
	for _, typ := range blockTypes {
		if string(rest[:end]) == typ { // compared without a copy
			return typ, true
		}
	}
	return "", false
}

// decodeBlock decodes a block of the known type typ. It also returns the
// "type" the decoding read, which differs from typ if the block has another.
func decodeBlock(typ string, data []byte, lenient bool) (Block, string, error) {
	switch typ {
	case typeText:
		var w textWire
		if err := decodeJSON(data, &w, lenient); err != nil {
			return nil, "", err
		}
		return Text{Text: w.Text}, w.Type, nil
	case typeToolUse:
		var w toolUseWire
		if err := decodeJSON(data, &w, lenient); err != nil {
			return nil, "", err
		}
		b, err := decodeToolUse(w)
		return b, w.Type, err
	case typeToolResult:
		var w toolResultWire
		if err := decodeJSON(data, &w, lenient); err != nil {
			return nil, "", err
		}
		if w.ToolUseID == "" {
			return nil, "", fmt.Errorf("%w: tool result needs a tool use ID", ErrInvalidMessage)
		}
		return ToolResult{ToolUseID: w.ToolUseID, Content: w.Content, IsError: w.IsError, Loads: w.Loads}, w.Type, nil
	case typeOpaque:
		var w opaqueWire
		if err := decodeJSON(data, &w, lenient); err != nil {
			return nil, "", err
		}
		if err := checkOpaque(w.Provider, []byte(w.Data)); err != nil {
			return nil, "", err
		}
		return Opaque{Provider: w.Provider, Data: json.RawMessage(w.Data)}, w.Type, nil
	}
	return nil, "", fmt.Errorf("%w: unknown block type %q", ErrInvalidMessage, typ)
}

// decodeToolUse puts Input in the form Marshal writes (compact, HTML
// characters escaped), so that decode, encode and decode again is stable.
func decodeToolUse(w toolUseWire) (Block, error) {
	if w.ID == "" || w.Name == "" {
		return nil, fmt.Errorf("%w: tool use needs an ID and a name", ErrInvalidMessage)
	}
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

// Messages are decoded with encoding/json/v2's Unmarshal, which takes the
// strictness as an option and rejects trailing data by itself, instead of a
// json.Decoder per block. Its options are encoding/json's own semantics
// (names matched regardless of case, duplicate names allowed, invalid UTF-8
// replaced by U+FFFD), so that what decodes, and to what, is unchanged.
var (
	lenientDecoding = json.DefaultOptionsV1()
	strictDecoding  = jsonv2.JoinOptions(lenientDecoding, jsonv2.RejectUnknownMembers(true))
)

// decodeJSON decodes exactly one JSON value into v and rejects trailing data
// and, unless lenient, unknown fields.
func decodeJSON(data []byte, v any, lenient bool) error {
	opts := strictDecoding
	if lenient {
		opts = lenientDecoding
	}
	if err := jsonv2.Unmarshal(data, v, opts); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidMessage, err)
	}
	return nil
}
