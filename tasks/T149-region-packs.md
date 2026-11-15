---
id: T149
title: Region packs - crop library, inputs, cycle rules and schemes by country, state and district
epic: E25
status: review
depends_on: [T145, T148]
---

# T149: Region packs - crop library, inputs, cycle rules and schemes by country, state and district

## Context

The owner's ask of 2026-11-15: "ensure we are building software in a
good, open-to-changes way": methods, insurance, the cycle library and the
crop library can be the same or differ from region to region, and
differences must plug in later without code changes. Until now there was
one crop library and one inputs book for every plot, the cycle rules were
constants in code, and the app had no notion of region. Design:
[REGIONS.md](../apps/farm/docs/REGIONS.md).

## Scope

- `apps/farm/region`: the base set plus overlays keyed by country, state
  and district (most specific wins, field by field), loaded and checked
  at startup from embedded files and an optional folder (`-regions`,
  `FARM_REGIONS`), resolved by a plot's place, every overridden value
  with its source and layer; a schemes slot (crop insurance) as data.
- The cycle rules as data (`region.Rules`: turnaround, max wait, wait
  cost, rotation adjustments, seasons), the base numbers unchanged.
- The planner, the cycle service, the inputs, roadmap and timing addons,
  the season roadmap, the calendar and weekly advice take their library,
  book, rules and storage values from the plot's pack.
- Local sowing windows on a crop (`agro.Crop.Windows`) and switching a
  crop off (`agro.Crop.Off`).
- Embedded overlays for Andhra Pradesh, YSR Kadapa and Anantapur with
  sourced values only.

## Out of scope

- Showing or alerting on schemes (cut-offs, loss notice): data only.
- A review desk page for packs (the origins are there for it).
- Moving the recommender's weights, `agro.SeasonOf` inside scoring, the
  price tools' MSP and the soils list into packs (REGIONS.md, "Still in
  code").
- The web client: no change.

## Acceptance criteria

- [x] With no overlay for a place, the pack is the base, and the base is the built-in library, book, rules, storage and MSP unchanged; the base seasons are `agro.SeasonOf`'s — test: `region_test.TestBaseIsTheBuiltInData`, `TestDefaultSeasonsAreTheIndianSeasons`
- [x] A plot with no overlay gets byte for byte the line it got without packs — test: `cycle_test.TestRegionPacksChangeTheLineThereOnly`
- [x] A plot in YSR Kadapa gets the district's sowing windows on top of Andhra Pradesh's scheme, another AP district only the state's, other states the base; names match by alias, case and punctuation; each value reports its layer, file and ref, unset values "base" — test: `region_test.TestEmbeddedAndhraPradeshOverlays`
- [x] A sowing outside a local window that applies scores less there than on the base and says so; inside it, the same score with the window as a reason — test: `region_test.TestWindowsMoveTheScoreThereOnly`
- [x] An overlay changes crops, inputs doses and products, rules (rotation merged field by field), storage, MSP and schemes for its place only — test: `region_test.TestDirectoryOverlayChangesItsPlaceOnly`
- [x] A rules overlay changes the suggested line there only (turnaround 60 days moves cycle 2) — test: `cycle_test.TestRegionPacksChangeTheLineThereOnly`
- [x] A crop an overlay switches off is never suggested or recommended there, is still elsewhere, is still scored when asked for by name, and stays known for existing plans — test: `cycle_test.TestRegionPacksChangeTheLineThereOnly`
- [x] The inputs addon plans from the plot's pack's book; a plot elsewhere gets the plan it got without packs — test: `addon_test.TestInputsFromTheRegionPack`
- [x] An invalid overlay (unknown key at any level, no ref or an unknown source, a wrong type, an unknown crop, a banned product, bad rules, a bad window, a bad region) fails with `region.ErrInvalid` naming the file and the key — test: `region_test.TestInvalidOverlayFailsWithFileAndKey`
- [x] Two files for one region fail — test: `region_test.TestTwoFilesForOneRegion`
- [x] A bad overlay in the `-regions` folder stops `cmd/farm` at startup with the file and key — test: `main.TestBadRegionOverlayStopsTheStart`
- [x] The base rules are the constants the cycles were built with — test: `cycle_test.TestBaseRulesAreTheConstants`
- [x] Existing tests pass unchanged

## Notes

- Design note: [REGIONS.md](../apps/farm/docs/REGIONS.md). One picker:
  `cmd/farm` loads the set, `planner.WithRegions` hands it out; consumers
  ask the planner (`Pack`, `LibraryFor`, `RulesFor`) or take
  `addon.WithPacks`. No switch, no `init()`.
- `cycle/compute.go` changed only mechanically (constants to
  `in.Rules.Or()`, and switched-off crops left out of suggestions), as
  another change to how the suggested crop is chosen is in flight there.
- Sources read 2026-11-15: ICAR-CRIDA district contingency plans for YSR
  Kadapa and Ananthapuramu (table 1.12), PMFBY revamped operational
  guidelines (paras 5.2.4, 16.2, 21.5.4, 21.6.3), Deccan Chronicle of 30
  October 2026 on AP's kharif 2026 insurance. ANGRAU's own page for
  Anantapur did not load (503); no value was taken from search snippets.
- Size: the farm binary grows 143,360 bytes ([SIZES.md](../docs/SIZES.md)).

## Review fixes (branch `wt-fix`, 2026-11-15)

- Overlay keys differing only in case or punctuation ("Andhra Pradesh",
  "ANDHRA PRADESH.") escaped the duplicate check but matched the same
  place. The region key is now built with `fold()`, as matching is, so
  they are refused as one region, and a district finds its state's
  overlay whatever the punctuation (`TestTwoFilesForOneRegion`). Pack
  keys read "IN/andhrapradesh" now (only logged).
