# Farm

An app for farmers, built on agentkit: plots with verified boundaries
first, then crops, money and agents that watch the fields. What and why:
[docs/PRODUCT.md](docs/PRODUCT.md); crop planning: [docs/CROP-PLANNING.md](docs/CROP-PLANNING.md);
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

| Package | What |
|---|---|
| [geo](geo) | Area, perimeter, shape checks of small polygons on WGS84 |
| [farm](farm) | Plots, units, the verifier, the service, the store interface; `sqlite`, `farmtest` |
| [nominatim](nominatim) | Where a point is (reverse geocoding), a `farm.Locator` |
| [agro](agro) | Crop planning rules: soil and crop libraries, the recommender, layouts, plan checks; `agrotest` |
| [nasapower](nasapower) | Climate normals at a point (NASA POWER), an `agro.ClimateSource` |
| [planner](planner) | Plans seasons for the farms: profile, recommend, lay out, check, save |
| [handbook](handbook) | The assistant's starter knowledge base, Markdown chapters and sections |
| [farmagent](farmagent) | The farm and planning tools for agents, the `plan-season` and `check-plan` skills, and the assistant (agentkit harness) |
| [farmapi](farmapi) | The HTTP API under `/api/` |
| [webdist](webdist) | The web client's build, embedded |
| [cmd/farm](cmd/farm) | The binary |
| [web/apps/farm](../../web/apps/farm) | The web client (React, Vite, Leaflet) |
