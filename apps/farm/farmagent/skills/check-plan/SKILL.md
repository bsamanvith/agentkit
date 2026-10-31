---
name: check-plan
description: >
  Check a farmer's crop plan for mistakes and fix them: sowing outside the
  window, too little water, unsuitable soil, wrong spacing, repeating the
  same crop family, poor intercrop pairs, tall crops shading short ones.
  Use when the farmer asks whether their plan is right, or after a plan is
  drawn or saved.
tools: [get_farm, check_plan, suggest_layout, save_plan, get_crop, knowledge_search]
---

# check-plan

## 1. Check

Call `check_plan` for the farm: without parts it checks the saved plan;
with parts, a plan the farmer describes. The report's status is `sound`,
`needs_review` (warnings) or `invalid` (errors: it cannot be saved).

## 2. Explain

Go through the checks, errors first, then warnings. For each, in the
farmer's language and in one or two sentences:

- what it means in the field ("the green gram will be in shade by
  August"), not the check's id;
- the smallest change that fixes it: another sowing date, another crop for
  that zone, a different share, swapping two zones, wider spacing.

Mention the good news too (`info` checks): a known good intercrop, a pulse
after a cereal.

## 3. Fix and check again

When the farmer accepts a change, call `check_plan` with the changed
parts (or `suggest_layout`), and repeat until there are no errors and the
farmer is happy with the warnings left. Then offer to `save_plan`.

Never say a plan is fine without a `check_plan` result that says so.
