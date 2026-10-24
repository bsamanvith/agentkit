---
id: T121
title: "Memory files hold readable text only"
epic: E23
status: done
depends_on: [T058]
---

# T121: memory files hold readable text only

## Context

The owner wants memory as plain readable text with size caps (E23). The caps
exist (T058); nothing stopped a file holding NULs, escape sequences or bytes
that are not UTF-8, which a person reviewing memory cannot read and a
terminal or page may misrender.

## Scope

- `memory.CheckText` and `memory.ErrNotText`: valid UTF-8, no control
  character but tab, LF, CR.
- Every backend refuses, through `fileops`, a create, the new text of a
  str_replace and the text of an insert that is not readable text, and
  changes nothing.
- The memory tool's error text says memory holds plain readable text only;
  its description says files are plain text with size limits.
- `memorytest`: a `Text` case, and the model and the simulation know the rule.

## Out of scope

- The search store's items (E23: RAG later).
- Rejecting files written before the rule: they can still be read, edited
  and deleted.

## Acceptance criteria

- [x] Readable text (tabs, CRLF, any script, emoji) is stored; bytes that are not UTF-8 and other control characters (C0, DEL, C1) are refused with where they are. — test: `memory.TestCheckText`
- [x] On every backend, each writing command refuses such text with `ErrNotText` and changes nothing. — test: `memorytest` `Text` (inmem, sqlite, postgres), and the simulation's model
- [x] Through the tool, the refusal is a tool failure saying memory holds plain readable text only. — test: `memory.TestToolRefusesTextThatIsNotReadable`

## Notes

- Mutation check: dropping the check from `fileops.Insert` fails `Text` and `Simulation`.
- The check runs on what a command writes (`new_str`, `insert_text`), not the
  whole edited file, so a file from before the rule can still be fixed.
