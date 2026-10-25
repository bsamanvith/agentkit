package gemini

import (
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/llm"
)

// Options are the settings of a request specific to this adapter. Put them in
// llm.Request.Provider (as Options or *Options) for one request, or give them
// to [WithOptions] as the model's defaults. A request's ThinkingLevel, if
// set, wins over the model's; IncludeThoughts is on if either turns it on.
type Options struct {
	// ThinkingLevel is sent as thinkingConfig.thinkingLevel ("minimal",
	// "low", "medium" or "high", as the model takes them). Empty sends
	// nothing and leaves it to the model.
	ThinkingLevel string
	// IncludeThoughts asks for thought summaries, which come back as opaque
	// blocks.
	IncludeThoughts bool
}

// merge returns o with the unset fields taken from d.
func (o Options) merge(d Options) Options {
	if o.ThinkingLevel == "" {
		o.ThinkingLevel = d.ThinkingLevel
	}
	o.IncludeThoughts = o.IncludeThoughts || d.IncludeThoughts
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
	for _, r := range o.ThinkingLevel {
		if r < 'a' || r > 'z' {
			return fmt.Errorf("%w: thinking level %q: want a lowercase word such as \"low\"", llm.ErrInvalidRequest, o.ThinkingLevel)
		}
	}
	return nil
}
