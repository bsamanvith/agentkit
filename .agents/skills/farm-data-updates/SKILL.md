---
name: farm-data-updates
description: Check the farm app's knowledge and rule data against its sources and bring it up to date (crop insurance and scheme cut-offs, minimum support prices, banned pesticides, input doses, district sowing windows, the handbook). Use when asked to check for updates to the farm knowledge base, insurance details, scheme dates, MSP or the banned list, or before a sowing season starts. Runs only when someone invokes it; it schedules nothing.
---

# farm-data-updates

**Goal: every value a farmer reads still matches the document it came from, and the data says when that was last checked.** Why the data is built this way (a base plus region overlays, every value with its source): [REGIONS.md](../../../apps/farm/docs/REGIONS.md); the inputs and their review list: [INPUTS.md](../../../apps/farm/docs/INPUTS.md); prices and MSP: [PRICES.md](../../../apps/farm/docs/PRICES.md).

Worked example: the first run (2026-11-16, [T152](../../../tasks/T152-farm-data-updates-skill.md)), on the insurance schemes.

This skill never runs by itself. Do not create a schedule, a routine or an automation for it; the owner asked for a check only when it is invoked (2026-11-16).

## Steps

1. **List what there is to check.** `python3 .agents/skills/farm-data-updates/scripts/sources.py` prints every dated source, oldest first, with the file it is in and what it is used for. Narrow it with `--kind schemes|windows|varieties|wide|inputs|banned|methods|msp` when the request names one, or `--older-than 90`. The handbook (`apps/farm/handbook/docs`) has no dates: check it only when asked, section by section against the sources its pages name.
2. **Pick by season, not by age alone.** Insurance cut-offs and notified crops change every season (kharif notices from April, rabi from September); MSP every May or June and September or October; the banned list whenever the ministry notifies; sowing windows, doses and variety lists rarely (a new edition of a university guide or a district plan); the experimental tier only when FAO changes ECOCROP (rebuild it with `apps/farm/agro/data/ecocrop/build.py`). Say which you will check and which you leave.
3. **Read the source itself.** Open the listed URL, then look for a newer edition or notice from the same publisher (the state's crop insurance notification, the PMFBY guidelines, the Cabinet's MSP decision on PIB, the CIB&RC list). A value changes only from a document you opened and read in this run: never from a search snippet, a news summary of a notice you could not open, or memory. Free sources first; say so when one is paywalled or free for non-commercial use only.
4. **Compare, value by value.** For each source write down: same, changed (old and new, with the page or paragraph), newer edition found, or could not be reached. A source that did not load is "not checked", not "unchanged".
5. **Change the data, not the code.** Edit the JSON (`apps/farm/region/data`, `apps/farm/inputs/data`, and `apps/farm/agro/data/crops.json` for varieties) or, for MSP, the table in `apps/farm/prices/msp.go`. Keep the `ref` (source id and where in it), add a new `sources` entry for a new document, and set `read` to today on every source you opened, changed or not. A season's scheme gets a new id (`...-rabi-2026`), the old one stays until its season ends. The loader refuses an unknown key, an unknown crop or a banned product, naming the file.
6. **Check.** `go test ./apps/farm/region/... ./apps/farm/inputs/... ./apps/farm/prices/... ./apps/farm/cycle/...`, then `make check`. A changed dose or window that moves a test's expected value is reviewed, not just re-recorded.
7. **Report and hand over.** A short table: source, checked on, result, what changed. Name anything a farmer would see differently, anything not reachable, and what still needs an agronomist (ANGRAU, a KVK). Update [FEATURES.md](../../../apps/farm/docs/FEATURES.md) with the `farm-features` skill when a farmer-visible value changed, and open one PR for the run.

## Done when

- Every source in scope is listed as same, changed, newer edition found or not reachable, with today's `read` date on the ones opened.
- No value was changed from a snippet or from memory; each change names the document and where in it.
- The tests and `make check` pass, and the PR says what a farmer will see differently.
- Nothing was scheduled.
