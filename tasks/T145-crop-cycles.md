---
id: T145
title: Crop cycles - a suggested line and the farmer's own, linked, three at most
epic: E25
status: todo
depends_on: [T141]
---

# T145: Crop cycles - a suggested line and the farmer's own, linked, three at most

## Context

The owner's brief of 2026-11-15: crop cycles at the centre of the farm
app, basics first, everything else optional and additive. Design:
[CYCLES.md](../apps/farm/docs/CYCLES.md), section "Cycles".

## Scope

- `apps/farm/cycle`: the cycle model, the suggested line (linked through harvest, turnaround and rotation), the farmer's own line, the planned cycle, checks of own cycles; a store with a conformance suite, in memory and SQLite.
- Routes, the assistant's tools, `cmd/farm` wiring, the *Cycles* view on a plot (a lazy chunk).

## Out of scope

- Inputs and sprays (T148), sell timing (T147), drawing (T146).

## Acceptance criteria

Each item is one observable behavior and maps to one test. The
implementer fills these in from the design before writing code.

## Notes
