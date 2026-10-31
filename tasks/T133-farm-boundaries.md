---
id: T133
title: Farm plots with verified boundaries, and the farm assistant
epic: E25
status: review
depends_on: []
---

# T133: farm plots with verified boundaries, and the farm assistant

## Context

The first feature of the farm app ([E25](E25-farm-app.md),
[product notes](../apps/farm/docs/PRODUCT.md#first-feature-my-land)): a
farmer records each plot's boundary and the app checks it, so that every
later feature (field watch, season plans, insurance claims) starts from a
plot that is where and as big as it should be.

## Scope

- `apps/farm/geo`: area, perimeter, centroid, interior angles, self
  intersections, containment and overlap of small polygons on WGS84.
- `apps/farm/farm`: plots, units (m², ha, acre, gunta, cent), the
  verifier and its checks, the service (saves only verified plots), the
  store interface; `farm/sqlite`; `farm/farmtest` (fake, suite, locator).
- `apps/farm/nominatim`: reverse geocoding on OpenStreetMap, within the
  public server's policy (1 request a second, User-Agent, cache).
- `apps/farm/farmagent`: the farm tools and the assistant (harness).
- `apps/farm/farmapi`, `apps/farm/cmd/farm`: the API and the binary,
  serving the embedded web client.
- `web/apps/farm`: plots list, add or edit by drawing, walking or typing,
  live verification, plot page with GeoJSON export, the assistant.

## Out of scope

- Satellite land-cover check, land-record matching, offline capture,
  accounts (E25's later tasks). An image and a deployment for the app.

## Acceptance criteria

- [x] Area and perimeter agree with GeographicLib — test: `geo_test.TestAgainstGeographicLib`, `TestAreaMatchesTheEllipsoid`
- [x] Self intersections, spikes and containment found — test: `geo_test.TestSelfIntersections`, `TestAngles`, `TestContainsAndOverlap`
- [x] A good plot is verified, with sizes in every unit and its location — test: `farm_test.TestVerifyGoodPlot`
- [x] Too few corners, out of range, crossing, too small, too large, too many corners are invalid; nothing is looked up — test: `farm_test.TestVerifyInvalidShapes`, `TestVerifyCrossingNamesTheCorners`
- [x] Declared size checked, with the wrong unit noticed — test: `farm_test.TestVerifyDeclaredArea`
- [x] Spikes, strips, very large plots and rough GPS fixes warn, naming the corners — test: `farm_test.TestVerifyWarnings`
- [x] Repeated and closing corners are merged — test: `farm_test.TestVerifyMergesRepeatedCorners`
- [x] Overlaps with the farmer's other plots warn — test: `farm_test.TestVerifyOverlaps`, `TestServiceChecksOverlapsWithOtherFarmsOnly`
- [x] Location: found, in the sea, another country, lookup down or slow — test: `farm_test.TestVerifyLocation`
- [x] Verify never panics and its status matches its checks — test: `farm_test.FuzzVerify`
- [x] Only verified plots are saved — test: `farm_test.TestServiceRefusesInvalidBoundaries`, `TestServiceRefusesBadDrafts`
- [x] Stores pass the suite — test: `farmtest.RunStore` from `sqlite_test.TestConformance`, `farmtest_test.TestMemory`
- [x] Nominatim: parse, nothing there, errors not cached, the interval kept — test: `nominatim_test`
- [x] The farm tools are read-only and answer from the service — test: `farmagent_test`
- [x] The assistant answers from the tools and keeps the conversation server side — test: `farmagent_test.TestAssistantAnswersFromTheTools`
- [x] The API: lifecycle, dry-run verify, errors, assistant — test: `farmapi_test`
- [x] Typed coordinates in decimal and DMS, with errors per line — test: `web/apps/farm/src/coords.test.ts`
- [x] The report view — test: `web/apps/farm/src/report.test.tsx`

## Notes

- Ran end to end in a cloud session (2026-10-31): three plots added by
  typing, drawing and walking (Playwright with a moved geolocation), real
  Nominatim lookups (Farooqnagar, Ranga Reddy), and the assistant on Claude
  answering a Telugu question from list_farms and get_farm. It first
  converted an overlap from m² to guntas itself and got it wrong; the
  overlap check now says guntas, and the prompt asks for convert_area for
  any conversion.
- The farm binary is about 26 MB (it links the Anthropic and Gemini
  adapters and SQLite); it has no image yet, so docs/SIZES.md has no row.
