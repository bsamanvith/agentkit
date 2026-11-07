# Farm

An app for farmers, built on agentkit: plots with verified boundaries
first, then crops, money and agents that watch the fields. What and why:
[docs/PRODUCT.md](docs/PRODUCT.md); crop planning: [docs/CROP-PLANNING.md](docs/CROP-PLANNING.md); the land
(survey, layout, map tools, web research): [docs/LAND.md](docs/LAND.md);
plot groups and how plants are spread over them: [docs/GROUPS.md](docs/GROUPS.md);
the sun, the slope and the seasons: [docs/SUN.md](docs/SUN.md);
the plan: [E25](../../tasks/E25-farm-app.md).

```sh
make farm                     # http://127.0.0.1:8090, data in ./farm.db
make farm-web                 # rebuild the web client after changing web/apps/farm
pnpm --dir web --filter @agentkit/farm dev   # the client with hot reload, against make farm
```

The assistant runs on Claude when `ANTHROPIC_API_KEY` (or `ANTHROPIC_KEY`)
is set, else on Gemini with `GEMINI_API_KEY` (or `GEMINI_KEY`); `-model off`
turns it off. Plots are located with OpenStreetMap's Nominatim
(`-geocoder off` to skip it); `-country IN` is where plots are expected.
The climate at a plot comes from NASA POWER (`-climate off` to skip it:
then there are no recommendations, and plan checks skip sowing and water).
The land around a plot comes from OpenStreetMap (`-overpass`, `-osm-api`)
and its slope from Open-Meteo (`-elevation`); each can be `off`. The
handbook searches by meaning with Gemini embeddings when `GEMINI_API_KEY`
(or `GEMINI_KEY`) is set (`-embedder off` for keywords only; `-embed-rate`
paces a free-tier key; vectors are kept in `<db>-vectors.gob`). Farmers'
Exa and Parallel keys are sealed in `<db>-vault.db` with the key ring in
`FARM_CREDENTIALS_KEY` or a key file made on first start. The assistant's
tools are served over MCP at `/mcp/<toolset>` (`-mcp=false` to turn it
off). `make farm-eval` runs the assistant's scenarios on a real model.

| Package | What |
|---|---|
| [geo](geo) | Area, perimeter, shape checks of small polygons on WGS84 |
| [farm](farm) | Plots and plot groups, units, the verifier, the service, the store interfaces; `sqlite`, `farmtest` |
| [planting](planting) | Plant groups spread over a group's plots, and what that does: shade, edges, spacing, water, neighbours, wind |
| [nominatim](nominatim) | Where a point is (reverse geocoding), a `farm.Locator` |
| [agro](agro) | Crop planning rules: soil and crop libraries, the recommender, layouts, plan checks; `agrotest` |
| [nasapower](nasapower) | Climate normals at a point (NASA POWER), an `agro.ClimateSource` |
| [osm](osm) | Map features near a plot from OpenStreetMap (Overpass, then the OSM API), classified; `osmtest` |
| [elevation](elevation) | Ground heights (Open-Meteo, Copernicus GLO-90) |
| [land](land) | The land's shape, its survey (nearest roads, water, buildings, slope) and the farm layout with its checks |
| [surveyor](surveyor) | Surveys and layouts of the farms, point location, place search |
| [research](research) | Web search and extract on Exa or Parallel with the farmer's key in a vault; `researchtest` |
| [planner](planner) | Plans seasons for the farms: profile, recommend, lay out, check, save; distributions for plot groups |
| [handbook](handbook) | The assistant's knowledge base on farming and plants in India: chapters, a crop library chapter, keyword or hybrid search |
| [farmagent](farmagent) | The farm, planning and map tools for agents (also over MCP), the skills (`plan-season`, `check-plan`, `survey-land`, `shape-farm`, `distribute-plants`, `research-products`), and the assistant (agentkit harness) |
| [farmapi](farmapi) | The HTTP API under `/api/` |
| [webdist](webdist) | The web client's build, embedded |
| [cmd/farm](cmd/farm) | The binary |
| [web/apps/farm](../../web/apps/farm) | The web client (React, Vite, Leaflet) |
