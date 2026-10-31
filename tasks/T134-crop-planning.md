---
id: T134
title: Crop planning: what to plant, when, and where on the plot
epic: E25
status: review
depends_on: [T133]
---

# T134: crop planning

## Context

The owner's second brief for the farm app ([E25](E25-farm-app.md)): turn
a verified plot into data the app can plan on; tell the farmer what can
grow, when to sow (the next good time, or around when they want), and how
to share the plot between crops; check the plan by rules; give the agent
skills to suggest and check plans; and a knowledge base that can be
searched and walked as a tree. Design and source survey:
[CROP-PLANNING.md](../apps/farm/docs/CROP-PLANNING.md).

## Scope

- `apps/farm/agro`: the soil library (11 Indian soil types with local
  names and field signs) and crop library (37 crops, four of them climate
  analogs from Mexico and the Andes); climate normals; the land and water
  profile; the recommender (every crop, every week of the next year, fuzzy
  "around" dates, sowing windows, reasons and risks); strip layouts; the
  plan checker. `agro/agrotest`: a recorded climate and a fake source.
- `apps/farm/geo`: `Strips` (cut a plot along its long axis by area
  shares) and `Extent`.
- `apps/farm/nasapower`: climate normals at any point from NASA POWER.
- `apps/farm/planner`: joins farms to the rules; `farm.Farm` gains a
  profile, a plan and its report.
- `apps/farm/farmapi`: soils, crops, climate, profile, recommend, layout,
  plan check, save and clear.
- `apps/farm/farmagent`: nine planning tools, the `plan-season` and
  `check-plan` skills (pkg/skill), and the handbook (pkg/knowledge).
- `apps/farm/handbook`: a starter knowledge base (seasons, soils, crops,
  mixed farming, water, sources), chapters and sections.
- `pkg/knowledge`: `Outline` and `knowledge_outline`, and `knowledge_read`
  of one section, so a base is a tree as well as a search.
- `web/apps/farm`: land and water form, "what to plant", the season plan
  with its zones on the map and its check.

## Out of scope

- SoilGrids lookup (down when this was built), rivers and canals from
  OpenStreetMap, FAO-56 water need, forecast-aware advice, ECOCROP and
  licensed sources in bulk (CROP-PLANNING.md, "later").

## Acceptance criteria

- [x] The libraries load and are consistent (ranges, textures, seasons, mutual intercrops) — test: `agro_test.TestDefaultLibrary`, `TestLoadRefusesInconsistentData`
- [x] Profiles are checked; irrigation adds up with reliability and drip — test: `agro_test.TestProfileCheck`, `TestIrrigation`
- [x] Black soil, rainfed, at Hyderabad: chickpea now, the kharif crops next June — test: `agro_test.TestBlackSoilRainfedAtHyderabad`
- [x] Results are ordered, windows well formed, analogs flagged, the same request gives the same answer — test: `agro_test.TestResultShape`, `TestDeterministic`
- [x] "Around" is a range clamped to today, and in-range options come first — test: `agro_test.TestAround`
- [x] Irrigation never lowers a score — test: `agro_test.TestIrrigationNeverLowersAScore`
- [x] Cold, frost, salt, pH and texture rules — test: `agro_test.TestCoolClimate`, `TestSoilRules`
- [x] Bad requests are refused — test: `agro_test.TestRecommendRefuses`
- [x] Strips hold their shares, stay inside the plot, do not overlap, run along the long side — test: `geo_test.TestStrips`, `TestStripsRefuses`, `TestExtent`
- [x] Plan checks: layout is sound; every mistake rule fires — test: `agro_test.TestLayoutAndCheck`, `TestCheckPlanFindsMistakes`, `TestShadeOnlyFromTheSunnySide`, `TestCheckPlanWithoutClimate`
- [x] NASA POWER: parse, rain per month, cache per cell, failures not cached — test: `nasapower_test`
- [x] The planner: farmer's date in India, profile used, failures, save refuses errors — test: `planner_test`
- [x] Profile and plan survive edits and stores — test: `farm_test.TestServiceKeepsProfileAndPlanAcrossEdits`, `farmtest.RunStore`
- [x] The API routes and their errors — test: `farmapi_test.TestPlanningRoutes`, `TestPlanningErrors`
- [x] The planning tools, their kinds, and the assistant with skills and the handbook — test: `farmagent_test.TestPlanningToolKinds`, `TestPlanningToolsFlow`, `TestAssistantWithPlanningSkillsAndHandbook`
- [x] Outline and section reads — test: `knowledge_test.TestOutline`, `TestOutlineAndSectionTools`, `TestSectionReadsInParts`
- [x] The handbook is a tree and searchable — test: `handbook_test.TestHandbook`
- [x] Plan helpers of the web app — test: `web/apps/farm/src/plan.test.ts`

## Notes

- Ran end to end in a cloud session (2026-10-31) against real NASA POWER
  climate for Farooqnagar: Playwright set the land and water profile
  (black soil, a borewell that runs dry, cotton last season), got
  suggestions, laid out 60 % chickpea and 40 % sunflower, saved it and saw
  the zones on the map after a reload. The assistant on Claude, asked in
  Telugu what to sow this rabi and whether the plan is right, loaded
  plan-season, called check_plan and recommend_crops, and answered in
  Telugu with the rotation benefit and the sunflower's water risk. On
  Gemini (gemini-3.5-flash), asked in Hindi about tomato around June, it
  answered from recommend_crops and cited the handbook on black soil
  drainage.
- Two fixes came from those runs: a plan checker rule for zones sown
  months apart (two seasons in one plan), and get_farm now shows the
  profile and plan (the assistant asked again for what was saved).
- The rules' numbers are starter values (ECOCROP ranges, packages of
  practice); they need review by an agronomist before farmers rely on
  them.
