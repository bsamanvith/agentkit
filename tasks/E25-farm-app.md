---
id: E25
title: "Farm app: a farmer's land, crops and money, with agents on top"
status: in-progress
---

# E25: farm app

Owner request (2026-10-31): a second app in this repository, built on
agentkit, for farmers: a Go backend and a web frontend. Start from the
farmer's lifecycle and product ideas, then let farmers define their plots
(boundary and location, by GPS or coordinates) and verify them. The product
notes are in [apps/farm/docs/PRODUCT.md](../apps/farm/docs/PRODUCT.md).

## Decisions

| Question | Decision |
|---|---|
| Where | Go in `apps/farm/` (this module, so it imports agentkit packages directly); the web client in the pnpm workspace as `web/apps/farm`, as [PRODUCT.md](../docs/PRODUCT.md) plans for consumer apps, on `@agentkit/ui`. Its build is embedded in the `farm` binary like the console's. |
| Frontend | React + TypeScript + Vite (the console's stack, shared components and tooling), Leaflet for the map. Not Next.js (wants a Node server; the server is Go) nor SvelteKit (a second ecosystem). |
| Geometry | Our own, dependency-free (`apps/farm/geo`): an equal-area projection of the WGS84 ellipsoid, checked against GeographicLib. |
| Agents | The app's numbers come from Go; agents get them as agentkit tools (`apps/farm/farmagent`). |
| Accounts | One workspace first; farmers' accounts with `pkg/auth` later. |
| Land and layout | OpenStreetMap features (Overpass, then the OSM API) and Copernicus heights (Open-Meteo); rules suggest and check the layout; the app draws its own picture (SVG), not map tiles. |
| Web research | The farmer's own Exa or Parallel key, sealed in a `pkg/credentials` vault; no shared keys. |
| Crop planning | Rules first (`apps/farm/agro`, deterministic, every score with its reasons); the agent gathers inputs and explains. Crops from abroad are scored on climate, flagged as analogs. Climate from NASA POWER; soil from the farmer until SoilGrids is reliable. |

## Tasks

- [ ] [T133](T133-farm-boundaries.md) Plots: boundary by map, GPS walk or coordinates; verification; the farm assistant
- [ ] [T134](T134-crop-planning.md) Crop planning: soil and crop libraries, climate, what to plant and when, plot layouts, plan checks, skills, the handbook ([design](../apps/farm/docs/CROP-PLANNING.md))
- [ ] [T135](T135-farm-land.md) The land: shape and surroundings from OpenStreetMap and elevation, the animated land view, farm layout (gate, path, shed, pond, border trees), map tools over MCP, web research with the farmer's own key, the plants knowledge base ([design](../apps/farm/docs/LAND.md))
- [ ] [T136](T136-plot-groups.md) Plot groups, plant groups spread over them with their effects (shade, edges, spacing, water, neighbours, wind), the distribute-plants skill, a responsive layout UX ([design](../apps/farm/docs/GROUPS.md))
- [ ] [T137](T137-sun-and-seasons.md) The sun, the slope and the seasons: sunlight, day length and the noon sun in the recommender and in shade, the slope's light, crop research month by month, the Sun and seasons tab ([design](../apps/farm/docs/SUN.md))
- [ ] [T138](T138-backup-elevation.md) A backup elevation source (Open Topo Data, SRTM 30 m) when Open-Meteo cannot answer, and heights cached in a file
- [ ] Field watch: satellite greenness and weather per plot, a weekly background agent (to plan)
- [ ] Land-cover cross-check: is the plot cropland (ESA WorldCover / Dynamic World) (to plan)
- [ ] Farmers' accounts and offline (PWA) capture (to plan)
