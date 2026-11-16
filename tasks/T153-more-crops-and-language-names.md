---
id: T153
title: More crops (castor, horse gram, vegetables, small fruit) and crop names in Kannada, Tamil and Marathi
epic: E25
status: review
depends_on: [T134, T149]
---

# T153: More crops and crop names in more Indian languages

## Context

The owner's asks of 2026-11-16: does the app do small fruit and vegetable
farming or only grains; add more plants; why were castor and horse gram
missed; add Indian language names (Telugu now, other languages later).

Castor and horse gram were missed because the library began as a general
all-India starter list of 39 crops, and the district overlays took only
the "5 major field crops" table of each district plan. Both crops are in
those plans, further down (the Southern zone sowing table, the late-rain
contingency pages), and were never carried over.

## Scope

- 36 crops added to `apps/farm/agro/data/crops.json` (39 to 75): horse
  gram, castor, cowpea, field bean, safflower, little millet, kodo
  millet; cluster bean, French bean, cabbage, cauliflower, carrot, radish,
  beetroot, cucumber, bottle gourd, bitter gourd, ridge gourd, pumpkin,
  muskmelon, capsicum, garlic, spinach, amaranth greens, coriander,
  fenugreek; sweet orange, acid lime, pomegranate, custard apple, sapota,
  grape, ber, amla, fig, tamarind.
- Every crop named in Telugu, Hindi, Kannada, Tamil and Marathi.
- YSR Kadapa's castor sowing window from the district plan.

## Out of scope

- Curry leaf and mulberry: perennials with no feeding values of their own
  in the inputs book, and its loader refuses a perennial that would
  default to an annual's doses. They need their own values from a source.
- Flowers (marigold, jasmine, chrysanthemum): no kind for them yet.
- Translating the screens. Crop names follow the browser's language; the
  labels are English.
- Own seed rates and doses for the new crops: they use their kind's
  default (a vegetable like tomato, a pulse like black gram, a fruit tree
  like mango) until read from a package of practices.

## Acceptance criteria

- [x] The library loads with castor and horse gram, and every crop has a name in te, hi, kn, ta and mr (`TestDefaultLibrary`).
- [x] A Kadapa plot's castor carries the district plan's windows, any water (`TestEmbeddedAndhraPradeshOverlays`).
- [x] Every crop still gets an inputs plan (`apps/farm/inputs` tests).

## Notes

- The values are starter values (ECOCROP ranges, ICAR and state
  university spacing and seasons), like the first 39. The Kannada, Tamil
  and Marathi names were written without a native speaker's review.
- The Kadapa plan's Southern zone table has a row for horse gram with no
  dates, so horse gram has no district window.
