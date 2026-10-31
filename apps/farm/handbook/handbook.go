// Package handbook is the farm assistant's starter knowledge base: short,
// hand-checked Markdown notes on Indian seasons, soils, crops, mixed
// farming and water, organised as chapters and sections so the assistant
// can search them or walk their outline (pkg/knowledge).
package handbook

import (
	"context"
	"embed"
	"io/fs"

	"github.com/bsamanvith/agentkit/pkg/knowledge"
)

//go:embed docs/*.md
var files embed.FS

// Description says what the base holds, for the knowledge tools.
const Description = "the farm handbook: Indian seasons and sowing, soils, crops by group, mixed farming and rotation, water, and its sources"

// New loads the handbook into a knowledge base searched by keyword.
func New(ctx context.Context) (*knowledge.Base, error) {
	docs, err := fs.Sub(files, "docs")
	if err != nil {
		return nil, err
	}
	loaded, err := knowledge.Load(docs)
	if err != nil {
		return nil, err
	}
	return knowledge.New(ctx, loaded, knowledge.Config{})
}
