---
name: survey-land
description: >
  Read a farmer's plot and what is around it: its shape and size, which
  way it runs and slopes, and how far the nearest road, canal, river,
  drain, pond, houses, power line and village are, and what that means
  for farming. Use when the farmer asks about their land's shape, access,
  water nearby, neighbours, a place's distance, or a point's position.
tools: [list_farms, get_farm, survey_land, nearby_features, locate_point, measure, find_place, knowledge_search]
---

# survey-land

Every distance, direction, size and slope comes from a tool. Say
"about" for straight-line distances: the way by road is longer.

## 1. Survey

- Find the plot (`list_farms`; ask which one if the farmer has several and
  did not say), then call `survey_land`. The saved survey is reused; pass
  `refresh` only when the farmer says the map is out of date or asks again
  after a while.
- If `missing` is not empty, say what could not be looked up (the map or
  the slope) and go on with the rest.

## 2. Tell the farmer what matters

In the farmer's language, short, most useful first:

1. **Shape and size**: the kind (square, rectangle, long strip,
   irregular), length and width in metres, which way the long side runs,
   and the shape's notes (rows along the long side; odd corners for trees
   or a pond).
2. **Access**: the nearest road or track, how far and which side; if a
   road meets the plot, that side is where the gate goes.
3. **Water**: canals, rivers, drains, ponds and wells near the plot — a
   possible water source, where excess water can go, or a water course
   running through the plot.
4. **Slope**: which way the land falls and how much. When `flat` is true,
   the elevation data cannot tell (90 m grid): ask the farmer where rain
   water stands.
5. **Neighbours and hazards**: houses close by (spray drift, animals), a
   busy road (dust), an overhead power line (no tall trees under it), the
   nearest village.

Use the survey's `notes` for what each means; do not add facts the tools
did not give.

## 3. Map questions

- "How far is X from my field?": `find_place` with the farm's id; give the
  straight-line distance and direction.
- "Where is this point?" (coordinates from the farmer or their phone):
  `locate_point`.
- "How long is this line / big is this area?": `measure` (closed for an
  area).
- "Is there a canal within 1 km?": `nearby_features` with kinds and
  within_m.

## 4. When the farmer knows better

OpenStreetMap misses many village tracks, field channels and new
buildings. When the farmer says something the map does not show, believe
them, and say the map may be out of date.
