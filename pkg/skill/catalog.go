package skill

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/bsamanvith/agentkit/pkg/tool"
)

// Names of the tools [Catalog.Toolset] gives.
const (
	LoadTool = "skill_load"
	ReadTool = "skill_read"
)

// Catalog is a fixed set of skills with unique names: what one agent may
// use. It is safe for concurrent use.
type Catalog struct {
	skills []Skill // sorted by name
}

// NewCatalog builds a catalog of skills, for example those of several
// [Load] calls. Two skills with one name fail with an [*Error] for
// [RuleDuplicate].
func NewCatalog(skills ...Skill) (*Catalog, error) {
	sorted := make([]Skill, len(skills))
	for i, s := range skills {
		sorted[i] = s.clone()
	}
	slices.SortStableFunc(sorted, func(a, b Skill) int { return strings.Compare(a.Name, b.Name) })
	if errs := duplicates(sorted); len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return &Catalog{skills: sorted}, nil
}

// Skills returns copies of the catalog's skills, sorted by name.
func (c *Catalog) Skills() []Skill {
	out := make([]Skill, len(c.skills))
	for i, s := range c.skills {
		out[i] = s.clone()
	}
	return out
}

// Names returns the skills' names, sorted.
func (c *Catalog) Names() []string {
	out := make([]string, len(c.skills))
	for i, s := range c.skills {
		out[i] = s.Name
	}
	return out
}

// Get returns a copy of the skill named name.
func (c *Catalog) Get(name string) (Skill, error) {
	i, ok := slices.BinarySearchFunc(c.skills, name, func(s Skill, n string) int { return strings.Compare(s.Name, n) })
	if !ok {
		return Skill{}, fmt.Errorf("%w: no skill %q; the skills are: %s", ErrNotFound, name, strings.Join(c.Names(), ", "))
	}
	return c.skills[i].clone(), nil
}

// SystemText is the catalog for the system prompt: what skills are, how to
// load one, and each skill's name and description, one per line, sorted by
// name. It is the same bytes for the same skills whatever order they were
// given in, so a cached prompt stays valid. An empty catalog gives "".
//
// Only names and descriptions go in: the instructions and files are read
// with the tools, when a task needs them.
func (c *Catalog) SystemText() string {
	if len(c.skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<skills>\n")
	b.WriteString("Skills hold instructions for particular kinds of tasks. When a task matches a skill's description, call ")
	b.WriteString(LoadTool)
	b.WriteString(" with its name before you start, and follow what it says; call ")
	b.WriteString(ReadTool)
	b.WriteString(" for a file it refers to. Skill scripts cannot be run here.\n\n")
	for _, s := range c.skills {
		b.WriteString("- ")
		b.WriteString(s.Name)
		b.WriteString(": ")
		b.WriteString(strings.Join(strings.Fields(s.Description), " "))
		b.WriteByte('\n')
	}
	b.WriteString("</skills>")
	return b.String()
}

type (
	loadIn struct {
		Name string `json:"name" jsonschema:"the skill's name, from the list in the system prompt"`
	}
	loadOut struct {
		Name string `json:"name"`
		// Instructions are SKILL.md after its front matter.
		Instructions string `json:"instructions"`
		// Files are the skill's other files, for skill_read.
		Files []File `json:"files"`
	}
	readIn struct {
		Name string `json:"name" jsonschema:"the skill's name"`
		Path string `json:"path" jsonschema:"a file of the skill, relative to its folder, as skill_load lists it (e.g. references/api.md)"`
	}
	readOut struct {
		Name    string `json:"name"`
		Path    string `json:"path"`
		Content string `json:"content"`
	}
)

// Toolset returns the tools that read the catalog: skill_load (a skill's
// instructions and file index) and skill_read (one file of a skill, text
// only, confined to its folder). Both are read-only and low risk.
func (c *Catalog) Toolset() ([]tool.Tool, error) {
	meta := func(title, present, past string) tool.FuncOption {
		return tool.WithMetadata(tool.Metadata{
			Kind: tool.KindRead, Risk: tool.RiskLow, Tags: []string{tool.TagIdempotent},
			Display: tool.Display{Title: title, Icon: "page", Present: present, Past: past},
		})
	}
	load, err := tool.Func(LoadTool,
		"Load a skill: its instructions (SKILL.md) and the list of its other files. "+
			"Call it when a task matches a skill's description in the system prompt, before starting the task.",
		func(_ context.Context, in loadIn) (loadOut, error) {
			s, err := c.Get(in.Name)
			if err != nil {
				return loadOut{}, err
			}
			files := s.Files
			if files == nil {
				files = []File{}
			}
			return loadOut{Name: s.Name, Instructions: s.Body, Files: files}, nil
		}, meta("Load skill", "Loading a skill", "Loaded a skill"))
	if err != nil {
		return nil, err
	}
	read, err := tool.Func(ReadTool,
		"Read one file of a skill (text only), by its path relative to the skill's folder as skill_load lists it.",
		func(_ context.Context, in readIn) (readOut, error) {
			s, err := c.Get(in.Name)
			if err != nil {
				return readOut{}, err
			}
			data, err := s.Read(in.Path)
			if err != nil {
				return readOut{}, err
			}
			p, _ := checkPath(in.Path)
			return readOut{Name: s.Name, Path: p, Content: string(data)}, nil
		}, meta("Read skill file", "Reading a skill file", "Read a skill file"))
	if err != nil {
		return nil, err
	}
	return []tool.Tool{load, read}, nil
}
