package openaicompat

import (
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/llm"
)

// Options are the settings of a request specific to this adapter. Put them in
// llm.Request.Provider (as Options or *Options) for one request, or give them
// to [WithOptions] as the model's defaults. A request's ReasoningEffort, if
// set, wins over the model's; the switches are on if either turns them on.
type Options struct {
	// ReasoningEffort is sent as reasoning_effort ("minimal", "low",
	// "medium", "high", ...), for reasoning models that take it. Empty sends
	// nothing and leaves it to the server.
	ReasoningEffort string
	// LegacyMaxTokens sends the output limit as max_tokens instead of
	// max_completion_tokens, for servers that only know the older field.
	// OpenAI's reasoning models reject max_tokens.
	LegacyMaxTokens bool
	// DropReasoning leaves this adapter's reasoning blocks out of requests,
	// for servers that reject a reasoning field on assistant messages.
	DropReasoning bool
}

// merge returns o with the unset fields taken from d.
func (o Options) merge(d Options) Options {
	if o.ReasoningEffort == "" {
		o.ReasoningEffort = d.ReasoningEffort
	}
	o.LegacyMaxTokens = o.LegacyMaxTokens || d.LegacyMaxTokens
	o.DropReasoning = o.DropReasoning || d.DropReasoning
	return o
}

// requestOptions returns the options of req merged over the model's defaults.
// Options of another adapter are ignored; a nil *Options counts as none.
func requestOptions(req llm.Request, defaults Options) Options {
	switch p := req.Provider.(type) {
	case Options:
		return p.merge(defaults)
	case *Options:
		if p == nil {
			return defaults
		}
		return p.merge(defaults)
	}
	return defaults
}

func (o Options) validate() error {
	for _, r := range o.ReasoningEffort {
		if r < 'a' || r > 'z' {
			return fmt.Errorf("%w: reasoning effort %q: want a lowercase word such as \"low\"", llm.ErrInvalidRequest, o.ReasoningEffort)
		}
	}
	return nil
}
