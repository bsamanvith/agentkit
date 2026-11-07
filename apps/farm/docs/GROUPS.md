# Plot groups and how plants are spread

A farmer keeps plots in groups (the home farm, the fields by the river)
and decides, per group, which **plant groups** grow there and how they
are spread over the plots: the **distribution**. The app lays it out to
scale and says what it does. Task: [T136](../../../tasks/T136-plot-groups.md).

## The model

| Thing | What |
|---|---|
| Plot group (`farm.Group`) | A name and its plots. A plot is in one group at a time; deleting a plot takes it out. Many groups. |
| Plant group (`planting.PlantGroup`) | A name and one to four crops (mixed in alternating rows). Its **place**: a *block* (a share of the land), the *border* (a band of `width_m` round each plot's edge) or *tree rows* (bands `width_m` wide every `every_m`, crops between: alley cropping). |
| Distribution (`planting.Distribution`) | The plant groups, how blocks are cut (`strips` by share, `alternate` strips of `strip_m`, `by_plot`: whole plots, largest first) and which way rows run (`long_side`, `north_south`, `east_west`). Saved on the group. |

Stores that keep groups implement `farm.GroupStore` and pass
`farmtest.RunGroupStore`; the service finds it on the store, as the other
optional store interfaces.

## Laying it out

`planting.Distribute(library, plots, distribution)` is pure: the same
plots and distribution give the same result. Per plot it takes the border
band off (`geo.Inset`), cuts tree-row bands across the rows, then cuts the
rest into blocks by share or into alternating strips along the row
direction (`geo.StripsAlong`, `geo.BandsAlong`). Tree groups get trees
placed at their crop's spacing; a band narrower than the spacing gets one
row down its middle. The plot's saved layout (pond, shed, path) is taken
out of what it covers.

Each piece is sampled on a grid in a local plane (about 1,500 points) for
its area, its edge share and its shade.

## The effects

Every effect has an id, a kind, a severity (warning, info) and a message
in the farmer's words with its number.

| Kind | How | Ids |
|---|---|---|
| Shade | The sun hour by hour from 9 to 3 on a representative day of each month the shorter crop stands in the field (its seasons' months; all twelve for a perennial); the worst month counts and is named ([SUN.md](SUN.md)). A point is shaded when, for 4 of the 7 hours, a taller plant group's piece lies between it and the sun within its shadow's reach (height ÷ tan(sun's altitude)). Heights are of grown plants (`height_m` in the crop library). A crop that bears part shade (turmeric, ginger, cocoa) gets a note, not a warning. | `shade` |
| Edges | Share of a piece within 5 m of the plot's edge; pieces too narrow for three rows or under 20 m². | `edge.most`, `edge.some`, `piece.narrow`, `piece.small` |
| Spacing | A tree group's placed trees against the open ground's ideal count. | `trees.fit`, `trees.none` |
| Water | Crops with very different water needs in one group; the thirstiest group's distance to the pond or water on the map. | `water.mixed`, `water.far`, `water.near` |
| Neighbours | Same family side by side or mixed (shared pests); known good pairs; alternating strips against pests. | `next.family`, `next.good`, `mix.family`, `mix.good`, `mix.unknown`, `alternate.pests` |
| Wind | A tall border shelters about ten times its height downwind. | `wind.shelter` |
| Layout | A border already in the plot's layout; what the pond, shed or path takes. | `layout.border`, `layout.takes` |

The result's status is `sound`, `needs_review` (a warning) or `invalid`
(a group got no land: `by_plot.no_land`, `border.no_fit`). The thresholds
are starter values.

## The agent

Tools: `list_groups`, `save_group` (write), `distribute_plants` (read: lay
out without saving, with the pieces' centres and tree counts) and
`save_distribution` (write). The `distribute-plants` skill: know the
group's shapes, start from the rules of thumb, lay out and read every
effect with its number, compare two arrangements in two or three numbers,
save only when the farmer agrees.

## The app

- A sidebar of groups with their plots and the plots in no group; a drawer
  on phones.
- The overview: totals, a card per group with its plots' shapes and plant
  groups, the plots in no group.
- The group page: its plots; a to-scale plan, north up, with each piece in
  its plant group's colour, rows hatched the way they run, trees as
  crowns and the midday sun's side marked, or the satellite map; templates
  to start from (tree border with crops inside, tree rows with crops
  between, alternating strips, a crop per plot); an editor whose every
  change is laid out again after a moment; the effects, warnings first;
  each group's numbers.
- Plot pages have tabs (map, land and layout, season plan) and a link back
  to their group.

## API

| Route | What |
|---|---|
| `GET/POST /api/groups` | List, create |
| `GET/PUT/DELETE /api/groups/{id}` | One group; rename and set its plots; delete (plots stay) |
| `GET /api/groups/{id}/distribution` | The saved distribution laid out |
| `POST /api/groups/{id}/distribution/preview` | Lay one out without saving |
| `PUT/DELETE /api/groups/{id}/distribution` | Save, clear |

A bad distribution is 400 with the reason; a store without groups is 503.
