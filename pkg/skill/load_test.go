package skill_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/google/go-cmp/cmp"

	"github.com/bsamanvith/agentkit/pkg/skill"
)

// TestLoadValid: each skill loads with its name, description, metadata,
// tools, body and file index, whichever YAML form its front matter takes.
func TestLoadValid(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"skills/pdf/SKILL.md": md(`name: pdf
description: >
  Fill and read PDF forms.
  Use when a task names a PDF.
metadata:
  author: ops
  version: "1.2"
tools: [files_*, read_file]`, "# PDF\n\nRead references/forms.md first.\n"),
		"skills/pdf/references/forms.md": {Data: []byte("forms\n")},
		"skills/pdf/scripts/fill.py":     {Data: []byte("print(1)\n")},
		"skills/pdf/logo.png":            {Data: []byte{0x89, 'P', 'N', 'G', 0}},
		"skills/pdf/alias.md":            link("references/forms.md"),
		"skills/pdf/.git/HEAD":           {Data: []byte("ref")},
		"skills/notes/SKILL.md": md(`# a comment
name: notes
description: 'Take notes: it''s simple' # trailing comment
license: Apache-2.0
allowed-tools: Read Write
tools:
  - memory_view
  - "memory_*"`, "Notes body"),
		"skills/quoted/SKILL.md": md(`name: "quoted"
description: "Line one\nline two \u00e9"`, ""),
		"skills/plain/SKILL.md": md("name: plain\ndescription: a long\n  plain description\n\n  with a break", "body"),
		"skills/README.md":      {Data: []byte("not a skill")},
		"skills/.hidden/x":      {Data: []byte("ignored")},
	}
	got := load(t, fsys, "skills")
	want := []skill.Skill{
		{Name: "notes", Description: "Take notes: it's simple", Tools: []string{"memory_view", "memory_*"}, Body: "Notes body"},
		{
			Name: "pdf", Description: "Fill and read PDF forms. Use when a task names a PDF.\n",
			Metadata: map[string]string{"author": "ops", "version": "1.2"},
			Tools:    []string{"files_*", "read_file"},
			Body:     "# PDF\n\nRead references/forms.md first.\n",
			Files: []skill.File{
				{Path: "alias.md", Size: 6},
				{Path: "logo.png", Size: 5},
				{Path: "references/forms.md", Size: 6},
				{Path: "scripts/fill.py", Size: 9},
			},
		},
		{Name: "plain", Description: "a long plain description\nwith a break", Body: "body"},
		{Name: "quoted", Description: "Line one\nline two é"},
	}
	if diff := cmp.Diff(exported(want), exported(got)); diff != "" {
		t.Errorf("skills (-want +got):\n%s", diff)
	}
}

// TestLoadRepoSkills: this repository's own .agents/skills load as they
// are, on disk through os.Root.
func TestLoadRepoSkills(t *testing.T) {
	t.Parallel()
	root, err := os.OpenRoot(filepath.Join("..", "..", ".agents", "skills"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	got := load(t, root.FS(), ".")
	if n := names(got); !slices.Contains(n, "write-tests") || !slices.Contains(n, "write-connector") {
		t.Errorf("skills %v, want write-connector and write-tests among them", n)
	}
	for _, s := range got {
		if !strings.HasPrefix(s.Body, "# "+s.Name) {
			t.Errorf("%s: body starts %.40q, want its heading", s.Name, s.Body)
		}
		if len(s.Files) == 0 {
			t.Errorf("%s: no reference files indexed", s.Name)
		}
	}
}

// TestLoadInvalid: a skill that breaks a rule fails the load with an error
// naming its folder and the rule.
func TestLoadInvalid(t *testing.T) {
	t.Parallel()
	ok := md("name: ok\ndescription: fine", "body")
	big := func(n int) *fstest.MapFile { return &fstest.MapFile{Data: []byte(strings.Repeat("a", n))} }
	tooMany := fstest.MapFS{"s/bad/SKILL.md": md("name: bad\ndescription: d", "")}
	for i := range skill.MaxFiles {
		tooMany[fmt.Sprintf("s/bad/refs/f%03d.md", i)] = &fstest.MapFile{Data: []byte("x")}
	}
	tests := []struct {
		name   string
		fsys   fstest.MapFS
		folder string
		rule   skill.Rule
	}{
		{"no SKILL.md", fstest.MapFS{"s/bad/README.md": {Data: []byte("x")}}, "bad", skill.RuleSkillMD},
		{"no front matter", fstest.MapFS{"s/bad/SKILL.md": {Data: []byte("# just markdown\n")}}, "bad", skill.RuleFrontMatter},
		{"unclosed front matter", fstest.MapFS{"s/bad/SKILL.md": {Data: []byte("---\nname: bad\n")}}, "bad", skill.RuleFrontMatter},
		{"unsupported YAML", fstest.MapFS{"s/bad/SKILL.md": md("name: &a bad\ndescription: d", "")}, "bad", skill.RuleFrontMatter},
		{"duplicate key", fstest.MapFS{"s/bad/SKILL.md": md("name: bad\nname: bad2\ndescription: d", "")}, "bad", skill.RuleFrontMatter},
		{"no name", fstest.MapFS{"s/bad/SKILL.md": md("description: d", "")}, "bad", skill.RuleName},
		{"bad name", fstest.MapFS{"s/bad/SKILL.md": md("name: has space\ndescription: d", "")}, "bad", skill.RuleName},
		{"long name", fstest.MapFS{"s/bad/SKILL.md": md("name: "+strings.Repeat("n", 65)+"\ndescription: d", "")}, "bad", skill.RuleName},
		{"empty description", fstest.MapFS{"s/bad/SKILL.md": md("name: bad\ndescription: ''", "")}, "bad", skill.RuleDescription},
		{"no description", fstest.MapFS{"s/bad/SKILL.md": md("name: bad", "")}, "bad", skill.RuleDescription},
		{"long description", fstest.MapFS{"s/bad/SKILL.md": md("name: bad\ndescription: "+strings.Repeat("é", 1025), "")}, "bad", skill.RuleDescription},
		{"metadata not a map", fstest.MapFS{"s/bad/SKILL.md": md("name: bad\ndescription: d\nmetadata: [a]", "")}, "bad", skill.RuleMetadata},
		{"bad tools", fstest.MapFS{"s/bad/SKILL.md": md("name: bad\ndescription: d\ntools: [\"a b/c\"]", "")}, "bad", skill.RuleTools},
		{"SKILL.md too large", fstest.MapFS{"s/bad/SKILL.md": md("name: bad\ndescription: d", strings.Repeat("a", skill.MaxSkillMDBytes))}, "bad", skill.RuleSize},
		{"file too large", fstest.MapFS{"s/bad/SKILL.md": md("name: bad\ndescription: d", ""), "s/bad/big.txt": big(skill.MaxFileBytes + 1)}, "bad", skill.RuleSize},
		{"too many files", tooMany, "bad", skill.RuleFileCount},
		{"duplicate names", fstest.MapFS{"s/a/SKILL.md": md("name: same\ndescription: d", ""), "s/b/SKILL.md": md("name: same\ndescription: d", "")}, "b", skill.RuleDuplicate},
		{"symlink to a sibling skill", fstest.MapFS{"s/bad/SKILL.md": md("name: bad\ndescription: d", ""), "s/bad/x.md": link("../ok/SKILL.md")}, "bad", skill.RuleSymlink},
		{"nested symlink out", fstest.MapFS{"s/bad/SKILL.md": md("name: bad\ndescription: d", ""), "s/bad/a/b/x.md": link("../../../../secret.txt"), "secret.txt": {Data: []byte("s")}}, "bad", skill.RuleSymlink},
		{"symlinked folder out", fstest.MapFS{"s/bad/SKILL.md": md("name: bad\ndescription: d", ""), "s/bad/refs": link("../../secret"), "secret/x.md": {Data: []byte("s")}}, "bad", skill.RuleSymlink},
		{"absolute symlink", fstest.MapFS{"s/bad/SKILL.md": md("name: bad\ndescription: d", ""), "s/bad/x.md": link("/etc/passwd")}, "bad", skill.RuleSymlink},
		{"skill folder linked from outside the root", fstest.MapFS{"s/bad": link("../elsewhere"), "elsewhere/SKILL.md": md("name: bad\ndescription: d", "")}, "bad", skill.RuleSymlink},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tc.fsys["s/ok/SKILL.md"] = ok
			got, err := skill.Load(tc.fsys, "s")
			if got != nil {
				t.Errorf("Load returned %d skills with an error", len(got))
			}
			var e *skill.Error
			if !errors.Is(err, skill.ErrInvalidSkill) || !errors.As(err, &e) {
				t.Fatalf("err = %v, want an *skill.Error", err)
			}
			if e.Folder != tc.folder || e.Rule != tc.rule {
				t.Errorf("error for folder %q rule %q (%v), want folder %q rule %q", e.Folder, e.Rule, err, tc.folder, tc.rule)
			}
		})
	}
}

// TestLoadSymlinkOnDisk: on a real folder, a link out of the skill fails
// the load and a link inside it is indexed.
func TestLoadSymlinkOnDisk(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	write := func(p, s string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("skills/a/SKILL.md", "---\nname: a\ndescription: d\n---\nbody")
	write("skills/a/refs/r.md", "ref")
	write("secret.txt", "secret")
	if err := os.Symlink("refs/r.md", filepath.Join(dir, "skills/a/alias.md")); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	skills := load(t, os.DirFS(dir), "skills")
	if got := skills[0].Files; len(got) != 2 || got[0].Path != "alias.md" {
		t.Errorf("files %v, want alias.md and refs/r.md", got)
	}
	if err := os.Symlink("../../secret.txt", filepath.Join(dir, "skills/a/out.md")); err != nil {
		t.Fatal(err)
	}
	_, err := skill.Load(os.DirFS(dir), "skills")
	if e := (*skill.Error)(nil); !errors.As(err, &e) || e.Rule != skill.RuleSymlink || e.Folder != "a" {
		t.Errorf("err = %v, want rule symlink for folder a", err)
	}
}

// TestFrontMatterRefusals: an unsupported or malformed construct
// is refused, never misread.
func TestFrontMatterRefusals(t *testing.T) {
	t.Parallel()
	for _, front := range []string{
		"name: a\n  indented: x\ndescription: d\n x",
		"name: a\ndescription: \"unterminated",
		"name: a\ndescription: d\nmetadata: {a: b}",
		"name: a\ndescription: d\nmetadata:\n  a:\n    deep: x",
		"name: a\ndescription: |2\n  x",
		"name: a\ndescription: d\ntools: [a, [b]]",
		"name: a\ndescription: \"bad \\q escape\"",
		"not a key",
		" name: a",
	} {
		_, err := skill.Load(fstest.MapFS{"s/a/SKILL.md": md(front, "")}, "s")
		var e *skill.Error
		if !errors.As(err, &e) || e.Folder != "a" {
			t.Errorf("%q: err = %v, want an *skill.Error for folder a", front, err)
		}
	}
}

// TestNewCatalogDuplicates: skills from two sources with one name fail.
func TestNewCatalogDuplicates(t *testing.T) {
	t.Parallel()
	a := load(t, fstest.MapFS{"x/s/SKILL.md": md("name: s\ndescription: one", "")}, "x")
	b := load(t, fstest.MapFS{"y/t/SKILL.md": md("name: s\ndescription: two", "")}, "y")
	_, err := skill.NewCatalog(slices.Concat(a, b)...)
	var e *skill.Error
	if !errors.As(err, &e) || e.Rule != skill.RuleDuplicate || e.Name != "s" {
		t.Errorf("err = %v, want rule duplicate for s", err)
	}
	c, err := skill.NewCatalog(a...)
	if err != nil {
		t.Fatal(err)
	}
	// The catalog keeps its own copies.
	a[0].Metadata = map[string]string{"x": "y"}
	got := c.Skills()
	got[0].Tools = append(got[0].Tools, "z")
	if s := c.Skills()[0]; s.Metadata != nil || s.Tools != nil {
		t.Errorf("catalog skill changed through a copy: %+v", s)
	}
}

// skillView is a skill's exported fields, with empty and nil alike.
type skillView struct {
	Name, Description, Body string
	Metadata                map[string]string
	Tools                   []string
	Files                   []skill.File
}

func exported(skills []skill.Skill) []skillView {
	var out []skillView
	for _, s := range skills {
		v := skillView{Name: s.Name, Description: s.Description, Body: s.Body, Metadata: s.Metadata, Tools: s.Tools, Files: s.Files}
		if len(v.Metadata) == 0 {
			v.Metadata = nil
		}
		if len(v.Tools) == 0 {
			v.Tools = nil
		}
		if len(v.Files) == 0 {
			v.Files = nil
		}
		out = append(out, v)
	}
	return out
}
