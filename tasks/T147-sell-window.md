---
id: T147
title: Seasonal price index, harvest-month advice and its backtest
epic: E25
status: todo
depends_on: [T140]
---

# T147: Seasonal price index, harvest-month advice and its backtest

## Context

The owner's brief of 2026-11-15: crop cycles at the centre of the farm
app, basics first, everything else optional and additive. Design:
[CYCLES.md](../apps/farm/docs/CYCLES.md), section "When to harvest and sell".

## Scope

- `apps/farm/prices`: seasonal index per crop and market from the history, harvest-month advice inside a sowing window, sell-or-hold with storage cost, a walk-forward backtest against naive baselines, and a gate that hides advice that does not beat them.
- A loader for CEDA and India Data Portal files; `make farm-price-backtest`.

## Out of scope

- API routes, web and `cmd/farm` wiring (the integration after T145).

## Acceptance criteria

Each item is one observable behavior and maps to one test. The
implementer fills these in from the design before writing code.

## Notes
