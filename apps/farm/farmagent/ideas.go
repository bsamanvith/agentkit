package farmagent

import (
	"context"

	"github.com/bsamanvith/agentkit/apps/farm/ideas"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// CheckIdeasTool is the idea check's tool.
const CheckIdeasTool = "check_ideas"

// IdeasSystem is added to the system prompt with check_ideas.
const IdeasSystem = `
- check_ideas answers "what could I try on this plot", "is growing X here a good idea", "can I grow X in a polyhouse or under a shade net", "what is realistic" and "where would I sell it". It checks crops against the plot and gives each a verdict (realistic, stretch, unrealistic) with the reason, the growing method that helps (with the horticulture mission's cost norm and subsidy share), a price where the app has one, and the ways to sell. With no search it returns a spread: usual crops, new ones, ones that need a method, and popular ideas that do not work here. Give the verdict and its reason plainly, including when it is unrealistic: do not soften a no. Say that a method's effect is a starter value, that its cost is the scheme's norm and not a quotation, and that the app does not estimate yield or profit yet: never make those numbers up. A method idea is a stretch, never "a strong option": a higher score under a method says the crop would grow better, not that the structure pays for itself. Quote the cost from the tool's cost object (total, subsidy, farmer, on sqm square metres) and nothing else. Name only the ways to sell the tool returns: do not add buyers or promise a premium price.`

// WithIdeas gives the assistant check_ideas over s.
func WithIdeas(s *ideas.Service) Option { return func(x *setup) { x.ideas = s } }

type ideasIn struct {
	Farm   string `json:"farm" jsonschema:"the farm's id, from list_farms"`
	Search string `json:"search,omitempty" jsonschema:"a plant's name to check (any language, or its botanical name); leave out for a spread of ideas"`
	Limit  int    `json:"limit,omitempty" jsonschema:"how many ideas a band (default 6): usual, new, with a method, unrealistic"`
}

// IdeaTools returns check_ideas over s. It only reads.
func IdeaTools(s *ideas.Service) ([]tool.Tool, error) {
	check, err := tool.Func(CheckIdeasTool, "Check farming ideas against a farm's plot: for each crop a verdict (realistic, stretch, unrealistic) and why, how it scores in the open field and under the growing method that helps most (mulch with drip, raised beds, low tunnel, shade net, polyhouse, hydroponics) with that method's cost norm and subsidy, a price (mandi estimate, minimum support price) where known, and the ways to sell it. Bands: common, new, method, unrealistic. Give search to check one plant by name.",
		func(ctx context.Context, in ideasIn) (ideas.Report, error) {
			return s.For(ctx, in.Farm, ideas.Ask{Search: in.Search, Limit: in.Limit})
		}, read)
	if err != nil {
		return nil, err
	}
	return []tool.Tool{check}, nil
}
