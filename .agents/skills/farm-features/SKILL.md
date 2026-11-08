---
name: farm-features
description: Read and keep up to date the farm app's product list (what farmers can do, how each feature works, where its code is, its data providers, and what is most needed next). Use when asked what the farm app does for farmers, how a farm feature works, what to build next for farmers, or when a change adds, changes or removes anything a farmer can see or a data provider in apps/farm or web/apps/farm.
---

# farm-features

**Goal: [apps/farm/docs/FEATURES.md](../../../apps/farm/docs/FEATURES.md) always says what farmers can do today, how it works, and what is most needed next.** Why the app exists and the farmer's year: [PRODUCT.md](../../../apps/farm/docs/PRODUCT.md); the epic: [E25](../../../tasks/E25-farm-app.md).

Worked example: the first list (2026-11-08), written from T133 to T138 and the weather and price work in flight.

## Answering "what does it do" or "how does X work"

1. **Start from the list.** Read FEATURES.md: the table for status, the feature's section for what the farmer does, what the app does and *Behind it* (packages, tools, skills, web files).
2. **Go one level down only when asked how.** The feature's design doc (LAND.md, CROP-PLANNING.md, GROUPS.md, SUN.md in `apps/farm/docs/`), then the packages it names. Tools and skills the assistant uses are in `apps/farm/farmagent` and `apps/farm/farmagent/skills`.
3. **Check it is still true before saying it.** `git log --oneline -- apps/farm web/apps/farm | head` and the open branches: a feature marked *building* may have merged, a *live* one may have changed. Fix the list if it is wrong (below).
4. **Answer in the farmer's terms first** (what they do, what they see), then the mechanism. No package names unless asked.

## Updating the list (in the same PR as the change)

1. **Change the row and the section.** A new farmer-visible feature gets a row (next number, never reused) and a section under *Live*, *Building now*, *Planned* or *Ideas* with three parts: **The farmer** (what they do), **The app** (what it does, the checks), **Behind it** (packages, tools, skills, web files, design doc). A changed feature updates its text; a feature that moved status moves section and updates *Since* with the task ID.
2. **Data providers.** A new, changed or dropped source of data (weather, prices, map, heights, climate, news, knowledge, model) updates the *Data providers* table. Every kind of data is an interface with more than one provider, picked in one place (the `farm` binary's flags), never a switch inside a feature: [AGENTS.md, Seams](../../../AGENTS.md#seams). Free sources and free tiers first; fill the *Cost* column, and name anything paid or non-commercial-only in the PR and in your reply.
3. **Write for the farmer.** Units they use (acres, guntas, cents), their seasons (kharif, rabi), their words; short sentences, no arrows.
4. **Bump the date** on the *Last updated* line.
5. **Check.** `scripts/lint-skills.sh` (links and anchors), then `make lint`.

## Deciding what is most needed next

1. **Reread the farmer's year** in [PRODUCT.md](../../../apps/farm/docs/PRODUCT.md#the-farmers-year): which stage has no help yet.
2. **Rank by** what unblocks the most other features (accounts before notifications), what farmers ask most in season, and what can start in parallel with the work in flight without touching the same packages.
3. **Write the ranking** in *What is most needed next* with one line of why per item and the date; move items between *Ideas* and *Planned* when the owner decides.
4. **Turn the top item into tasks** with the `plan` skill when the owner wants it built.

## Done when

- Every farmer-visible change in the PR has its row and section right, with status and *Since*.
- The *Data providers* table matches the providers the `farm` binary can pick.
- *Last updated* is today, and the lint passes.
