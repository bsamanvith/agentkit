// Package handbook is the farm assistant's knowledge base: short,
// hand-checked Markdown notes on Indian seasons, soils, crops, mixed
// farming, water, trees and borders, farm layout and ponds, fodder and
// green manure, and the agro-climatic regions, plus a crop library chapter
// made from the app's crop data (package agro): one section per crop with
// its names, season, temperatures, water, soils and spacing.
//
// It is a tree as well as a search (pkg/knowledge): documents have
// chapters and sections the assistant can outline and read one at a time.
// Search is by keyword, or hybrid (keyword and meaning) with an embedder.
package handbook

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/pkg/knowledge"
	"github.com/bsamanvith/agentkit/pkg/memory"
)

//go:embed docs/*.md
var files embed.FS

// Description says what the base holds, for the knowledge tools.
const Description = "the farm handbook: Indian seasons and sowing, soils, crops by group, a crop library (one section per crop: names, seasons, temperatures, water, soils, spacing), mixed farming and rotation, water, trees for borders and agroforestry, farm layout and farm ponds, fodder and green manure, India's agro-climatic regions and similar climates abroad, and its sources"

// LibraryDoc is the name of the generated crop library chapter.
const LibraryDoc = "crop-library.md"

// Option configures [New].
type Option func(*knowledge.Config)

// WithEmbedder searches by meaning as well as keywords (hybrid), with e.
func WithEmbedder(e memory.Embedder) Option {
	return func(c *knowledge.Config) { c.Embedder = e }
}

// New loads the handbook into a knowledge base. lib is the crop library
// whose chapter it makes; nil for the default library.
func New(ctx context.Context, lib *agro.Library, opts ...Option) (*knowledge.Base, error) {
	if lib == nil {
		var err error
		if lib, err = agro.Default(); err != nil {
			return nil, err
		}
	}
	sub, err := fs.Sub(files, "docs")
	if err != nil {
		return nil, err
	}
	loaded, err := knowledge.Load(sub)
	if err != nil {
		return nil, err
	}
	loaded = append(loaded, knowledge.Document{Source: LibraryDoc, Title: "Crop library", Text: Library(lib)})
	slices.SortFunc(loaded, func(a, b knowledge.Document) int { return strings.Compare(a.Source, b.Source) })
	var c knowledge.Config
	for _, o := range opts {
		o(&c)
	}
	return knowledge.New(ctx, loaded, c)
}

var kindTitles = map[string]string{
	"cereal": "Cereals", "millet": "Millets", "pulse": "Pulses", "oilseed": "Oilseeds",
	"vegetable": "Vegetables", "fruit": "Fruit", "fibre": "Fibre crops", "cash": "Cash crops",
	"spice": "Spices", "tuber": "Tubers", "pseudocereal": "Pseudocereals", "tree": "Trees",
}

var languages = map[string]string{"hi": "Hindi", "te": "Telugu", "ta": "Tamil", "kn": "Kannada", "mr": "Marathi", "bn": "Bengali", "gu": "Gujarati", "ml": "Malayalam", "pa": "Punjabi", "or": "Odia"}

// Library writes the crop library chapter: crops by kind, a section each.
func Library(lib *agro.Library) string {
	var b strings.Builder
	b.WriteString("# Crop library\n\nEvery crop the app can plan, by kind: its local names, family, seasons, days to harvest, temperatures, water, soils and spacing, from the app's crop data (FAO ECOCROP ranges and Indian packages of practice, starter values to check with local experts). Crops marked as climate analogs are new to India, suggested because they grow where the climate is like ours.\n")
	byKind := map[string][]agro.Crop{}
	for _, c := range lib.Crops() {
		byKind[c.Kind] = append(byKind[c.Kind], c)
	}
	kinds := make([]string, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, k)
	}
	slices.Sort(kinds)
	textures := map[agro.Texture]string{}
	for _, t := range agro.Textures() {
		textures[t] = strings.ReplaceAll(string(t), "_", " ")
	}
	for _, k := range kinds {
		title := kindTitles[k]
		if title == "" {
			title = strings.ToUpper(k[:1]) + k[1:]
		}
		fmt.Fprintf(&b, "\n## %s\n", title)
		for _, c := range byKind[k] {
			fmt.Fprintf(&b, "\n### %s\n\n", c.Name)
			var names []string
			for _, code := range sortedKeys(c.Names) {
				lang := languages[code]
				if lang == "" {
					lang = code
				}
				names = append(names, lang+" "+c.Names[code])
			}
			if len(names) > 0 {
				fmt.Fprintf(&b, "Also called: %s. ", strings.Join(names, ", "))
			}
			life := "an annual"
			if c.Perennial {
				life = "a perennial"
			}
			fmt.Fprintf(&b, "%s, family %s. Id in the app: %s.\n\n", capital(life), c.Family, c.ID)
			if c.ClimateAnalog {
				fmt.Fprintf(&b, "A climate analog: grown in %s, new to most Indian farms.\n\n", strings.Join(c.GrownIn, ", "))
			} else if len(c.GrownIn) > 0 {
				fmt.Fprintf(&b, "Grown in %s.\n\n", strings.Join(c.GrownIn, ", "))
			}
			fmt.Fprintf(&b, "- Seasons: %s; %g to %g days to %s.\n", strings.Join(c.Seasons, ", "), c.Days[0], c.Days[1], firstOr(c.Perennial))
			fmt.Fprintf(&b, "- Temperature: best %g to %g °C (monthly mean), survives %g to %g °C.\n", c.TempOpt[0], c.TempOpt[1], c.TempAbs[0], c.TempAbs[1])
			fmt.Fprintf(&b, "- Water over the season: %g to %g mm from rain and irrigation.\n", c.WaterMM[0], c.WaterMM[1])
			var ts []string
			for _, t := range c.Textures {
				ts = append(ts, textures[t])
			}
			fmt.Fprintf(&b, "- Soils: %s; pH %g to %g.\n", strings.Join(ts, ", "), c.PH[0], c.PH[1])
			if len(c.Tolerates) > 0 {
				fmt.Fprintf(&b, "- Tolerates: %s.\n", strings.ReplaceAll(strings.Join(c.Tolerates, ", "), "_", " "))
			}
			fmt.Fprintf(&b, "- Spacing: %g cm between rows, %g cm between plants.", c.RowCM, c.PlantCM)
			if c.Tall {
				b.WriteString(" A tall crop: it shades shorter ones beside it.")
			}
			b.WriteString("\n")
			if len(c.Intercrops) > 0 {
				var ns []string
				for _, id := range c.Intercrops {
					if o, err := lib.Crop(id); err == nil {
						ns = append(ns, o.Name)
					}
				}
				fmt.Fprintf(&b, "- Grows well with: %s.\n", strings.Join(ns, ", "))
			}
		}
	}
	return b.String()
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}

func capital(s string) string { return strings.ToUpper(s[:1]) + s[1:] }

func firstOr(perennial bool) string {
	if perennial {
		return "the first harvest"
	}
	return "harvest"
}
