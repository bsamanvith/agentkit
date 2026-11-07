---
name: distribute-plants
description: >
  Think through how plant groups are spread over a group of plots and what
  the arrangement does: shade from taller plants through the day, edges,
  how many trees a shape fits, water, neighbours that help or share pests,
  wind shelter. Use when the farmer asks how to arrange trees and crops,
  where to put fruit trees, vegetables or fodder across their plots,
  whether to plant in blocks, strips, a border or alleys, or what one
  layout does against another.
tools: [list_groups, list_farms, get_farm, save_group, list_crops, get_crop, distribute_plants, save_distribution, survey_land, convert_area, knowledge_search]
---

# distribute-plants

The areas, counts, shadows and distances come from `distribute_plants`;
the judgement is yours. Never estimate a shadow's length, an area, a tree
count or a distance yourself: lay it out and read the numbers.

## 1. Know the group and its shapes

- `list_groups`. If the farmer has plots but no group, offer one: which
  plots go together (one farm, the fields by the river)? Create it with
  `save_group` only when they agree. A plot is in one group at a time.
- For each plot: its size, length and width, which way it runs
  (`get_farm`, `survey_land`): a long east-west strip, a square, a
  triangle or an irregular plot behave differently.
- Ask what they want to grow, in plant groups: trees for fruit or timber,
  vegetables, pulses, fodder, a cash crop. Map each to crop ids
  (`list_crops`); a group can mix up to four crops in alternating rows.

## 2. Pick a first arrangement from the shape

Start from the rules of thumb, then let the tool test them.

| Shape and wish | Try first |
|---|---|
| Trees round the edge, crops inside | `border` (3 to 6 m) + blocks in `strips` |
| Trees and crops together on one plot | `alley`: rows of trees every 12 to 20 m, crops between |
| Several crops on one big regular plot | blocks in `strips` by share |
| Pests that spread along one crop, mixed farming | `alternate` strips 6 to 15 m wide |
| Several small plots, one crop each | `by_plot` |
| Tall crops (sugarcane, maize, trees) with short ones | rows `north_south` |

## 3. Lay it out and read the effects

Call `distribute_plants` with the distribution. Go through `effects`,
warnings first, and explain each in the farmer's words with its number:

- **Shade** (`shade`): sampled hour by hour, 9 to 3, on a day of each
  month the shorter crop stands in the field (kharif June to October,
  rabi November to March, zaid March to May, perennials all year); the
  worst month counts and is named. In winter the sun is low in the south,
  so taller plants shade the next strip north. Inside the tropics (most of
  India) the summer and monsoon noon sun is north of overhead, so tall
  trees shade the strip to their south a little in kharif. North-south
  rows shade their neighbours only in the early morning and late
  afternoon. Fixes: move the short crop to the sunny side for its season,
  north-south rows, a gap of a shadow's length, or a crop that bears part
  shade (turmeric, ginger) under the trees; the effect says so for them.
- **Edges** (`edge.*`, `piece.*`): the land within 5 m of a boundary gets
  the neighbours' pests, stray cattle and tree roots. Narrow strips and
  thin plots are mostly edge. Pieces too narrow for three rows, or under
  20 m², are not worth their own crop.
- **Spacing** (`trees.fit`): a tree group's real count against open
  ground. Sharp corners and narrow strips waste room; wider pieces or a
  triangle (quincunx) pattern fit more.
- **Water** (`water.*`): the thirstiest group should be nearest the farm
  pond, canal or well, so channels are short; crops with very different
  needs in one group cannot share a watering.
- **Neighbours** (`next.*`, `mix.*`): crops of one family side by side or
  mixed share pests and diseases (tomato, brinjal, chilli are one family);
  known pairs help each other (pigeon pea with sorghum, mango with green
  gram).
- **Wind** (`wind.shelter`): a windbreak shelters about ten times its
  height downwind.
- **Layout** (`layout.*`): what the farm pond, shed or path takes from each
  group.

Give the totals that matter: each group's area and share (in the farmer's
units: `convert_area`), the number of trees, the plants of each crop.

## 4. Compare, then save

- When there is a real choice (long-side rows against north-south, a
  border against alleys, blocks against alternating strips), lay out both
  and compare in two or three numbers: shaded area, edge share, trees.
  Recommend one and say why.
- Change one thing at a time; say what changed.
- Save with `save_distribution` only when the farmer agrees, with the
  `raw` of the layout they chose.

The thresholds are starter values; the effects are a guide to where to
look, not a promise. Shade is for grown trees; young ones cast less for
their first years, when the alleys can carry more sun-loving crops.
