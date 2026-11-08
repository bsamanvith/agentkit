---
name: weather-check
description: >
  Read a plot's coming week of weather against its crops and decide whether
  the farmer must do anything. Use in the weekly background check, or when
  a farmer asks "will the weather hurt my crop", "should I spray / put
  fertiliser / harvest this week", or about rain, heat, wind or cold ahead.
tools: [get_farm, get_week, crop_calendar, list_alerts, knowledge_search, notify_farmer]
---

# weather-check

The forecast and the advice come from `get_week`; your part is judging
what matters to this farmer and saying it plainly. Never forecast weather
yourself or change a number the tool gave.

## 1. Read the week

- Call `get_week` for the plot. It gives the daily forecast, each crop of
  the plan with its stage today, the plan's work due this week, and
  `advice`: only what needs doing. **Empty advice is a quiet week.**
- If there is no saved plan, the advice covers only weather that harms
  any land (heavy rain, damaging wind).

## 2. Decide

- Act on `urgent` and `warning` advice. Pass on `advice` items when they
  change this week's work (a top-dressing to move, a dry week at
  flowering).
- Look at `tasks` together with the advice: a top-dressing or spray due
  the day before heavy rain should move to a dry day the tool named; a
  harvest due in the window of rain should come forward.
- In a background run, call `list_alerts` (unread) first: do not repeat a
  warning the farmer has not read yet.
- If nothing needs doing, **send nothing**. In a background run, end with
  a one-line log such as "quiet week: 4 mm rain, maize vegetative".

## 3. Tell the farmer

- In a background run, one `notify_farmer` per thing to do (at most three;
  put related items together), most pressing first:
  - **title**: what and when, e.g. "Heavy rain Thursday: harvest the green gram"
  - **body**: the action first, then why in a few words from the tool
    ("85 mm forecast Thursday"), in the farmer's language, no jargon.
  - **severity** and **day** from the advice.
- In a chat, answer in the same shape: what to do, by when, why. Offer the
  handbook's reason (`knowledge_search`) only if they ask why.
