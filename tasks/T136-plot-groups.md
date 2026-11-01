---
id: T136
title: Plot groups, plant groups and how they are spread, with their effects; the layout UX made responsive
epic: E25
status: review
depends_on: [T134, T135]
---

# T136: plot groups and plant distribution

## Context

The owner's fourth brief for the farm app ([E25](E25-farm-app.md),
2026-11-01): organise plots in groups, many groups; each group has its
own plant groups and its own distribution over its plots; a skill for the
agent to think about how plants are spread over a plot's shape and what
that does; and a layout UX that is responsive and more beautiful. Design:
[GROUPS.md](../apps/farm/docs/GROUPS.md).

## Scope

- `apps/farm/agro`: every crop's grown height (`height_m`), for shade.
- `apps/farm/geo`: strips along any bearing, bands of given widths.
- `apps/farm/planting` (new): plant groups (block, border, tree rows),
  arrangements (strips by share, alternating strips, whole plots), row
  direction; `Distribute` cuts the plots, places trees, and measures the
  effects: shade through a winter day, edges, narrow and small pieces,
  tree fit, water, neighbours, wind shelter, what the layout takes.
- `apps/farm/farm`: `Group`, `GroupStore` with its conformance suite,
  the service's group methods (a plot in one group at a time; deleting a
  plot takes it out of its group); `sqlite` and `farmtest` implement it.
- `apps/farm/planner`: lay out, save and clear a group's distribution.
- `apps/farm/farmapi`: `/api/groups` and `/api/groups/{id}/distribution`
  (get, preview, save, clear).
- `apps/farm/farmagent`: `list_groups`, `save_group`,
  `distribute_plants`, `save_distribution`; the `distribute-plants`
  skill.
- `web/apps/farm`: a sidebar of groups and their plots (a drawer on
  phones), an overview with group cards, the group page (a to-scale plan
  of the group with each piece in its colour, rows hatched the way they
  run, trees as crowns, the midday sun's side; templates; an editor with
  a live preview; effects and numbers), a group form, plot pages with
  tabs; laid out for phones through desktops.

## Out of scope

- Several distributions per group (seasons); drawing pieces by hand;
  slope and aspect in shade; young-tree growth over the years.

## Acceptance criteria

- [x] Strips along a bearing and bands of given widths — test: `geo_test.TestBands`, `TestAlong`
- [x] Bad distributions are refused with a reason — test: `planting_test.TestRefusesBadDistributions`
- [x] Strips by share, north-south rows, border with its trees, alternating strips, alleys, whole plots — test: `planting_test.TestStripsByShare`, `TestNorthSouthRows`, `TestBorderAndItsTrees`, `TestAlternateStrips`, `TestAlleys`, `TestByPlot`
- [x] Shade from taller plants on the sunny side, less with north-south rows; water; neighbours; tree fit — test: `planting_test.TestShade`, `TestWater`, `TestNeighbours`, `TestTreesFit`
- [x] Distribute leaves the caller's plots alone and gives the same answer twice — test: `planting_test.TestDistributeIsPure`
- [x] Groups: create, rename, plots in one group only, delete a plot leaves its group; any store passes the suite — test: `farm_test.TestGroups`, `TestNoGroupStore`, `sqlite_test.TestGroupConformance`
- [x] Lay out, save, clear a group's distribution — test: `planner_test.TestDistribution`
- [x] API routes and their errors — test: `farmapi_test.TestGroupRoutes`
- [x] Agent tools: kinds, flow, bad input is the model's to fix — test: `farmagent_test.TestGroupTools`
- [x] The group page lays a template out live and saves it; the group form; templates, colours, problems, shares — test: `web/apps/farm/src/groups.test.tsx`, `distribution.test.ts`
- [x] On Claude: reads the shade warning with its number, compares north-south and east-west rows, groups plots and saves a layout — eval: `make farm-eval` (the three group scenarios)

## Notes

- Shade is sampled hourly from 9 to 3 on the winter solstice (December in
  the north); a spot counts as shaded when it is in shade for 4 of the 7
  hours. Heights are of grown plants.
- Screenshots on desktop and phone in the project files (`farm/g1` to
  `g11`).
