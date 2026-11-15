---
id: T146
title: Drawing a plot by pencil, lines or rectangle, areas inside it, and a first-run path
epic: E25
status: todo
depends_on: [T133]
---

# T146: Drawing a plot by pencil, lines or rectangle, areas inside it, and a first-run path

## Context

The owner's brief of 2026-11-15: crop cycles at the centre of the farm
app, basics first, everything else optional and additive. Design:
[CYCLES.md](../apps/farm/docs/CYCLES.md), section "Onboarding".

## Scope

- The plot editor: pencil (freehand, simplified to corners), lines, rectangle (drag, rotate, stretch), beside GPS walk and typed coordinates; undo; corner drag.
- Areas inside a plot: cut by a line, or draw a shape; they become the plan's zones.
- The first-run path: village, draw, crop, dates.

## Out of scope

- The cycle model and its API (T145).

## Acceptance criteria

Each item is one observable behavior and maps to one test. The
implementer fills these in from the design before writing code.

## Notes
