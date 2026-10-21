package openaicompat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/bsamanvith/agentkit/pkg/llm"
)

// errorPrefix starts the content of a failed tool result: the API has no
// error flag for tool messages.
const errorPrefix = "Error: "

// The request body of POST /chat/completions, as far as this adapter sends
// it. Field order is the order on the wire.
type (
	chatRequest struct {
		Messages            []message     `json:"messages"`
		Model               string        `json:"model"`
		MaxCompletionTokens int           `json:"max_completion_tokens,omitempty"`
		MaxTokens           int           `json:"max_tokens,omitempty"`
		ReasoningEffort     string        `json:"reasoning_effort,omitempty"`
		StreamOptions       streamOptions `json:"stream_options"`
		Tools               []toolParam   `json:"tools,omitempty"`
		Stream              bool          `json:"stream"`
	}
	streamOptions struct {
		IncludeUsage bool `json:"include_usage"`
	}
	// message is one message of any role. Content is a string, or text
	// parts for a user message of several texts; nil leaves it out (an
	// assistant message with only tool calls).
	message struct {
		Content          any        `json:"content,omitempty"`
		ToolCalls        []toolCall `json:"tool_calls,omitempty"`
		ToolCallID       string     `json:"tool_call_id,omitempty"`
		Role             string     `json:"role"`
		ReasoningContent *string    `json:"reasoning_content,omitempty"`
		Reasoning        *string    `json:"reasoning,omitempty"`
	}
	textPart struct {
		Text string `json:"text"`
		Type string `json:"type"`
	}
	toolCall struct {
		ID       string       `json:"id"`
		Function callFunction `json:"function"`
		Type     string       `json:"type"`
	}
	callFunction struct {
		Arguments string `json:"arguments"`
		Name      string `json:"name"`
	}
	toolParam struct {
		Function functionDef `json:"function"`
		Type     string      `json:"type"`
	}
	functionDef struct {
		Name        string                     `json:"name"`
		Description string                     `json:"description,omitempty"`
		Parameters  map[string]json.RawMessage `json:"parameters,omitempty"`
	}
)

// encode writes the request body. HTML characters are not escaped, so text
// and tool arguments go out as written.
func (r *chatRequest) encode() ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// build turns req into the request body. It fails with llm.ErrInvalidRequest
// for a request the API cannot take.
func (m *Model) build(req llm.Request) (*chatRequest, error) {
	opts := requestOptions(req, m.defaults)
	if err := opts.validate(); err != nil {
		return nil, err
	}
	if len(req.Messages) == 0 {
		return nil, fmt.Errorf("%w: no messages", llm.ErrInvalidRequest)
	}
	if req.MaxTokens < 0 {
		return nil, fmt.Errorf("%w: max tokens %d is negative", llm.ErrInvalidRequest, req.MaxTokens)
	}
	var msgs []message
	if req.System != "" {
		msgs = append(msgs, message{Role: "system", Content: req.System})
	}
	for i, msg := range req.Messages {
		var (
			out []message
			err error
		)
		switch msg.Role {
		case llm.RoleUser:
			out, err = userMessages(msg)
		case llm.RoleAssistant:
			out, err = assistantMessage(msg, opts.DropReasoning)
		default:
			err = fmt.Errorf("unknown role %q", msg.Role)
		}
		if err != nil {
			return nil, fmt.Errorf("%w: message %d: %w", llm.ErrInvalidRequest, i, err)
		}
		msgs = append(msgs, out...)
	}
	body := &chatRequest{
		Model:           m.model,
		Messages:        msgs,
		ReasoningEffort: opts.ReasoningEffort,
		StreamOptions:   streamOptions{IncludeUsage: true},
		Stream:          true,
	}
	for _, def := range req.Tools {
		fn := functionDef{Name: def.Name, Description: def.Description}
		if len(def.InputSchema) > 0 {
			schema, err := decodeSchema(def.InputSchema)
			if err != nil {
				return nil, fmt.Errorf("%w: tool %q: input schema: %w", llm.ErrInvalidRequest, def.Name, err)
			}
			fn.Parameters = schema
		}
		body.Tools = append(body.Tools, toolParam{Function: fn, Type: "function"})
	}
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = m.maxTokens
	}
	if opts.LegacyMaxTokens {
		body.MaxTokens = maxTokens
	} else {
		body.MaxCompletionTokens = maxTokens
	}
	return body, nil
}

// decodeSchema decodes a JSON schema object. Its members stay raw JSON, so
// the schema is sent as written (numbers included), with only its top-level
// members reordered.
func decodeSchema(raw json.RawMessage) (map[string]json.RawMessage, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return nil, err
	}
	if members == nil {
		return nil, fmt.Errorf("not a JSON object")
	}
	return members, nil
}

// userMessages maps a user message: its tool results first, as "tool"
// messages, then its text, as one "user" message.
func userMessages(msg llm.Message) ([]message, error) {
	var (
		out   []message
		texts []string
	)
	for j, b := range msg.Content {
		switch b := b.(type) {
		case llm.Text:
			texts = append(texts, b.Text)
		case llm.ToolResult:
			if b.ToolUseID == "" {
				return nil, fmt.Errorf("block %d: tool result without a tool use ID", j)
			}
			content := b.Content
			if b.IsError {
				content = errorPrefix + content
			}
			out = append(out, message{Role: "tool", Content: content, ToolCallID: b.ToolUseID})
		case llm.Opaque:
			if b.Provider == Provider {
				return nil, fmt.Errorf("block %d: a reasoning block in a user message", j)
			}
		default:
			return nil, fmt.Errorf("block %d: %T is not allowed in a user message", j, b)
		}
	}
	switch len(texts) {
	case 0:
		if len(out) == 0 {
			return nil, fmt.Errorf("empty user message")
		}
	case 1:
		out = append(out, message{Role: "user", Content: texts[0]})
	default:
		parts := make([]textPart, len(texts))
		for k, t := range texts {
			parts[k] = textPart{Text: t, Type: "text"}
		}
		out = append(out, message{Role: "user", Content: parts})
	}
	return out, nil
}

// assistantMessage maps an assistant message: text blocks as its content,
// tool uses as its tool calls, and this adapter's reasoning blocks as the
// fields they came in. A message left with nothing to send (only other
// providers' blocks) is left out.
func assistantMessage(msg llm.Message, dropReasoning bool) ([]message, error) {
	var (
		a         = message{Role: "assistant"}
		text      []byte
		hasText   bool
		reasoning = map[string]*string{}
	)
	for j, b := range msg.Content {
		switch b := b.(type) {
		case llm.Text:
			text = append(text, b.Text...)
			hasText = true
		case llm.ToolUse:
			if b.ID == "" || b.Name == "" {
				return nil, fmt.Errorf("block %d: tool use without an ID or a name", j)
			}
			args := string(b.Input)
			if len(b.Input) == 0 || args == "null" {
				args = "{}"
			} else if !json.Valid(b.Input) {
				return nil, fmt.Errorf("block %d: input of tool use %q is not valid JSON", j, b.ID)
			}
			a.ToolCalls = append(a.ToolCalls, toolCall{ID: b.ID, Function: callFunction{Name: b.Name, Arguments: args}, Type: "function"})
		case llm.Opaque:
			if b.Provider != Provider || dropReasoning {
				continue
			}
			fields, err := reasoningFields(b.Data)
			if err != nil {
				return nil, fmt.Errorf("block %d: %w", j, err)
			}
			for _, f := range fields {
				if prev := reasoning[f.name]; prev != nil {
					*prev += f.text
					continue
				}
				t := f.text
				reasoning[f.name] = &t
			}
		default:
			return nil, fmt.Errorf("block %d: %T is not allowed in an assistant message", j, b)
		}
	}
	if !hasText && len(a.ToolCalls) == 0 && len(reasoning) == 0 {
		return nil, nil
	}
	if hasText {
		a.Content = string(text)
	}
	a.ReasoningContent, a.Reasoning = reasoning["reasoning_content"], reasoning["reasoning"]
	return []message{a}, nil
}

type reasoningField struct{ name, text string }

// reasoningFields reads the data of a reasoning block: a JSON object of
// reasoning fields with string values, in the order they appear.
func reasoningFields(data json.RawMessage) ([]reasoningField, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("reasoning block is not a JSON object")
	}
	var fields []reasoningField
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("reasoning block: %w", err)
		}
		name, _ := tok.(string)
		var text string
		if err := dec.Decode(&text); err != nil {
			return nil, fmt.Errorf("reasoning block field %q: %w", name, err)
		}
		if !slices.Contains(reasoningNames, name) {
			return nil, fmt.Errorf("reasoning block field %q is not one of %q", name, reasoningNames)
		}
		fields = append(fields, reasoningField{name, text})
	}
	return fields, nil
}
