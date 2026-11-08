---
name: draw-crop-plan
description: >
  Draw a season plan for a plot: what to sow, when to sow it, where it
  goes, and the work through to harvest (preparation, weeding,
  fertiliser, irrigation, harvest). Use when the farmer asks for a plan,
  "when should I plant", "what do I do after sowing", or a calendar for
  the season.
tools: [get_farm, recommend_crops, compare_crops, suggest_layout, crop_calendar, get_week, save_plan, convert_area, knowledge_search]
---

# draw-crop-plan

A plan has three parts the farmer can act on: **when to sow**, **where**,
and **the work after sowing**. Every date, score, area and count comes from
the tools. Follow `plan-season` for gathering soil and water first if the
plot's profile is empty.

## 1. When to sow

- Call `recommend_crops` (with `around` when the farmer has a time in mind)
  or, when they are weighing a few crops across the year, `compare_crops`.
- Agree the crops and the sowing date of each. Use the tool's best date,
  inside its window; say the window, so the farmer can wait for the rain.

## 2. Where

- For more than one crop, agree the shares and call `suggest_layout`; read
  each zone's area back in the farmer's units with `convert_area`.

## 3. The work after sowing

- Call `crop_calendar` with the same crops and sowing dates as `parts`
  (or none, for the saved plan). Draw the plan as a short calendar the
  farmer can follow, grouped by fortnight or month:

  | When | Crop | Work |
  |---|---|---|
  | 24 Sep | Maize | Plough twice, spread manure |
  | 15 Oct | Maize | Sow, 60 x 20 cm |
  | ... | | |
  | 13 Jan to 12 Feb | Maize | Harvest |

  Keep it to the tasks that matter (sowing, weeding, each fertiliser
  split, the critical irrigations, harvest); mention that walking the
  field for pests every two weeks is assumed.
- Say once that the timings are general rules for the kind of crop and the
  seed packet or local package of practices gives the variety's.
- If sowing is within the next week, call `get_week` and move the date if
  heavy rain is due on it.

## 4. Save

- When the farmer agrees, `save_plan`. Tell them the app will now check
  each week's weather against these crops and send an alert only when they
  need to act, and list the week's work every Monday.
