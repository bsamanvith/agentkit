---
id: T148
title: Inputs and sprays - a shopping list and a spray schedule from crop, area and method
epic: E25
status: todo
depends_on: [T134]
---

# T148: Inputs and sprays - a shopping list and a spray schedule from crop, area and method

## Context

The owner's brief of 2026-11-15: crop cycles at the centre of the farm
app, basics first, everything else optional and additive. Design:
[CYCLES.md](../apps/farm/docs/CYCLES.md), section "Inputs and sprays".

## Scope

- `apps/farm/inputs`: a data file of per-crop nutrition and plant protection, organic and conventional, each value with its source; `Plan` from crop, area, method and sowing date to a shopping list in sold units and a dated schedule.

## Out of scope

- API routes, web and `cmd/farm` wiring (the integration after T145); the agronomist's review.

## Acceptance criteria

Each item is one observable behavior and maps to one test. The
implementer fills these in from the design before writing code.

## Notes
