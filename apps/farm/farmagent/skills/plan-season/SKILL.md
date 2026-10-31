---
name: plan-season
description: >
  Help a farmer decide what to plant on a plot, when to sow it and how to
  share the plot between crops. Use when the farmer asks what to grow, when
  to sow, whether a crop suits their land, or wants a plan for the season.
tools: [get_farm, list_soils, set_profile, recommend_crops, get_crop, suggest_layout, save_plan, knowledge_search]
---

# plan-season

The numbers come from the tools, the advice from you. Never score a crop,
pick a date or count plants yourself.

## 1. Know the plot

- Call `get_farm` for the plot; ask which plot if the farmer has several
  and did not say.
- Look at its profile. What is missing, ask for, one question at a time,
  in the farmer's words:
  - **Soil.** If they do not know the type, call `list_soils` and describe
    two or three likely ones by their signs (colour, cracks when dry,
    sticky when wet) in their language; let them pick.
  - **Water.** Rain only, or a canal, river, tank, borewell, open well or
    farm pond? Does it run dry in summer (then it is not reliable)? Drip?
  - **Last season's crop**, for rotation.
- Save what they tell you with `set_profile` (the whole profile each time).
  Do not wait for everything: a recommendation without the soil is still
  useful, and says the soil was not scored.

## 2. Recommend

- Call `recommend_crops`. When the farmer has a time in mind ("after
  Dasara", "in June"), turn it into a date and pass it as `around`: it is
  approximate, the tool looks either side of it. When they ask about one
  crop, pass it in `crops`.
- Present three to five options, best first. For each: the crop's name in
  the farmer's language, the best sowing date and window, harvest, and the
  one main risk. Say plainly when a crop is new to India (a climate
  analog) and that it is best tried on a small area first.
- If the farmer's own choice scores poorly, say why (from its risks) and
  what would make it work (irrigation, another date, another soil), or
  offer the nearest good option.
- The `knowledge_search` and `knowledge_outline` tools have the handbook
  for the why: sowing after 50 to 75 mm of rain, residual moisture,
  intercrop ratios.

## 3. Lay it out

- If the farmer wants more than one crop, agree the shares ("60 % cotton
  with green gram, 40 % soybean") and call `suggest_layout`.
- Read back each zone's area in acres and guntas (use `convert_area`),
  rows and plant counts, and any warnings from the checks.
- When the farmer agrees, call `save_plan`; it appears on their map.
