# The land: shape, surroundings, layout, and research

The owner's third brief for the farm app (2026-11-01, [T135](../../../tasks/T135-farm-land.md)):
from a saved plot boundary, work out the land's shape and what is near it
(roads, water, buildings, villages, power lines); draw the plot in the
app's own animated picture, with the crop zones growing through the
season; let agents ask map questions and shape the farm (gate, path,
shed, pond, border trees) through agentkit tools, also served over MCP;
give the assistant web research (Exa or Parallel, the farmer's own key)
and a knowledge base on plants in India that can be searched and walked as
a tree.

As with crop planning, every number comes from Go and every suggestion
comes with its reason; the agent gathers what the farmer wants, calls the
tools and explains.

## Packages

| Package | What |
|---|---|
| [geo](../geo) | `near.go`: bearings, compass words, offsets, rectangles, distance from a plot to a line or area (`ClosestTo`). `shape.go`: convex hull, the minimum-area bounding box, inset (a band inside the boundary), ring-in-ring, how far a ray reaches inside a plot |
| [osm](../osm) | Map features near a point from OpenStreetMap: Overpass, then the OSM API for small boxes (Overpass mirrors are often busy), at most one request a second, cached a day. Tags are sorted into 18 kinds (road, canal, drain, building, settlement, power line, well, ...). `osmtest` holds a recording around a paddy plot by Kothapeta, Andhra Pradesh |
| [elevation](../elevation) | Ground heights from Open-Meteo (Copernicus GLO-90), or Open Topo Data (SRTM 30 m) when Open-Meteo cannot answer; one survey's heights all come from one model. Cached per service and point in `<db>-heights.json`; one retry on a network error or 5xx; a 429 pauses that service (Retry-After, else an hour) |
| [land](../land) | `Describe` (shape: square, rectangle, long strip, triangle, trapezium or irregular; edges and which way each faces); `Analyse` (the survey: nearest of each kind, what is within 400 m, slope, notes); `Suggest` and `CheckDesign` (the layout and its checks) |
| [surveyor](../surveyor) | Joins farms to the map, heights and place search; saves surveys (not one with missing map data, so the next call tries again) and layouts (not one with errors); places a point among the plots; finds a place by name |
| [research](../research) | `web_search` and `web_extract` on Exa or Parallel (`connectors/exa`, `connectors/parallel`) with the farmer's key sealed in a `pkg/credentials` vault |
| [handbook](../handbook) | The knowledge base: hand-written chapters plus a crop library chapter made from the crop data; keyword search, or hybrid with Gemini embeddings (`Vectors` keeps them in a file and paces a free-tier key) |
| [farmagent](../farmagent) | The map tools (`maptools.go`), the skills `survey-land`, `shape-farm` and `research-products`, `Toolsets` (for MCP) |
| [web/apps/farm](../../../web/apps/farm) | The land view (`landpicture.tsx`, `landview.ts`), surroundings and shaping panels (`land.tsx`), web research settings (`research.tsx`) |

## The survey

`land.Analyse` measures the plot and every map feature against its
boundary: the shortest distance (0 when touching, crossing or around the
plot), the direction from the plot, and the side of the plot it faces.
The survey keeps the nearest feature of each kind however far (the map is
read for at least 400 m around, more for a big plot), everything within
400 m, counts by kind, and the features to draw (at most 500, nearest
first, plus the nearest of each kind). Heights at the corners and the
centre give the fall, the slope and the downhill direction; Copernicus
GLO-90 is a 90 m model (the SRTM backup 30 m), so a small plot's slope is
a hint, and the note says so with the model's grid.

Notes come from rules: road frontage or the nearest road and its class, a
busy road, canals, drains, ponds and water crossing the plot, power lines
over or near it, houses inside or close, the village, a railway, slope
and flatness, and missing map data.

## The layout

`land.Suggest` lays out what the farmer asks for:

- **Gate**: on the boundary where the plot is nearest a road (or the
  middle of the longest side), kept off the corners.
- **Cart path**: from the gate straight across, square to that side,
  3 m wide by default.
- **Shed**: just inside the gate beside the path, 6 x 4 m by default.
- **Farm pond**: a square, 10 to 30 m a side by the plot's size, in the
  lowest corner by the heights; else the corner nearest a drain or water;
  else the corner farthest from the gate. 3 m deep by default; its volume
  is reported.
- **Border trees**: a band inside the boundary (3 m), with trees every
  4 m along it.

`land.CheckDesign` checks any layout, the agent's or the farmer's:
elements outside the plot or overlapping, the pond too close to the
boundary, too big a share of the plot, under a power line, near houses,
not at the low end; the shed far from the gate; the gate away from the
road side or off the boundary; a border on a narrow plot; trees under a
power line; too little left to crop; the pond inside a crop zone of the
saved plan. A layout with errors is not saved.

## Agents

| Tool | Kind | What |
|---|---|---|
| `survey_land` | read | The survey of a plot: shape, slope, nearest of each kind, notes |
| `nearby_features` | read | Features near a plot, by kind and distance; with kinds and no distance, the nearest of each kind however far |
| `locate_point` | read | Which plot a point is in, and how far and which way it is from each |
| `measure` | read | Distances, directions and the area of points |
| `find_place` | read | A place by name (Nominatim), with its distance from a plot |
| `suggest_design` | read | A layout for a plot and its checks |
| `check_design` | read | The checks of any layout |
| `save_design` | write | Save a layout that has no errors |
| `web_search`, `web_extract` | read | The web, through the farmer's Exa or Parallel key |
| `knowledge_search`, `knowledge_outline`, `knowledge_read` | read | The handbook |

The binary serves the tools over MCP at `/mcp/<toolset>` (`farm`,
`farm-map`, `farm-research`, `farm-handbook`), so an agentkit agent
elsewhere can use them as an `mcp` toolset; `-mcp=false` turns it off.
`farmagent_test.TestToolsetsOverMCP` serves them and calls them through
agentkit's MCP client.

## Web research keys

The farmer pastes an Exa or Parallel key in the app (Web research). It is
sealed in a `pkg/credentials` vault (`<db>-vault.db`, key ring from
`FARM_CREDENTIALS_KEY` or a key file made on first start, mode 0600) and
never sent back. Without a key the tools say how to connect one and the
assistant does not make prices up (the `research without a key` eval).

## Tests

- Unit and integration: `go test ./apps/farm/...` (recorded map data,
  fake heights, a fake search API), `pnpm --dir web --filter @agentkit/farm test`.
- Real model: `make farm-eval` (Claude; `FARM_EVAL_MODEL=gemini` for
  Gemini): proximity, a saved layout with pond and shed, a Telugu
  question about the canal, web research with and without a key, border
  trees from the handbook.
