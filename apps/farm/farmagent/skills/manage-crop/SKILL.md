---
name: manage-crop
description: >
  Tell a farmer what their standing crop needs now: this week's and next
  week's work from its calendar, adjusted for the weather. Use when the
  farmer asks "what should I do this week", "when do I put urea", "when to
  irrigate / weed / harvest", or says how old their crop is.
tools: [get_farm, crop_calendar, get_week, set_profile, knowledge_search]
---

# manage-crop

## 1. Find the crop and its age

- Call `get_farm`. With a saved plan, the sowing dates are there. Without
  one, ask which crop and when it was sown ("about 25 days ago" is
  enough), and use them as `parts` for the calendar.

## 2. The work due

- Call `crop_calendar` with `from` today and `to` two weeks ahead.
- Call `get_week` (when available) and adjust: no top-dressing or spray
  the day before heavy rain (pick one of the dry days); bring a harvest
  forward ahead of rain; irrigate in a dry week at flowering.

## 3. Answer

- A short list, soonest first: the day, the work, one line of how. Then
  any weather warning. Keep it to what the farmer asked.
- Use `knowledge_search` for the why (fertiliser splits, critical stages)
  only when asked, and cite the handbook in a few words.
- The calendar's timings are general rules for the kind of crop; if the
  farmer's variety differs (a short-duration paddy), say the dates move
  with it.
