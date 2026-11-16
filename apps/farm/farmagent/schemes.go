package farmagent

import (
	"context"

	"github.com/bsamanvith/agentkit/apps/farm/schemes"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// GetSchemesTool is the crop insurance tool.
const GetSchemesTool = "get_schemes"

// SchemesSystem is added to the system prompt with get_schemes.
const SchemesSystem = `
- get_schemes answers "is my crop insured", "what is the last date to enrol", "what do I do if the crop is damaged" for a plot's current cycle: each crop insurance scheme of the plot's region, whether it covers the crop, the last date and the days left (or that it has passed, or that this season's dates are not yet announced), and how soon a loss must be reported. Answer only from it, never from memory: give its dates exactly, say they are from the notice read on its source's read date, and tell the farmer to confirm at the bank, the Rythu Seva Kendra or the insurer.`

// Schemes answers what crop insurance applies to a plot's current cycle;
// *addon.Schemes is one.
type Schemes interface {
	Farm(ctx context.Context, farmID string) (schemes.Answer, error)
}

// WithSchemes gives the assistant get_schemes over s.
func WithSchemes(s Schemes) Option { return func(x *setup) { x.schemes = s } }

type schemesIn struct {
	Farm string `json:"farm" jsonschema:"the farm's id, from list_farms"`
}

// SchemeTools returns get_schemes over s. It only reads.
func SchemeTools(s Schemes) ([]tool.Tool, error) {
	get, err := tool.Func(GetSchemesTool, "Get the crop insurance schemes for a farm's current cycle (its saved plan): for each, whether it covers the crop, the last date to enrol and the days left (or passed, or last year's dates not yet announced for this season), the hours to report a loss, and its source with the day it was read. Empty items: no plan, or no scheme known for the region.",
		func(ctx context.Context, in schemesIn) (schemes.Answer, error) {
			return s.Farm(ctx, in.Farm)
		}, read)
	if err != nil {
		return nil, err
	}
	return []tool.Tool{get}, nil
}
