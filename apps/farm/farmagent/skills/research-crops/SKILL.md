---
name: research-crops
description: >
  Help a farmer research crops for their plot: what grows when, how crops
  compare month by month, how the sun, day length, slope, shade and the
  seasons change the answer, and where to read more. Use when the farmer
  asks "what can I grow", "which is better, X or Y", "when does X do
  well here", or about sunlight, shade, slope or the angle of the sun.
tools: [get_farm, list_crops, get_crop, get_climate, get_sun, compare_crops, recommend_crops, survey_land, knowledge_search, knowledge_read, web_search]
---

# research-crops

The numbers come from the tools; the explaining is yours. Never score a
crop, a month or a slope yourself.

## 1. The plot's year

- `get_farm`, then `get_sun` for the plot: day length, hours of bright
  sun, the noon sun's side and height, cloud, and the slope. Its notes say
  what matters; use them in plain words, in the farmer's language.
- If the sun report says the plot is not surveyed and the farmer's land is
  hilly or sloping, offer `survey_land`: the slope's direction changes the
  sun (a north-facing slope gets much less in winter; south-facing more).
- `get_climate` gives the months' heat and rain beside the sun.

## 2. Compare crops

- When the farmer names crops, or asks what grows, pick two to six crops
  (theirs, or from `list_crops` by kind, or from `recommend_crops`) and
  call `compare_crops`. Show it as a short table: crop, then a mark per
  month (good 70+, fair 45+, poor), then the best sowing and its main risk.
- Explain the months: heat, rain, cloud and day length decide them. A
  dull monsoon lowers sun-loving crops (`sun.low`); shade crops such as
  cocoa need cover in the open Deccan sun (`sun.strong`); turmeric and
  ginger bear part shade and fit under trees; short-day crops (rice,
  soybean, sorghum, pigeon pea, sesame) flower early when sown late, so
  sowing on time matters (`day.short`).
- `get_crop` has a crop's ranges (temperature, water, pH, spacing, light,
  day length) for "why".

## 3. Shade and layout

- Inside the tropics (south of about 23° N, most of India) the noon sun
  is to the north of the plot in the summer and monsoon months and to the
  south in winter. Tall crops and trees shade the north side of their
  neighbours in kharif and the south side in rabi; north to south rows
  shade each other least. The plan and plant-group checks count this for
  the months each crop stands in the field.

## 4. Read more

- `knowledge_search` and `knowledge_read` for the handbook's practice;
  cite it in a few words.
- If the farmer wants local varieties, prices or what farmers elsewhere
  do, `web_search` (it needs their own search key: if it says so, tell
  them to add one in the app's settings).

The rules' values are starter values from FAO and Indian packages of
practice: say so when the farmer is about to stake a season on a close
call, and suggest the local KVK or agricultural officer.
