package anthropic

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"unicode/utf8"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
	"github.com/anthropics/anthropic-sdk-go/shared/constant"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// provider is the llm.Opaque provider name of this adapter's blocks.
const provider = "anthropic"

// memoryToolName is the name of Claude's memory tool, which
// [Options.NativeMemory] sends as its native type.
const memoryToolName = "memory"

// build returns the SDK parameters of req and the request body to send. The
// body is the parameters marshaled by the SDK, with each Opaque block's data
// spliced in byte for byte.
func (m *Model) build(req llm.Request) (anthropic.BetaMessageNewParams, []byte, error) {
	var p anthropic.BetaMessageNewParams
	o, err := m.requestOptions(req)
	if err != nil {
		return p, nil, err
	}
	maxTokens := req.MaxTokens
	if maxTokens < 0 {
		return p, nil, fmt.Errorf("%w: max tokens %d is negative", llm.ErrInvalidRequest, maxTokens)
	}
	if maxTokens == 0 {
		maxTokens = m.maxTokens
	}
	cache := map[CachePoint]bool{}
	if !o.NoCache {
		for _, c := range o.Cache {
			cache[c] = true
		}
	}

	p.Model = m.model
	p.MaxTokens = int64(maxTokens)
	if req.System != "" {
		sys := anthropic.BetaTextBlockParam{Text: req.System}
		if cache[CacheSystem] {
			sys.CacheControl = anthropic.NewBetaCacheControlEphemeralParam()
		}
		p.System = []anthropic.BetaTextBlockParam{sys}
	}
	deferred, err := deferredTools(req)
	if err != nil {
		return p, nil, err
	}
	// The tools breakpoint goes on the last tool sent in full: the API
	// refuses cache_control on a deferred one.
	lastFull := -1
	for i, d := range req.Tools {
		if !deferred[d.Name] {
			lastFull = i
		}
	}
	for i, d := range req.Tools {
		last := cache[CacheTools] && i == lastFull
		var deferLoading param.Opt[bool]
		if deferred[d.Name] {
			deferLoading = param.NewOpt(true)
		}
		if o.NativeMemory && d.Name == memoryToolName {
			mt := anthropic.BetaMemoryTool20250818Param{DeferLoading: deferLoading}
			if last {
				mt.CacheControl = anthropic.NewBetaCacheControlEphemeralParam()
			}
			p.Tools = append(p.Tools, anthropic.BetaToolUnionParam{OfMemoryTool20250818: &mt})
			continue
		}
		t := anthropic.BetaToolParam{Name: d.Name, InputSchema: inputSchema(d.InputSchema), DeferLoading: deferLoading}
		if d.Description != "" {
			t.Description = param.NewOpt(d.Description)
		}
		if last {
			t.CacheControl = anthropic.NewBetaCacheControlEphemeralParam()
		}
		p.Tools = append(p.Tools, anthropic.BetaToolUnionParam{OfTool: &t})
	}
	if len(req.Tools) > 0 {
		// Forced tool choice ("any", "tool") is a 400 on current models.
		p.ToolChoice = anthropic.BetaToolChoiceUnionParam{OfAuto: &anthropic.BetaToolChoiceAutoParam{}}
	}
	s := splicer{nonce: rand.Text(), deferred: deferred}
	for i, msg := range req.Messages {
		mp, err := s.message(msg)
		if err != nil {
			return p, nil, fmt.Errorf("message %d: %w", i, err)
		}
		if len(mp.Content) > 0 {
			p.Messages = append(p.Messages, mp)
		}
	}
	p.Thinking = anthropic.BetaThinkingConfigParamUnion{OfAdaptive: &anthropic.BetaThinkingConfigAdaptiveParam{
		Display: anthropic.BetaThinkingConfigAdaptiveDisplay(o.ThinkingDisplay),
	}}
	p.OutputConfig = anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffort(o.Effort)}
	if cache[CacheConversation] {
		p.CacheControl = anthropic.NewBetaCacheControlEphemeralParam()
	}
	var betas []string
	if o.Fallback == FallbackDefault {
		p.Fallbacks = anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()}
		betas = append(betas, fallbackBeta)
	}
	for _, b := range append(betas, o.Betas...) {
		if !slices.Contains(p.Betas, b) {
			p.Betas = append(p.Betas, b)
		}
	}

	body, err := json.Marshal(p)
	if err != nil {
		return p, nil, fmt.Errorf("%w: %w", llm.ErrInvalidRequest, err)
	}
	body, err = s.splice(body)
	if err != nil {
		return p, nil, err
	}
	return p, body, nil
}

// deferredTools returns the set of req.Deferred, which must name tools of
// the request and leave at least one tool sent in full (the API refuses a
// request whose tools are all deferred).
func deferredTools(req llm.Request) (map[string]bool, error) {
	if len(req.Deferred) == 0 {
		return nil, nil
	}
	set := make(map[string]bool, len(req.Deferred))
	for _, name := range req.Deferred {
		if !slices.ContainsFunc(req.Tools, func(d tool.Definition) bool { return d.Name == name }) {
			return nil, fmt.Errorf("%w: deferred tool %q is not in the request's tools", llm.ErrInvalidRequest, name)
		}
		set[name] = true
	}
	if len(set) == len(req.Tools) {
		return nil, fmt.Errorf("%w: every tool is deferred; at least one must be sent in full", llm.ErrInvalidRequest)
	}
	return set, nil
}

// inputSchema sends a tool's JSON Schema as it is.
func inputSchema(schema json.RawMessage) anthropic.BetaToolInputSchemaParam {
	if len(schema) == 0 {
		schema = json.RawMessage(`{"type":"object"}`)
	}
	return param.Override[anthropic.BetaToolInputSchemaParam](schema)
}

// splicer turns messages into SDK parameters. Each Opaque block becomes a
// placeholder string, replaced by the block's data in the marshaled body: the
// SDK, like encoding/json, would compact and escape it otherwise.
type splicer struct {
	nonce string   // makes placeholders impossible to guess from content
	data  [][]byte // Opaque data, by placeholder number
	// deferred are the request's deferred tools, the only ones a result
	// can load: a reference to any other tool is a 400.
	deferred map[string]bool
}

func (s *splicer) placeholder(i int) string {
	return strconv.Quote("agentkit-opaque-" + s.nonce + "-" + strconv.Itoa(i))
}

func (s *splicer) splice(body []byte) ([]byte, error) {
	for i, data := range s.data {
		ph := []byte(s.placeholder(i))
		if n := bytes.Count(body, ph); n != 1 {
			return nil, fmt.Errorf("%w: opaque placeholder found %d times in the request body", llm.ErrInvalidRequest, n)
		}
		body = bytes.Replace(body, ph, data, 1)
	}
	return body, nil
}

func (s *splicer) message(msg llm.Message) (anthropic.BetaMessageParam, error) {
	var mp anthropic.BetaMessageParam
	switch msg.Role {
	case llm.RoleUser:
		mp.Role = anthropic.BetaMessageParamRoleUser
	case llm.RoleAssistant:
		mp.Role = anthropic.BetaMessageParamRoleAssistant
	default:
		return mp, fmt.Errorf("%w: unknown role %q", llm.ErrInvalidRequest, msg.Role)
	}
	// After a server-side fallback in the middle of a reply, the API wants the
	// declined attempt's thinking left out: the blocks before the last
	// fallback block.
	cut := -1
	for i, b := range msg.Content {
		if o, ok := b.(llm.Opaque); ok && o.Provider == provider && opaqueType(o.Data) == "fallback" {
			cut = i
		}
	}
	for i, b := range msg.Content {
		switch b := b.(type) {
		case llm.Text:
			mp.Content = append(mp.Content, anthropic.BetaContentBlockParamUnion{
				OfText: &anthropic.BetaTextBlockParam{Text: b.Text},
			})
		case llm.ToolUse:
			input := b.Input
			if len(input) == 0 {
				input = json.RawMessage(`{}`)
			}
			if !json.Valid(input) {
				return mp, fmt.Errorf("%w: tool use %s has invalid JSON input", llm.ErrInvalidRequest, b.ID)
			}
			mp.Content = append(mp.Content, anthropic.BetaContentBlockParamUnion{
				OfToolUse: &anthropic.BetaToolUseBlockParam{ID: b.ID, Name: b.Name, Input: input},
			})
		case llm.ToolResult:
			r := anthropic.BetaToolResultBlockParam{ToolUseID: b.ToolUseID}
			if b.Content != "" {
				r.Content = []anthropic.BetaToolResultBlockParamContentUnion{
					{OfText: &anthropic.BetaTextBlockParam{Text: b.Content}},
				}
			}
			// Tools the result loads: the API expands each reference into
			// the deferred tool's definition (tool search, done by the
			// harness rather than by the API's own search tool).
			for _, name := range b.Loads {
				if !s.deferred[name] {
					continue
				}
				r.Content = append(r.Content, anthropic.BetaToolResultBlockParamContentUnion{
					OfToolReference: &anthropic.BetaToolReferenceBlockParam{ToolName: name},
				})
			}
			if b.IsError {
				r.IsError = param.NewOpt(true)
			}
			mp.Content = append(mp.Content, anthropic.BetaContentBlockParamUnion{OfToolResult: &r})
		case llm.Opaque:
			if b.Provider != provider {
				continue // another provider's block means nothing to Claude
			}
			typ := opaqueType(b.Data)
			if typ == "" || !utf8.Valid(b.Data) {
				return mp, fmt.Errorf("%w: opaque block is not a JSON object with a type, in UTF-8", llm.ErrInvalidRequest)
			}
			if i < cut && (typ == "thinking" || typ == "redacted_thinking") {
				continue
			}
			s.data = append(s.data, b.Data)
			ph := json.RawMessage(s.placeholder(len(s.data) - 1))
			mp.Content = append(mp.Content, param.Override[anthropic.BetaContentBlockParamUnion](ph))
		default:
			return mp, fmt.Errorf("%w: unsupported block %T", llm.ErrInvalidRequest, b)
		}
	}
	return mp, nil
}

// opaqueType returns the "type" of a block's JSON, or "" if data is not a
// JSON object with a string type.
func opaqueType(data json.RawMessage) string {
	var head struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(data, &head) != nil {
		return ""
	}
	return head.Type
}
