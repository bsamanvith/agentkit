# Crop planning: what to plant, when, and where on the plot

> Design, 2026-10-31, from the owner's brief. Builds on the verified plot
> ([PRODUCT.md](PRODUCT.md#first-feature-my-land)). Epic:
> [E25](../../../tasks/E25-farm-app.md).

## The farmer's problem

A farmer has a plot. Before every season they need to answer:

1. **What can grow here?** That depends on the soil, the water (rain,
   a canal, a river, a borewell), and the climate month by month.
2. **When should I sow?** The next good window from today, and the window
   for a crop they already have in mind. The dates they give us are
   approximate ("after Dasara", "in June"), so we treat them as roughly
   right, not as exact.
3. **Where on the plot does each crop go?** One crop, or a mix: maize with
   soybean, pigeon pea with groundnut, a mango orchard with vegetables
   between the young trees. Mixed farming needs zones, spacing, and plant
   counts that make sense.
4. **Is my plan sound?** Sowing after the window closes, a thirsty crop on
   rain alone, a crop that rots in waterlogged clay. These are mistakes
   rules can catch before they cost a season.

Today the farmer answers these from habit, neighbours and the input
dealer. We want the answers to come from their own plot's data, explained
in their language, with the reasoning shown.

## Principles

- **Rules first, models on top.** Suitability, sowing windows, water
  balance, spacing and plan checks are deterministic Go: the same inputs
  always give the same answer, and every answer carries its reasons. The
  agent gathers inputs from the farmer, calls the rules, and explains the
  results. It never invents a number (we learned that in T133).
- **Ask when we cannot tell.** Climate we look up. Soil we try to look up
  (SoilGrids) and otherwise ask, showing the farmer our soil library with
  the signs they can see: colour, cracks when dry, sticky when wet. Water
  sources we ask.
- **Not only what the region always grows.** A crop is scored on whether
  this plot's climate and soil suit it, not on whether it is traditional.
  Dragon fruit (Mexico), avocado (Mexico, Florida), quinoa (the Andes) and
  chia (Mexico) are candidates where the climate matches. They are marked
  as *climate analogs*, with less local experience behind them.
- **Fuzzy time.** "Around" a date means a window (±45 days by default). We
  give the next good window and how confident we are in it, not one day.

## Inputs, and where they come from

| Input | Source | State |
|---|---|---|
| Plot shape, area, centroid | T133 | Done |
| Monthly climate normals: temperature (mean, max, min), rain, humidity | NASA POWER climatology API (free, no key, any point on Earth) | Reachable from the cloud session |
| Weather forecast, 16 days | Open-Meteo (free, no key) | Reachable |
| Soil at the point: texture (sand, clay), pH, organic carbon | ISRIC SoilGrids REST API (CC BY 4.0) | Often down (503 today), so optional |
| Soil, as the farmer knows it | Our soil library (below) | Built here |
| Water sources: rainfed, canal, river, tank, borewell, drip | The farmer; later rivers and canals near the plot from OpenStreetMap (Overpass) | Overpass unreachable today, so the farmer tells us first |
| Previous crop | The farmer, then our own plan history | Built here |
| Crop requirements | Our crop library (below), seeded from FAO ECOCROP ranges and Indian packages of practices | Built here, ~35 crops |

## Data structures

**Soil library.** About 12 soil types that farmers and soil surveys name:
black (regur), red sandy, red loam, laterite, alluvial loam, alluvial clay
(deltas), desert sand, saline and alkaline (usar), coastal sand, hill and
forest, peaty and marshy. For each: local names, how to recognise it,
texture class, pH range, water holding, drainage, fertility, where it is
found, and what it suits.

**Crop library.** For each crop: names (English, Hindi, Telugu), family
(for rotation), kind, perennial or not, days to harvest, the monthly mean
temperature it needs (optimal and absolute), water need per season (rain
plus irrigation), pH range, soil textures, tolerance of frost,
waterlogging, salt and drought, spacing between rows and plants, Indian
seasons, where it is grown, and whether it counts as a climate analog for
India.

**Climate.** Twelve months of normals for the plot's centroid.

**Recommendation.** For each crop, every sowing month in the next twelve
is scored by simulating its season over the climate: temperature month by
month, rain against need, how the water sources can cover the gap,
waterlogging risk, and the soil match. Each crop keeps its best window
inside the farmer's "around" range and its next good window after today,
with a score from 0 to 100, reasons for, risks, and the water gap in mm.

**Plan and layout.** A plan is a season of a plot:

```
Plan
  season, sowing around (date, fuzzy)
  zones[]
    crop, polygon (inside the plot) or a share of it
    row spacing, plant spacing, row direction
    sow date
  derived: zone area, rows, plant count, seed needed
```

A plot's polygon turns into zones by **strip splitting**: cut the plot
along its long axis into strips whose areas match the shares the farmer
chose ("60% cotton, 40% green gram"). Strips are easy to walk and
plough. Intercrops (alternating rows in one zone) are a zone with a row
pattern ("4 rows soybean : 1 row maize").

**Plan checks** (the validator, rule-based):

- Zones are inside the plot, do not overlap, and do not exceed it.
- Spacing is within the crop's range, and plant counts are sane.
- The sow date is in a good window for the crop at this plot.
- Water: the season's gap is covered by the sources, or it is a warning.
- Soil: pH and texture suit the crop; waterlogging-prone soil gets a
  warning for crops that do not tolerate it.
- Rotation: the same family as last season on the same land is a warning
  (pests, nutrients). A pulse after a cereal is noted as good.
- Mixed farming: known good intercrops are noted; tall crops on the sunny
  side of short ones are a warning; rows should run north to south.

## Agent skills

Two agentkit skills (`pkg/skill`, SKILL.md) carry the know-how. The tools
carry the numbers.

- **plan-season**: gather the missing inputs (soil, water, previous crop,
  roughly when), call `recommend_crops`, present three to five options in
  the farmer's language with the window and the main risk of each, then
  turn the choice into a plan with `suggest_layout`.
- **check-plan**: run `validate_plan`, explain each problem, propose the
  smallest change that fixes it, and check again.

## Knowledge: search plus a tree

Two ways to find things, over the same documents:

- **Search** (BM25 now; embeddings when a key is set) with `pkg/knowledge`
  (T112): passages with the heading path they sit under.
- **A tree** to explore: every document is split along its headings into
  chapters, sections and concepts; `knowledge_outline` shows the tree (the
  whole base, or one document), and reading a section returns just that
  section. An agent can walk from "Pulses" to "Pigeon pea" to "Sowing"
  instead of hoping a search hits. The backbone for concepts is FAO's
  AGROVOC thesaurus: about 40,000 concepts in broader and narrower terms,
  in 40+ languages including Hindi and Telugu, CC BY 4.0.

We start with a hand-written starter base (soils of India, crop sheets,
seasons, intercropping). It is short and checked, and comes with its
sources. Licensed sources come next.

### Sources to take in (survey)

| Source | What | Licence | Use |
|---|---|---|---|
| FAO ECOCROP | Temperature, rain, pH, soil and duration ranges for ~2,500 crops worldwide | FAO data, free use with attribution | Extend the crop library; the climate-analog search across all of it |
| FAO GAEZ v4 | Suitability and attainable yield maps for ~50 crops | CC BY 4.0 (most layers) | Cross-check our scores at the plot |
| FAO Irrigation and Drainage Paper 56 | Crop coefficients (Kc) and crop water need method | FAO, free with attribution | Better water gap: ET0 × Kc per growth stage |
| AGROVOC | Agricultural concept hierarchy, multilingual | CC BY 4.0 | The tree's backbone, local-language names |
| SoilGrids / HWSD v2 | Soil properties at any point | CC BY 4.0 | Soil without asking, when reachable |
| ICRISAT, IRRI (CGIAR, CGSpace) | Dryland crops, millets, pulses, rice: practice guides | Mostly CC BY | Crop sheets for the Deccan; ICRISAT is next to Hyderabad |
| IRRI Rice Knowledge Bank | Rice practice, step by step | Open | Rice sheets |
| TNAU Agritech portal, ICAR packages of practices, PJTSAU Vyavasaya Panchangam | Crop-by-crop practice for Indian states | Public sector; reuse terms vary, so we ask before bulk copying | Link and summarise; bulk ingest after permission |
| ICAR e-Courses (B.Sc. Agriculture lecture notes) | Agronomy, soil science, horticulture, organised course → lecture | Free to read; licence unclear | The best textbook-shaped tree for India; ingest only with permission |
| data.gov.in Kisan Call Centre queries | Millions of real farmer questions and answers, by state and crop | Open Government Data Licence India | Evals: real questions to test the assistant on |
| UF/IFAS EDIS (Florida), INIFAP (Mexico) | Extension guides for tropical and subtropical crops (avocado, dragon fruit, mango) | EDIS mostly open; INIFAP public | Climate-analog crop sheets |
| Chinese Academy of Agricultural Sciences publications, FAO China country material | Intercropping, rice and wheat systems | Varies | Later, through summaries |
| Textbooks (Reddy & Reddi *Principles of Agronomy*, Yawalkar, ...) | Classic Indian agronomy | Copyrighted | Not ingested without a licence; cited by chapter only |

## What we build now, and later

Now ([T134](../../../tasks/T134-crop-planning.md), built 2026-10-31):

- The soil and crop libraries, NASA POWER climate, the recommender
  with sowing windows and climate analogs.
- The farm's land and water profile.
- Strip layouts, plan checks, the API and farm tools, the two skills, the
  knowledge tree tool and a starter base, and the web pages for "what to
  plant" and the plan.

Later:

- SoilGrids lookup when it is up; rivers and canals from OpenStreetMap.
- ECOCROP ingested whole, for the worldwide analog search.
- Water need with FAO-56 (ET0 × Kc).
- Forecast-aware advice ("sow after Thursday's rain").
- Licensed knowledge sources, AGROVOC for concept names.
