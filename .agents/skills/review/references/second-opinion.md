# Second opinion from another agent CLI

Optional step 4 of the `review` skill. Another agent reads the same diff with the
same checklist, read-only, and you merge what it finds. It is worth running
even when the other CLI is the same kind of agent as you: it starts with no
context from your review.

## Which one

```sh
command -v codex >/dev/null && echo codex
command -v claude >/dev/null && echo claude
```

Prefer a CLI other than yourself (Claude Code: `codex`; Codex: `claude`). If
only your own is installed, use it. If neither is, skip the step silently.

## The prompt

Write the diff to a file first, so both agents see exactly the same change:

```sh
git diff origin/main...HEAD > /tmp/review.diff   # or the PR's / range's diff
```

The prompt (fill in the task file):

```text
Review the change in /tmp/review.diff in this repository, read-only: do not
edit files, commit, or run anything that writes. It implements tasks/T0xx-....md.
Use the checklist in .agents/skills/review/references/checklist.md, in order.
Read each changed file whole. For each problem give: BLOCKING or OPTIONAL,
file:line, what is wrong, the rule (doc and section), the fix. Only report what
you can point to in the code. End with "NO FINDINGS" if there are none.
```

## The commands

Run in the repository root, non-interactive, read-only:

```sh
# Codex: the read-only sandbox refuses writes and network.
codex exec --sandbox read-only "$(cat /tmp/review-prompt.txt)" > /tmp/review-codex.txt

# Claude Code: only reading tools are allowed; anything else is refused in -p
# mode. The prompt goes on stdin: the tool lists take several values and would
# swallow a prompt given after them.
claude -p --allowedTools "Read,Grep,Glob,Bash(git diff:*),Bash(git log:*),Bash(git show:*)" \
  --disallowedTools "Edit,Write,NotebookEdit" \
  < /tmp/review-prompt.txt > /tmp/review-claude.txt
```

Give it a time limit (`timeout 900 ...`). If it fails or times out, say so in
one line of the report and continue without it.

## Merging

- **Verify every finding yourself** in the code (and by a test or a plant if it
  is about behavior). Drop what does not hold, and do not report it.
- Merge duplicates into one finding, citing both sources.
- Mark each finding with where it came from: `[review]`, `[codex]`, `[claude]`,
  or `[review, codex]` for both.
- Keep its rank only if you agree; otherwise rank it yourself and say why.
