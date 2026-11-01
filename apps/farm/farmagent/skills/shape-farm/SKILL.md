---
name: shape-farm
description: >
  Lay out the fixed parts of a farm on the plot — the gate, a cart path, a
  shed, a farm pond and a border of trees — where they work best, check
  the layout, and save it when the farmer agrees. Use when the farmer
  asks where to dig a pond, put a shed or gate, plant border trees, or how
  to arrange their farm.
tools: [list_farms, get_farm, survey_land, suggest_design, check_design, save_design, knowledge_search, knowledge_read]
---

# shape-farm

The places, sizes, volumes and checks come from the tools. Never draw a
layout or size a pond yourself.

## 1. Know the plot and what the farmer wants

- `get_farm`, then `survey_land`: the road side, water nearby, the slope,
  the size.
- Ask which parts they want if they did not say, one short question:
  a gate and cart path, a shed (store or cattle), a farm pond, a border of
  trees. Ask only about sizes they care about; the tool picks sensible
  defaults (a farm pond of 10, 15, 20 or 30 m a side, 3 m deep, by plot
  size; a 6 × 4 m shed; a 3 m border with a tree every 4 m).

## 2. Suggest

Call `suggest_design` with only the parts wanted. Explain each part in a
sentence from its `why`: the gate on the road side, the pond at the low
end or by the drain, the shed by the gate. Give the pond's volume in
litres, the number of border trees, and how much land is left to crop
(`report.fields_m2`, in the farmer's units: call `convert_area`).

For border trees, the handbook (`knowledge_search` "border trees") lists
species for the region; suggest two or three and why.

## 3. Check and adjust

- Go through `report.checks`: errors first, then warnings, in plain words
  with the smallest fix (a smaller pond, another corner, no trees on the
  power line side).
- When the farmer wants a change, call `suggest_design` again with the new
  sizes, or edit the raw design and call `check_design`.
- If the farm has a season plan, a pond or shed takes land from its zones
  (`pond_in_crop_zone`): say how much.

## 4. Save

Only when the farmer agrees, call `save_design` with the raw design. A
design with errors is not saved. Never say a layout is fine without a
check that says so.

The rules' sizes and setbacks are starter values; for a pond under a
government scheme (MGNREGA, state farm pond schemes), tell the farmer to
confirm the size with the local agriculture office.
