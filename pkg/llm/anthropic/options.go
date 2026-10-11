package anthropic

import (
	"fmt"
	"slices"

	"github.com/bsamanvith/agentkit/pkg/llm"
)

// Options are the Claude-specific settings of a request. Put them in
// llm.Request.Provider (as Options or *Options) for one request, or give them
// to [WithOptions] as the model's defaults. Every zero field means "use the
// default": a request's field, if set, wins over the model's, which wins over
// the built-in default documented on the field. Betas are added together.
type Options struct {
	// Effort sets how much the model thinks and spends; the built-in default is
	// [EffortMedium] (Claude Opus 5.5's own default), always sent explicitly.
	Effort Effort
	// ThinkingDisplay sets what thinking blocks show. Empty leaves it to the
	// API, which returns them with empty text on current models.
	ThinkingDisplay ThinkingDisplay
	// Cache lists the prompt-cache breakpoints. Nil means the default,
	// [CacheConversation] alone; set NoCache to send none.
	Cache []CachePoint
	// NoCache sends no prompt-cache breakpoint, whatever Cache says.
	NoCache bool
	// Fallback sets the server-side retry on another model when the requested
	// model refuses for policy reasons. Empty means the default,
	// [FallbackDefault].
	Fallback Fallback
	// Betas are extra anthropic-beta header values, for API features this
	// package does not set itself.
	Betas []string
}

// Effort is the output_config.effort of a request.
type Effort string

// The effort levels, from cheapest to most thorough.
const (
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
	EffortXHigh  Effort = "xhigh"
	EffortMax    Effort = "max"
)

// ThinkingDisplay is the display setting of adaptive thinking.
type ThinkingDisplay string

// The thinking displays. ThinkingUpdates needs the
// thinking-display-updates-2026-08-18 beta in [Options.Betas].
const (
	// ThinkingSummarized returns a readable summary of the reasoning.
	ThinkingSummarized ThinkingDisplay = "summarized"
	// ThinkingOmitted returns thinking blocks with empty text (the API default).
	ThinkingOmitted ThinkingDisplay = "omitted"
	// ThinkingUpdates returns short progress notes between tool calls.
	ThinkingUpdates ThinkingDisplay = "updates"
)

// CachePoint is where a prompt-cache breakpoint goes. Each one caches
// everything before it (tools, then system, then messages).
type CachePoint string

// The cache breakpoints.
const (
	// CacheTools marks the last tool definition.
	CacheTools CachePoint = "tools"
	// CacheSystem marks the system prompt (it covers the tools too).
	CacheSystem CachePoint = "system"
	// CacheConversation is the API's automatic caching: it marks the last
	// cacheable block of the request, so each turn of a conversation reads
	// the previous turn's cache.
	CacheConversation CachePoint = "conversation"
)

// Fallback is the server-side refusal fallback setting.
type Fallback string

// The fallback settings.
const (
	// FallbackDefault lets the API retry a policy refusal on the requested
	// model's default fallback models, in the same request. The reply then
	// contains an opaque "fallback" block at the switch point. Not every model
	// accepts it (Claude Haiku 5.5 does not); turn it off for those.
	FallbackDefault Fallback = "default"
	// FallbackOff sends no fallback: a refusal ends the reply.
	FallbackOff Fallback = "off"
)

// fallbackBeta is the beta header that the "default" fallbacks form needs.
const fallbackBeta = "server-side-fallback-2026-07-01"

// merge returns o with every zero field taken from base.
func (o Options) merge(base Options) Options {
	if o.Effort == "" {
		o.Effort = base.Effort
	}
	if o.ThinkingDisplay == "" {
		o.ThinkingDisplay = base.ThinkingDisplay
	}
	if o.Cache == nil {
		o.Cache = base.Cache
	}
	o.NoCache = o.NoCache || base.NoCache
	if o.Fallback == "" {
		o.Fallback = base.Fallback
	}
	o.Betas = append(slices.Clip(base.Betas), o.Betas...)
	return o
}

// builtin are the defaults under every model's.
var builtin = Options{
	Effort:   EffortMedium,
	Cache:    []CachePoint{CacheConversation},
	Fallback: FallbackDefault,
}

func (o Options) validate() error {
	switch o.Effort {
	case EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax:
	default:
		return fmt.Errorf("%w: unknown effort %q", llm.ErrInvalidRequest, o.Effort)
	}
	switch o.ThinkingDisplay {
	case "", ThinkingSummarized, ThinkingOmitted, ThinkingUpdates:
	default:
		return fmt.Errorf("%w: unknown thinking display %q", llm.ErrInvalidRequest, o.ThinkingDisplay)
	}
	for _, c := range o.Cache {
		switch c {
		case CacheTools, CacheSystem, CacheConversation:
		default:
			return fmt.Errorf("%w: unknown cache point %q", llm.ErrInvalidRequest, c)
		}
	}
	switch o.Fallback {
	case FallbackDefault, FallbackOff:
	default:
		return fmt.Errorf("%w: unknown fallback %q", llm.ErrInvalidRequest, o.Fallback)
	}
	return nil
}

// requestOptions returns the options of req: its own Options, if any, over
// the model's defaults.
func (m *Model) requestOptions(req llm.Request) (Options, error) {
	o := m.defaults
	switch p := req.Provider.(type) {
	case Options:
		o = p.merge(o)
	case *Options:
		if p != nil {
			o = p.merge(o)
		}
	}
	return o, o.validate()
}
