---
id: T154
title: A wide experimental crop tier from FAO ECOCROP, and varieties under a crop
epic: E25
status: review
depends_on: [T134, T149, T153]
---

# T154: A wide experimental crop tier from FAO ECOCROP, and varieties under a crop

## Context

The owner's asks of 2026-11-16: 75 crops is few; include what could grow
here even when it is not popular here, since the weather can match and
people can try different things, marked experimental at best; and store
varieties of a crop (tomato, mango).

## Scope

- **Experimental tier.** `apps/farm/agro/data/wide.json.gz`: 1,282 food,
  fodder, medicinal and material plants from FAO ECOCROP that the library
  does not have, built by `data/ecocrop/build.py`. `agro.Wide()`,
  `Crop.Experimental`, `Species`, `Common`, `Grown`, `Zones`. It answers
  only when asked (`planner.Ask.Experimental`, with `Kinds` and `Search`),
  through `recommend_crops`, `recommend_for_place`, `list_crops`,
  `get_crop`, both recommend routes, and "What else could grow here?" on
  the planning screen. Every option says it is experimental.
- **Climate zones.** `Climate.Zone()` (Trewartha) and a mark-down for a
  plant ECOCROP does not know from the plot's zone, so a temperate tree is
  not offered in a hot dry district because the yearly means fit.
- **Varieties.** `Crop.Varieties`, each with its document: 266 on 32
  crops from TNAU's horticulture guide in the base; YSR Kadapa's own for
  groundnut, red gram, cotton, sunflower, castor and horse gram from the
  district plan, in its overlay. Shown by `get_crop` and on the planning
  screen's suggestions.
- `farm-data-updates` lists the variety sources and ECOCROP.

## Out of scope

- An experimental plant in a season plan or a cycle: it has no spacing,
  dose or schedule, so it can be explored and tried, not planned.
- Variety-level scoring (duration, traits): the documents read give names
  and, for some, what they are for.
- Field crop varieties in the base, and other districts' varieties.
- Readable local names for the experimental plants (ECOCROP gives the
  botanical name and common names in mixed languages).

## Acceptance criteria

- [x] The tier loads, is a thousand plants or more, all experimental, with no species the library has (`TestWideTier`).
- [x] The usual answer never holds an experimental plant; asked for, the answer holds farmed plants only, each saying it is experimental, best first; a search finds a plant by a common name; a kind limits it (`TestExperimentalTierOnlyWhenAsked`).
- [x] Coffee is marked down in Kadapa's climate (same test); the whole tier is scored in about a second (`TestWideTier` logs it).
- [x] The crop tools reach the tier and show a crop's varieties (`TestCropToolsReachTheExperimentalTier`).
- [x] A variety without a name or a source is refused (`Library.check`); mango has Banganapalli with its source (`TestDefaultLibrary`); a Kadapa plot gets the district's varieties and another district does not (`TestEmbeddedAndhraPradeshOverlays`).
- [x] The planning screen shows varieties and asks the tier only on its own button, with no "Add to plan" there (`planning.test.tsx`).

## Notes

- Licence: FAO's ECOCROP page states none. FAO's database terms (CC BY
  4.0, credit FAO) name its statistical databases; they bar use "for or in
  conjunction with the promotion of a commercial enterprise". Confirm with
  FAO before commercial use. Recorded in `crops.json` `sources`.
- By default the tier offers plants farmed at scale somewhere, fodder
  apart: most of ECOCROP is wild trees and pasture grasses, and some
  (Prosopis juliflora) are invasive in India. The note on every
  experimental answer says to check that with the agriculture office.
- The horticulture varieties are Tamil Nadu's list, not Andhra Pradesh's.
- The Kadapa plan calls every sunflower entry a hybrid and does not mark
  castor's hybrids; the data follows the plan.
- After the first deployment (2026-11-16) the assistant offered Prosopis
  juliflora and gave lentil a June sowing date. Fixed after: the build
  marks the 33 plants of the tier that the Global Register of Introduced
  and Invasive Species for India records as invasive
  (`data/ecocrop/griis-india-invasive.txt`, CC BY 4.0); they are never
  offered, and by name they score nothing with an `invasive` risk. An
  experimental annual carries an `experimental.season` risk: its date is a
  climate match, not a local season. Castor is on that register too and
  stays in the reviewed library: it is a traditional crop here.
