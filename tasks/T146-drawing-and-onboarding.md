---
id: T146
title: Drawing a plot by pencil, lines or rectangle, areas inside it, and a first-run path
epic: E25
status: review
depends_on: [T133]
---

# T146: Drawing a plot by pencil, lines or rectangle, areas inside it, and a first-run path

## Context

The owner's brief of 2026-11-15: crop cycles at the centre of the farm
app, basics first, everything else optional and additive. Design:
[CYCLES.md](../apps/farm/docs/CYCLES.md), section "Onboarding".

## Scope

- The plot editor: pencil (freehand, simplified to corners), lines, rectangle (drag, rotate, stretch), beside GPS walk and typed coordinates; undo; corner drag.
- Areas inside a plot: cut by a line, or draw a shape; they become the plan's zones.
- The first-run path: village, draw, crop, dates.

## Out of scope

- The cycle model and its API (T145).

## Acceptance criteria

Each item is one observable behavior and maps to one test (web unit
tests, `web/apps/farm/src`; Leaflet itself is checked in the browser, see
Notes).

Drawing a plot (`editor.tsx`, `sketch.ts`, `draw.ts`):

- [x] A new plot starts with the pencil, whose drag draws and whose taps add no corners. `editor.test.tsx` "starts a new plot with the pencil…"
- [x] A freehand loop becomes a few corners, closed, and exactly those corners go to the verifier. `editor.test.tsx` "turns a freehand loop into a few corners…"; `draw.test.ts` "smooths a wobbly loop to its four corners and closes it"
- [x] A freehand stroke keeps a sensible number of corners (at most 24; a round field about 8 to 24) and never crosses itself. `draw.test.ts` "keeps a round field…", "caps a jagged stroke…"
- [x] A tap or a scribble too small for a plot draws nothing. `draw.test.ts` "refuses a tap or a scribble…"
- [x] Simplifying a line keeps its corners and drops the points along straight runs (Douglas-Peucker in metres). `draw.test.ts` "keeps the corners of a line…"
- [x] A rectangle is the box dragged; its turn handle points its length without changing its size; its length and width handles move one edge, the opposite edge staying put, and never turn it inside out. `draw.test.ts` "rectangle" (3 tests)
- [x] In the editor the rectangle shows its three handles, and each handle drag is one undo step. `editor.test.tsx` "drags a rectangle, turns and stretches it…"
- [x] Dragging a corner of a rectangle leaves plain corners (no handles). `editor.test.tsx` "leaves the rectangle as plain corners…"
- [x] Lines adds a corner per tap; *Move map* lets one finger pan (no stroke, no taps). `editor.test.tsx` "adds a corner per tap with lines…"
- [x] Clear empties the boundary and Undo brings it back. `editor.test.tsx` "clears the boundary, and undo brings it back"
- [x] The village search still moves the map; a size that is not a number is still flagged. `editor.test.tsx` (the three earlier tests)

Areas inside a plot (`draw.ts`, `areas.tsx`, `plan.ts`, `planning.tsx`):

- [x] A line drawn across a plot cuts it in two pieces that tile it; a line that misses or only nicks it cuts nothing; on a U-shaped plot only the arm drawn over is cut. `draw.test.ts` "splitByLine" (3 tests)
- [x] A line cuts every area it is drawn across. `draw.test.ts` "cuts every area a line is drawn across"
- [x] A shape over the plot's edge becomes an area clipped to the plot; a shape wholly inside becomes an area and the rest is cut so no area has a hole; a shape over two areas splits between them; a shape outside does nothing; a corner exactly on an edge is handled. Every result tiles the plot with simple rings. `draw.test.ts` "areas" (5 tests)
- [x] The area editor starts with one area, cuts and draws areas, undoes, says when a line misses, and *Move map* frees one finger. `areas.test.tsx` (5 tests)
- [x] Each area gets a part with its share of the plot (a new area takes the first area's crop); a suggestion goes to the first area without a crop; a saved plan's zones come back as areas only when they cover the plot. `plan.test.ts` "areas as the plan's zones" (4 tests)
- [x] Drawn areas become the plan's zones as drawn, checked by the server (`POST /plan/check`, not laid out again), and an area without a crop stays unplanned. `planning.test.tsx`

First run (`firstrun.tsx`, the editor's guided mode, `app.tsx`):

- [x] A farmer with no plots goes village, draw, name (size optional), crop: four steps; *Suggest for me* lists crops, picking one shows the sow and harvest dates, *Other suggestions* goes back, and saving ends on the plot's season work with the plan saved. `firstrun.test.tsx` "takes a new farmer from the village…"
- [x] *Next* on the drawing step waits for a boundary that is not invalid. Same test.
- [x] Naming a crop shows its dates at once. `firstrun.test.tsx` "shows the dates of the crop the farmer names at once"
- [x] The crop step never blocks: with the suggestions down, an error shows and *Skip* still opens the plot. `firstrun.test.tsx` "never blocks on the crop step…"
- [x] With crop planning off the path is three steps and ends at the saved plot. `firstrun.test.tsx` "ends at the saved plot in three steps…"
- [x] The guide can be skipped for the plain editor. `firstrun.test.tsx` "lets the farmer skip the guide…"
- [x] The farm web first load stays within `FARM_WEB_BUDGET_KB` (162 KB): 161,833 bytes before, 161,835 after; all new code is in lazy chunks. `web/scripts/check-size.sh` (in `make web-check`)

## Notes

- **No Go change.** The editor's boundary goes to `POST /api/verify` and
  `POST /api/farms` as before; drawn areas go to the existing
  `POST /api/farms/{id}/plan/check` and `PUT /plan` as `agro.Zone` rings
  (row bearing 0, north to south, as the strips have). `planning.tsx`
  calls `call()` for the check rather than adding `api.checkPlan`, since
  `api.ts` is in the first load.
- **Map strokes** (`map.tsx`): capture-phase `pointerdown`/`mousedown`/
  `touchstart` listeners on the map container stop Leaflet's pan only
  while a one-pointer stroke is in progress and only from the zoom where
  taps add corners (`cornerZoom`); below it a drag pans and a tap zooms
  in. A second finger cancels the stroke and goes to Leaflet's pinch; the
  wheel and the zoom buttons work throughout. Drag handles are updated in
  place so the one under the finger keeps its drag. `user-select: none`
  while a tool is on (in `draw.css`, a lazy stylesheet with the map).
- **Areas never have holes.** `agro.CheckPlan` rejects a self-touching
  ring, so a shape wholly inside an area cuts the rest along an
  east-west line through the shape: three areas, not two. Clipping is
  Greiner-Hormann in metres on a local plane; a corner exactly on an edge
  is nudged by a millimetre first.
- **Checked in a browser** (Playwright, Chromium, real farm binary,
  imagery tiles loaded): pencil by one finger (CDP touch) drew 12 corners
  and the map did not pan; *Move map* then panned; a mouse rectangle,
  turned and widened by its handles, did not pan the map; wheel and +/-
  zoomed with the pencil on; cut and shape made four areas that the
  server's plan check accepted and saved; the first run from village to
  season work. Screenshots `d1` to `d11` in the project files (`farm/`).
- **Mutations** (each made a test fail): clipping always walking forward
  (4 failed); stretching from the centre instead of the far edge (1);
  handle drags not undoable (1); areas without a crop zoned (2); no cut
  for a shape inside an area (2); no corner cap on freehand (1, after
  adding the jagged-stroke test, which the first run of this list
  showed missing).
- **Not done / for later:** the area editor has no rotate handles for its
  rectangle (a drag only); no redo; the first run asks nothing about
  soil and water, so the suggestion uses defaults (by design: optional).
  A farmer who leaves the crop step by the sidebar sees the new plot
  only after the list reloads.

### For T145 (cycles)

- The first run ends in `firstrun.tsx` (`onDone` with the landing tab)
  and `app.tsx` (`Overview`'s `onDone`), both marked `HANDOVER(T145)`:
  send the farmer to the plot's cycles there instead of the season work
  or plan tab. `FarmDetail` takes an initial `tab`.
- Areas are `plan.zones` rings; a cycle's areas can use `areaParts`,
  `areaPlan` and `areasOf` in `plan.ts`, and `AreaEditor` from
  `areas.tsx` as it is.
