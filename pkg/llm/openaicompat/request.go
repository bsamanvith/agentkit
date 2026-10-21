package openaicompat

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/shared"

	"github.com/bsamanvith/agentkit/pkg/llm"
)

// errorPrefix starts the content of a failed tool result: the API has no
// error flag for tool messages.
const errorPrefix = "Error: "

// build turns req into the SDK's request parameters. It fails with
// llm.ErrInvalidRequest for a request the API cannot take.
func (m *Model) build(req llm.Request) (openai.ChatCompletionNewParams, error) {
	opts := requestOptions(req, m.defaults)
	if err := opts.validate(); err != nil {
		return openai.ChatCompletionNewParams{}, err
	}
	if len(req.Messages) == 0 {
		return openai.ChatCompletionNewParams{}, fmt.Errorf("%w: no messages", llm.ErrInvalidRequest)
	}
	if req.MaxTokens < 0 {
		return openai.ChatCompletionNewParams{}, fmt.Errorf("%w: max tokens %d is negative", llm.ErrInvalidRequest, req.MaxTokens)
	}
	var msgs []openai.ChatCompletionMessageParamUnion
	if req.System != "" {
		msgs = append(msgs, openai.SystemMessage(req.System))
	}
	for i, msg := range req.Messages {
		var (
			out []openai.ChatCompletionMessageParamUnion
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
			return openai.ChatCompletionNewParams{}, fmt.Errorf("%w: message %d: %w", llm.ErrInvalidRequest, i, err)
		}
		msgs = append(msgs, out...)
	}
	params := openai.ChatCompletionNewParams{
		Model:         m.model,
		Messages:      msgs,
		StreamOptions: openai.ChatCompletionStreamOptionsParam{IncludeUsage: param.NewOpt(true)},
	}
	for _, def := range req.Tools {
		fn := shared.FunctionDefinitionParam{Name: def.Name}
		if def.Description != "" {
			fn.Description = param.NewOpt(def.Description)
		}
		if len(def.InputSchema) > 0 {
			schema, err := decodeSchema(def.InputSchema)
			if err != nil {
				return openai.ChatCompletionNewParams{}, fmt.Errorf("%w: tool %q: input schema: %w", llm.ErrInvalidRequest, def.Name, err)
			}
			fn.Parameters = schema
		}
		params.Tools = append(params.Tools, openai.ChatCompletionFunctionTool(fn))
	}
	maxTokens := req.MaxTokens
	if maxTokens == 0 {
		maxTokens = m.maxTokens
	}
	if maxTokens > 0 {
		if opts.LegacyMaxTokens {
			params.MaxTokens = param.NewOpt(int64(maxTokens))
		} else {
			params.MaxCompletionTokens = param.NewOpt(int64(maxTokens))
		}
	}
	if opts.ReasoningEffort != "" {
		params.ReasoningEffort = shared.ReasoningEffort(opts.ReasoningEffort)
	}
	return params, nil
}

// decodeSchema decodes a JSON schema object. Its members stay raw JSON, so
// the schema is sent as written (numbers included), with only its top-level
// members reordered.
func decodeSchema(raw json.RawMessage) (map[string]any, error) {
	var members map[string]json.RawMessage
	if err := json.Unmarshal(raw, &members); err != nil {
		return nil, err
	}
	if members == nil {
		return nil, fmt.Errorf("not a JSON object")
	}
	schema := make(map[string]any, len(members))
	for k, v := range members {
		schema[k] = v
	}
	return schema, nil
}

// userMessages maps a user message: its tool results first, as "tool"
// messages, then its text, as one "user" message.
func userMessages(msg llm.Message) ([]openai.ChatCompletionMessageParamUnion, error) {
	var (
		out   []openai.ChatCompletionMessageParamUnion
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
			out = append(out, openai.ToolMessage(content, b.ToolUseID))
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
		out = append(out, openai.UserMessage(texts[0]))
	default:
		parts := make([]openai.ChatCompletionContentPartUnionParam, len(texts))
		for k, t := range texts {
			parts[k] = openai.TextContentPart(t)
		}
		out = append(out, openai.UserMessage(parts))
	}
	return out, nil
}

// assistantMessage maps an assistant message: text blocks as its content,
// tool uses as its tool calls, and this adapter's reasoning blocks as the
// fields they came in. A message left with nothing to send (only other
// providers' blocks) is left out.
func assistantMessage(msg llm.Message, dropReasoning bool) ([]openai.ChatCompletionMessageParamUnion, error) {
	var (
		a       openai.ChatCompletionAssistantMessageParam
		text    []byte
		hasText bool
		extra   map[string]any
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
			a.ToolCalls = append(a.ToolCalls, openai.ChatCompletionMessageToolCallUnionParam{
				OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
					ID:       b.ID,
					Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{Name: b.Name, Arguments: args},
				},
			})
		case llm.Opaque:
			if b.Provider != Provider || dropReasoning {
				continue
			}
			fields, err := reasoningFields(b.Data)
			if err != nil {
				return nil, fmt.Errorf("block %d: %w", j, err)
			}
			if extra == nil {
				extra = map[string]any{}
			}
			for _, f := range fields {
				if prev, ok := extra[f.name].(string); ok {
					extra[f.name] = prev + f.text
					continue
				}
				extra[f.name] = f.text
			}
		default:
			return nil, fmt.Errorf("block %d: %T is not allowed in an assistant message", j, b)
		}
	}
	if !hasText && len(a.ToolCalls) == 0 && len(extra) == 0 {
		return nil, nil
	}
	if hasText {
		a.Content.OfString = param.NewOpt(string(text))
	}
	if extra != nil {
		a.SetExtraFields(extra)
	}
	return []openai.ChatCompletionMessageParamUnion{{OfAssistant: &a}}, nil
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
