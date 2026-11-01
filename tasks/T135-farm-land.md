---
id: T135
title: The land: shape and surroundings, the animated land view, farm layout, map tools over MCP, web research, the plants knowledge base
epic: E25
status: review
depends_on: [T133, T134]
---

# T135: the land

## Context

The owner's third brief for the farm app ([E25](E25-farm-app.md),
2026-11-01): from the saved boundary, the shape of the land and how near
it is to roads, water, buildings and other structures; the plot in the
app's own animated picture, with the fields animated; agentkit tools for
map and lat/long questions and for shaping the farm, made available as an
MCP server, with skills for how the agent behaves. Then: the agent
analyses and suggests; the farmer researches products across India and
abroad through the web (Exa or Parallel, the farmer's own key in the
vault, no shared keys); a knowledge base on plants in India, searched by
meaning and walked as a tree. Test it all, on real models too. Design:
[LAND.md](../apps/farm/docs/LAND.md).

## Scope

- `apps/farm/geo`: bearings, offsets, closest distance from a plot to a
  shape, convex hull, minimum bounding box, inset, ring-in-ring, reach.
- `apps/farm/osm`: features from Overpass then the OSM API, classified
  into 18 kinds, rate limited and cached; `osmtest` (a Kothapeta
  recording).
- `apps/farm/elevation`: Open-Meteo heights, cached, one retry.
- `apps/farm/land`: shape description, the survey and its notes, layout
  suggestion and checks.
- `apps/farm/surveyor`: surveys and layouts saved on the farm, point
  location, place search (`nominatim.Search`).
- `apps/farm/research`: `web_search` and `web_extract` on Exa or
  Parallel, keys in a `pkg/credentials` vault; `researchtest`.
- `apps/farm/handbook`: the crop library chapter, four new chapters
  (trees and borders, farm layout and ponds, fodder and green manure,
  agro-climatic regions), hybrid search with Gemini embeddings and a
  vectors file paced for a free-tier key.
- `apps/farm/farmagent`: eight map tools, three skills, `Toolsets` for
  MCP; `cmd/farm` serves them at `/mcp/`.
- `apps/farm/farmapi`: survey, layout suggest/check/save/clear, locate,
  places, research status/connect/disconnect.
- `web/apps/farm`: the land view (plot draws in, surroundings fade in,
  water flows, buildings and shed rise when tilted, crop rows grow through
  the season, the layout appears piece by piece, callouts to the nearest
  features), the surroundings card, "Shape the farm", web research
  settings; layout shapes on the satellite map.

## Out of scope

- Field watch, land-cover checks (later E25 tasks); drawing a layout by
  hand in the app (the agent and the API take any layout; the app
  suggests); contour bunds and drainage lines from a finer elevation
  model.

## Acceptance criteria

- [x] Geometry: bearings, closest distance, hull, minimum box, inset, reach — test: `geo_test` (`near_test.go`)
- [x] OSM: both answer formats, classification, the fallback chain names the source that answered, rate limit, cache — test: `osm_test`
- [x] Elevation: batches, cache, failures, one retry — test: `elevation_test`
- [x] Shape kinds and edges — test: `land_test.TestDescribe`
- [x] The Kothapeta survey: square, drain 98 m E, road 208 m NW, canal 594 m W (drawn), notes; deterministic — test: `land_test.TestAnalyseKothapeta`
- [x] Survey notes from slope, water, power lines, houses; missing data — test: `land_test.TestAnalyseRules`
- [x] Suggested layouts are sound, inside the plot, pond at the low corner; every check fires — test: `land_test.TestSuggestKothapeta`, `TestSuggestUsesSlopeAndRoad`, `TestSuggestRefuses`, `TestCheckDesignFindsMistakes`
- [x] The surveyor saves surveys and layouts, not failed or invalid ones; locate and place search — test: `surveyor_test`
- [x] Map tools, kinds and flow; "how far is the canal" finds it past 400 m — test: `farmagent_test.TestMapToolKinds`, `TestMapToolsFlow`
- [x] The toolsets served over MCP and called through agentkit's MCP client — test: `farmagent_test.TestToolsetsOverMCP`
- [x] The assistant with the map tools and the shape-farm skill — test: `farmagent_test.TestAssistantShapesTheFarm`
- [x] Web research: no key no request; Exa first, then Parallel; keys sealed, key file private — test: `research_test`
- [x] The handbook: crop library chapter, new chapters found, hybrid search; vectors file paced and remembered — test: `handbook_test`
- [x] API routes and their errors — test: `farmapi_test.TestMapRoutes`, `TestResearchRoutes`, `TestMapRoutesNotConfigured`
- [x] The land view, surroundings, shaping and research settings — test: `web/apps/farm/src/landview.test.ts`, `land.test.tsx`
- [x] On Claude: proximity, a saved layout, Telugu, research with and without a key, the handbook — eval: `make farm-eval` (6 of 6)

## Notes

- Ran end to end in a cloud session (2026-11-01) on real OpenStreetMap
  (the OSM API; Overpass mirrors refused connections from there) and
  Open-Meteo heights for a plot by Kothapeta: drain 98 m E, residential
  road 208 m NW, inside Kothapeta Town, falling 5 m to the south-east; the
  layout put the pond in the south-east corner (675 m³). Screenshots in
  the project files (`farm/m1` to `m11`, and a walkthrough video). The
  assistant, asked in the app how far the plot is from road and water and
  where to dig a pond, called survey_land and suggest_design and answered
  with the numbers.
- Gemini's free tier allows 100 embedded texts a minute; the handbook has
  148 passages, so the first start takes about a minute and later starts
  read `<db>-vectors.gob`.
- The first Claude eval run missed the canal in Telugu (nearby_features
  stopped at 400 m); nearby_features now gives the nearest of the asked
  kinds however far.
