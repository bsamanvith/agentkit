package gemini

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/llm"
)

// The request body of streamGenerateContent, as far as this adapter sends
// it. Field order is the order on the wire.
type (
	generateRequest struct {
		SystemInstruction *content          `json:"systemInstruction,omitempty"`
		Contents          []content         `json:"contents"`
		Tools             []toolParam       `json:"tools,omitempty"`
		GenerationConfig  *generationConfig `json:"generationConfig,omitempty"`
	}
	content struct {
		Role  string `json:"role,omitempty"`
		Parts []part `json:"parts"`
	}
	// part is one part of a content. raw, if set, is a part sent as it
	// came (an opaque block's) and the other fields are unused.
	part struct {
		Text             *string           `json:"text,omitempty"`
		FunctionCall     *functionCall     `json:"functionCall,omitempty"`
		FunctionResponse *functionResponse `json:"functionResponse,omitempty"`
		ThoughtSignature string            `json:"thoughtSignature,omitempty"`

		raw json.RawMessage
	}
	functionCall struct {
		ID   string          `json:"id,omitempty"`
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	}
	functionResponse struct {
		ID       string          `json:"id,omitempty"`
		Name     string          `json:"name"`
		Response json.RawMessage `json:"response"`
	}
	toolParam struct {
		FunctionDeclarations []functionDecl `json:"functionDeclarations"`
	}
	functionDecl struct {
		Name                 string          `json:"name"`
		Description          string          `json:"description,omitempty"`
		ParametersJSONSchema json.RawMessage `json:"parametersJsonSchema,omitempty"`
	}
	generationConfig struct {
		MaxOutputTokens int             `json:"maxOutputTokens,omitempty"`
		ThinkingConfig  *thinkingConfig `json:"thinkingConfig,omitempty"`
	}
	thinkingConfig struct {
		ThinkingLevel   string `json:"thinkingLevel,omitempty"`
		IncludeThoughts bool   `json:"includeThoughts,omitempty"`
	}
)

// MarshalJSON writes a part sent as it came as it came.
func (p part) MarshalJSON() ([]byte, error) {
	if p.raw != nil {
		return p.raw, nil
	}
	type plain part // without this method
	return marshal(plain(p))
}

// encode writes the request body. HTML characters are not escaped, so text
// and tool arguments go out as written.
func (r *generateRequest) encode() ([]byte, error) { return marshal(r) }

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// build turns req into the request body. It fails with llm.ErrInvalidRequest
// for a request the API cannot take.
func (m *Model) build(req llm.Request) (*generateRequest, error) {
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
	if len(req.Deferred) > 0 {
		// The API cannot hold tools back: the model would get every tool,
		// and a search's results would be empty (harness.WithNativeToolSearch).
		return nil, fmt.Errorf("%w: deferred tools are not supported: use the harness's emulated tool search", llm.ErrInvalidRequest)
	}
	body := &generateRequest{}
	if req.System != "" {
		body.SystemInstruction = &content{Parts: []part{textPart(req.System)}}
	}
	names := map[string]string{} // tool use ID → tool name, for the results
	for i, msg := range req.Messages {
		var (
			c   content
			err error
		)
		switch msg.Role {
		case llm.RoleUser:
			c, err = userContent(msg, names)
		case llm.RoleAssistant:
			c, err = modelContent(msg, names)
		default:
			err = fmt.Errorf("unknown role %q", msg.Role)
		}
		if err != nil {
			return nil, fmt.Errorf("%w: message %d: %w", llm.ErrInvalidRequest, i, err)
		}
		if len(c.Parts) > 0 {
			body.Contents = append(body.Contents, c)
		}
	}
	var decls []functionDecl
	for _, def := range req.Tools {
		d := functionDecl{Name: def.Name, Description: def.Description}
		if len(def.InputSchema) > 0 {
			var members map[string]json.RawMessage
			if err := json.Unmarshal(def.InputSchema, &members); err != nil || members == nil {
				return nil, fmt.Errorf("%w: tool %q: input schema is not a JSON object", llm.ErrInvalidRequest, def.Name)
			}
			d.ParametersJSONSchema = def.InputSchema
		}
		decls = append(decls, d)
	}
	if len(decls) > 0 {
		body.Tools = []toolParam{{FunctionDeclarations: decls}}
	}
	gc := generationConfig{MaxOutputTokens: req.MaxTokens}
	if gc.MaxOutputTokens == 0 {
		gc.MaxOutputTokens = m.maxTokens
	}
	if opts.ThinkingLevel != "" || opts.IncludeThoughts {
		gc.ThinkingConfig = &thinkingConfig{ThinkingLevel: opts.ThinkingLevel, IncludeThoughts: opts.IncludeThoughts}
	}
	if gc != (generationConfig{}) {
		body.GenerationConfig = &gc
	}
	return body, nil
}

func textPart(s string) part { return part{Text: &s} }

// userContent maps a user message: tool results as functionResponse parts
// and text as text parts, in the order of the message.
func userContent(msg llm.Message, names map[string]string) (content, error) {
	c := content{Role: "user"}
	for j, b := range msg.Content {
		switch b := b.(type) {
		case llm.Text:
			if b.Text != "" {
				c.Parts = append(c.Parts, textPart(b.Text))
			}
		case llm.ToolResult:
			name, ok := names[b.ToolUseID]
			if !ok {
				return content{}, fmt.Errorf("block %d: tool result for %q, which no earlier tool use has", j, b.ToolUseID)
			}
			key := "output"
			if b.IsError {
				key = "error"
			}
			resp, err := marshal(map[string]string{key: b.Content})
			if err != nil {
				return content{}, err
			}
			c.Parts = append(c.Parts, part{FunctionResponse: &functionResponse{ID: b.ToolUseID, Name: name, Response: resp}})
		case llm.Opaque:
			if b.Provider == Provider {
				return content{}, fmt.Errorf("block %d: a Gemini part in a user message", j)
			}
		default:
			return content{}, fmt.Errorf("block %d: %T is not allowed in a user message", j, b)
		}
	}
	if len(c.Parts) == 0 {
		return content{}, fmt.Errorf("empty user message")
	}
	return c, nil
}

// modelContent maps an assistant message: text as text parts, tool uses as
// functionCall parts, and this adapter's opaque blocks as the parts or
// signatures they hold. It records the tool uses' names in names. A message
// left with nothing to send (only other providers' blocks) has no parts.
func modelContent(msg llm.Message, names map[string]string) (content, error) {
	c := content{Role: "model"}
	for j, b := range msg.Content {
		switch b := b.(type) {
		case llm.Text:
			if b.Text != "" {
				c.Parts = append(c.Parts, textPart(b.Text))
			}
		case llm.ToolUse:
			if b.ID == "" || b.Name == "" {
				return content{}, fmt.Errorf("block %d: tool use without an ID or a name", j)
			}
			args := b.Input
			if len(args) == 0 || string(args) == "null" {
				args = json.RawMessage(`{}`)
			} else if !json.Valid(args) {
				return content{}, fmt.Errorf("block %d: input of tool use %q is not valid JSON", j, b.ID)
			}
			names[b.ID] = b.Name
			c.Parts = append(c.Parts, part{FunctionCall: &functionCall{ID: b.ID, Name: b.Name, Args: args}})
		case llm.Opaque:
			if b.Provider != Provider {
				continue
			}
			sig, raw, err := opaquePart(b.Data)
			if err != nil {
				return content{}, fmt.Errorf("block %d: %w", j, err)
			}
			if raw != nil {
				c.Parts = append(c.Parts, part{raw: raw})
				continue
			}
			// A signature goes on the part before it, a text or a call.
			if n := len(c.Parts); n > 0 && c.Parts[n-1].raw == nil && c.Parts[n-1].FunctionResponse == nil && c.Parts[n-1].ThoughtSignature == "" {
				c.Parts[n-1].ThoughtSignature = sig
				continue
			}
			empty := ""
			c.Parts = append(c.Parts, part{Text: &empty, ThoughtSignature: sig})
		default:
			return content{}, fmt.Errorf("block %d: %T is not allowed in an assistant message", j, b)
		}
	}
	return c, nil
}

// opaquePart reads the data of an opaque block: the signature of the part
// before it ({"thoughtSignature":"..."} alone), or a part to send as it is,
// compacted.
func opaquePart(data json.RawMessage) (sig string, raw json.RawMessage, err error) {
	var members map[string]json.RawMessage
	if json.Unmarshal(data, &members) != nil || members == nil {
		return "", nil, fmt.Errorf("Gemini block is not a JSON object")
	}
	if s, ok := members["thoughtSignature"]; ok && len(members) == 1 {
		if json.Unmarshal(s, &sig) != nil || sig == "" {
			return "", nil, fmt.Errorf("Gemini block: thoughtSignature is not a string")
		}
		return sig, nil, nil
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, data); err != nil {
		return "", nil, fmt.Errorf("Gemini block: %w", err)
	}
	return "", buf.Bytes(), nil
}
