# Farm: product notes

> First pass, 2026-10-31. The farmer's year, where software helps, where
> agentkit agents help, and what we build first. The app lives in
> [apps/farm](..) (Go backend) and [web/apps/farm](../../../web/apps/farm)
> (the web app). Epic: [E25](../../../tasks/E25-farm-app.md).

## Who it is for

A smallholder or mid-size farmer, first in India (Telangana, Andhra Pradesh,
Karnataka, Maharashtra): 1 to 20 acres, often in two or three separate
plots, an Android phone with patchy data, comfortable with WhatsApp and
voice, often more comfortable in Telugu, Hindi, Kannada or Marathi than in
English. Land is talked about in acres, guntas (40 to an acre) and cents
(100 to an acre), not hectares. Close behind: the people around the farmer
who would use the same data: a farmer producer organisation (FPO), an input
dealer, an agronomist, a lender or an insurer.

## The farmer's year

| Stage | What happens | What the farmer needs |
|---|---|---|
| **1. Land** | Plots, ownership papers (pattadar passbook, RoR / 1-B, 7/12 extract), survey numbers, water source (borewell, canal, rainfed), soil (Soil Health Card) | One true record of each plot: where it is, how big, what it has. Everything later hangs off it |
| **2. Plan the season** | Kharif (June to October), Rabi (November to March), summer. Choose crops from soil, water, last season, prices, the monsoon outlook. Budget seed, fertiliser, labour. Crop loan (Kisan Credit Card), crop insurance (PMFBY) before its deadline | A crop plan per plot with a budget, and the deadlines that matter |
| **3. Prepare and sow** | Ploughing, buying seed and inputs, sowing in the right window after the rains set in | When to sow, how much seed, where to buy; a record of what was sown when |
| **4. Grow** | Irrigation, fertiliser in splits, weeding, pests and diseases, sprays timed against rain | Advice for this crop on this plot this week; "what is wrong with my plant"; alerts before it is too late |
| **5. Harvest** | Timing, labour and machines (harvester, thresher) to book | When the crop is ready, who has a machine free |
| **6. Sell** | Store or sell now, which mandi, at what price, transport, or sell through the FPO | Prices nearby today and the trend; a sell / hold call with the reasons |
| **7. Money and records** | Expenses, yields, profit per plot, loan repayments, PM-KISAN instalments, insurance claims after crop loss | A ledger that writes itself; evidence (geo-tagged photos, dates) for claims and loans |
| **8. Again** | Next season starts from what this one taught: soil, rotation, what paid | Plot history, season over season |

## Product ideas

Ordered roughly by how much they depend on the land record from stage 1.

1. **My land** (stage 1, the first feature): each plot's boundary, area and
   location, checked. The base for everything else.
2. **Field watch** (stage 4): weekly satellite greenness (Sentinel-2 NDVI,
   free, 10 m) and weather for each plot; an alert when a plot browns
   faster than its neighbours or heavy rain is due after a spray.
3. **Crop doctor** (stage 4): photo of a leaf, the likely problem, what to
   do, in the farmer's language; escalates to a human agronomist when unsure.
4. **Season planner** (stage 2): crops for this plot from its soil, water
   and history, with a budget and a calendar of tasks.
5. **Sell smart** (stage 6): mandi prices nearby (Agmarknet, eNAM), the
   trend, transport cost, a sell / hold recommendation.
6. **Ledger by voice** (stage 7): "spent 2,000 on urea for the east plot"
   becomes an entry; profit per plot and season.
7. **Schemes and paperwork** (stages 2 and 7): which schemes a farmer
   qualifies for (PM-KISAN, PMFBY, state schemes), documents needed,
   deadlines; drafts the insurance claim with geo-tagged evidence.
8. **For FPOs and lenders** (later): many farmers' verified plots in one
   view; crop area and expected yield across members; loan and insurance
   checks against a verified boundary.

## Where agentkit agents help

The app's deterministic parts (geometry, ledgers, schedules) are plain Go.
Agents sit on top, with those parts as their tools:

| Agent | agentkit pieces | Tools |
|---|---|---|
| **Farm assistant** (chat and voice, local language) | harness, Claude or Gemini (`pkg/llm`), memory per farmer (E23), skills | the farm tools: `list_farms`, `get_farm`, `verify_boundary`, `convert_area`; later weather, prices, ledger |
| **Field watch** (background, weekly) | triggers and schedules (E14), background runs, the runner | satellite and weather connectors; writes alerts |
| **Crop doctor** | image input to the model, knowledge base (T112) with the package of practices per crop (ICAR, state agriculture universities) | knowledge search, `get_farm` for crop and stage |
| **Paperwork** | supervised tasks (T084) and tool policy (E17): nothing is submitted or paid without the farmer's approval | form drafting, document checklist |
| **Market** | connectors (MCP) to price APIs | prices, distance to mandis |

Agents never own the numbers: an area, a price or a balance comes from a tool,
so the answer is the same however the question is asked.

## First feature: my land

A farmer adds a plot: a name, how big they believe it is (acres and guntas,
cents, hectares, ...) and its boundary, by one of:

- **Draw on the map**: tap the corners on a satellite map.
- **Walk it**: stand at each corner and tap "add my position" (phone GPS,
  with its accuracy kept per point).
- **Type or paste coordinates**: one corner per line, decimal degrees
  (`17.3850, 78.4867`); handy for a boundary from a survey sketch or another
  app.

Then it is **verified**, and the plot is saved with its report:

| Check | Result |
|---|---|
| Valid shape | at least 3 distinct corners, coordinates in range, no repeated corners, edges that do not cross |
| Area and perimeter | on the earth's surface (geodesic), shown in m², hectares, acres, acres + guntas, cents |
| Matches the declared size | within 10 %: fine; 10 to 25 %: warning; more: likely a wrong corner or the wrong unit |
| Plausible | not a speck (under 50 m²), not a district (over 1,000 ha: warning), not a hair-thin sliver, no needle spikes from a GPS jump |
| GPS accuracy | if walked: points whose accuracy is poor for a plot this size are flagged |
| Where it is | the centre is reverse geocoded (OpenStreetMap Nominatim): village, district, state, country; a plot in the sea or outside the expected country is flagged |
| Overlaps | a plot that overlaps another plot of the same farmer is flagged |

The report's status is `verified` (no warnings), `needs_review` (warnings the
farmer can accept) or `invalid` (cannot be saved).

Not yet, and why:

- **Satellite land-cover cross-check** (is this cropland, not a building or a
  lake?): ESA WorldCover or Dynamic World; needs a tile service. Next
  candidate, with Field watch.
- **Match the land records** (survey number area from the state's RoR
  portal): no open API; later, from a photo of the passbook through the
  model.
- **Offline**: walk the boundary without data and sync later (PWA). Needed
  before field use at scale.
- **Sign-in and farmers' own accounts**: the first version is one
  workspace; accounts come with the agentkit auth (`pkg/auth`).

## Stack

- **Backend: Go**, in this repository's module, built on agentkit packages:
  `pkg/tool` for the farm tools, `pkg/harness` and `pkg/llm` for the
  assistant. Geometry and verification are dependency-free Go.
- **Frontend: React + TypeScript + Vite** in the pnpm workspace
  (`web/apps/farm`), on the shared `@agentkit/ui` components, with
  **Leaflet** for the map (about 40 KB gzipped, works on low-end phones,
  free OpenStreetMap and Esri satellite tiles). The build is embedded in the
  Go binary, so the app ships as one binary.
  - Why not Next.js: it wants a Node server, and our server is Go; its
    strengths (SSR, SEO) matter little for a signed-in app.
  - Why not SvelteKit: smaller bundles, but a second ecosystem beside the
    console and no reuse of `@agentkit/ui` and `@agentkit/agent-ui`.
  - Why not React Native now: the web app as a PWA reaches every phone
    with no store; React keeps the door open to React Native later.
