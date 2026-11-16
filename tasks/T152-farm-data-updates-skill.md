---
id: T152
title: Live-test fixes for the assistant and cycles, "I already sowed" in one step, and the farm-data-updates skill
epic: E25
status: review
depends_on: [T145, T149]
---

# T152: Live-test fixes for the assistant and cycles, "I already sowed" in one step, and the farm-data-updates skill

## Context

The owner's asks of 2026-11-16: can the assistant reach the crop, soil
and climate libraries and recommend well when things are a mix and not
neatly categorised; "already sowed" as a normal entry on the page and in
chat, through tools and APIs, tested; and a skill that checks the
knowledge base and insurance details for updates when it is invoked (not
automatically).

## Scope

- A live test of the assistant on a real model at a Kadapa plot, and the
  fixes for what it showed.
- "I already sowed": the crop and the day in one step on the page, the
  route and `set_cycle`.
- The skill `.agents/skills/farm-data-updates` with a script that lists
  every dated source behind the data.

## Out of scope

- Asking for a village before drawing a plot (T150) and insurance on
  Season work (T151).
- New crops in the library (horse gram and castor are missing: the live
  test's question about them was answered from the model's general
  knowledge, and it said so).
- A scheduled update check: the owner wants it only when invoked.

## Acceptance criteria

- [x] The suggested line never plans a crop new to the country, in any month, on unknown, red sandy or black soil, with or without a borewell, and always suggests something — test: `cycle_test.TestSuggestionIsNeverACropNewToTheCountry`
- [x] `Service.Sowed` records a crop the farmer names and the day in one step, whatever was suggested; a refused day or crop saves nothing; the same crop again only moves the day; no crop keeps the planned one — test: `cycle_test.TestSowedACropInOneStep`
- [x] `POST /api/farms/{id}/cycles/sown` takes `crop` with `day`; the page's form sends the crop the farmer picks — test: `cycles.test.tsx` "records a crop the farmer already sowed that is not the planned one, in one step"
- [x] `set_cycle` with `sown` and `crop` records both in one call; a crop for the current cycle with a sowing day gone by is recorded as sown — test: `farmagent_test.TestCycleTools`
- [x] The model is sent the day with every question — test: `farmagent_test.TestAssistantAnswersFromTheTools`
- [x] `scripts/lint-skills.sh` passes with the new skill

## Notes

- Live test (2026-11-16, `gemini-3.5-flash` and `gemini-3.5-flash-lite`;
  the Anthropic key in the cloud environment was refused with 401):
  - A mixed plot (half red sandy, half blackish, a borewell till
    February, groundnut with red gram and mango on the border) was
    answered from `get_climate`, `recommend_crops`, `suggest_layout`,
    `check_plan` and the handbook, in parts by soil.
  - The suggested cycle for that plot was chia, a crop new to India: it
    scored 94 and trees were left out. Fixed: such crops are left out of
    the app's own plan (the recommender still lists them, marked).
    Leaving them out showed a hole (rain only, unknown soil, September:
    nothing within the 240-day wait); the line now takes the next good
    sowing past the limit instead of showing nothing.
  - "I already sowed sunflower on 9 November" was sent as 2025, 2024 and
    2023, each refused, and once answered "noted" with nothing saved. The
    day now goes with every question, the refusal names today, the
    system text says to claim a save only after it succeeded, and each
    refused tool call is logged. After the fix: one `set_cycle` call,
    "last Monday" read as 9 November 2026, saved as sown.
  - The page's "I already sowed on..." asked only for a day and recorded
    the suggestion as sown. It now asks what and when.
  - Horse gram and castor on stony murram: not in the library; the
    answer came from the model's general knowledge and said so.
- Skill dry run (2026-11-16, `--kind schemes`): the Deccan Chronicle
  article behind Andhra Pradesh's kharif 2026 scheme was opened again
  and says what the data says (22 notified crops, five named with sums
  insured, no cut-off date); its `read` date moved to today. A search
  found no Andhra Pradesh notification for rabi 2026-27, so no rabi
  scheme was added; the PMFBY guidelines were not reopened. Changed in
  the skill after the run: step 4 says a source that did not load is
  "not checked", not "unchanged".
