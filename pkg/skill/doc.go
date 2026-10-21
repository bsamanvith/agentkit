// Package skill gives an agent Agent Skills: packaged know-how (a folder with
// a SKILL.md and reference files) that the model loads only when a task
// needs it.
//
// The format is the one Claude Code and the Claude API use: SKILL.md starts
// with YAML front matter holding name and description (and optional metadata
// and tools), then Markdown instructions; the other files of the folder are
// references the instructions point to. Existing skill folders work as they
// are.
//
// Skills reach the model by progressive disclosure:
//
//   - [Catalog.SystemText] is the list of names and descriptions for the
//     system prompt: a few tokens per skill, in a stable order, so the
//     prompt cache holds.
//   - [Catalog.Toolset] gives the two tools that read the rest: skill_load
//     returns a skill's instructions and the index of its files, and
//     skill_read returns one of those files.
//
// [Load] reads every skill folder under a root of an [fs.FS] and validates
// it: the name follows the tool name rule, the description is required and
// short, files have size limits, and a symbolic link may not leave its
// skill's folder. A bad skill fails with an [*Error] naming the folder and
// the [Rule] it breaks.
//
// Reading is confined to the skill's folder: [Skill.Read] refuses absolute
// paths, any ".." element, paths that are not in the skill's index, symbolic
// links that lead out of the folder (checked again at every read, element by
// element, through [fs.ReadLinkFS]) and files that are not text. An fs.FS
// without [fs.ReadLinkFS] (such as embed.FS) is taken to have no symbolic
// links. For folders on disk, pass the FS of an [os.Root] (or os.DirFS):
// with os.Root, even a link swapped in after the check cannot leave the root.
//
// Skill scripts are never run: a skill that has scripts is listed, and its
// files can be read, but nothing in this package executes them.
package skill
