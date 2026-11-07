---
id: T137
title: The sun, the slope and the seasons in crop planning; crop research by month
epic: E25
status: review
depends_on: [T134, T135, T136]
---

# T137: the sun, the slope and the seasons

## Context

The owner asked (2026-11-07) whether a farmer can research crops, and
whether the app takes the angle of the sun, the amount of sunlight and
the season into account where it matters. Season was counted (a year of
weekly sowings against heat and rain); sunlight, day length and the
slope's effect on light were not, and shade assumed the noon sun is
always to the south, which is wrong inside the tropics for part of the
year. Design: [SUN.md](../apps/farm/docs/SUN.md).

## Scope

- `apps/farm/sun` (new): declination, day length, light at the top of the
  atmosphere, the sun's position, incidence on a slope, light on a slope,
  sun hours, where shadows lean, a year by month.
- `apps/farm/agro`: sunlight in the climate normals; every crop's `light`
  and `day_length`; ginger and cocoa added; the recommender's light
  balance, light factor, slope and day-length notes, and the calendar of
  every sowing; the plan check's shade follows the shorter crop's season.
- `apps/farm/nasapower`: the all-sky and clear-sky light normals.
- `apps/farm/planting`: shade month by month through the shorter crop's
  months; part-shade crops get a note.
- `apps/farm/planner`: the slope from the survey; the sun report; the
  calendar on request.
- `apps/farm/farmapi`: `GET /api/farms/{id}/sun`; `calendar` on recommend.
- `apps/farm/farmagent`: `get_sun`, `compare_crops`; the
  `research-crops` skill; the system prompt and `distribute-plants`
  skill on the tropics' noon sun.
- `web/apps/farm`: the plot's Sun and seasons tab.

## Out of scope

- Shade from buildings, neighbours' trees and hills; young trees'
  growth; a farmer-set slope when the elevation model says flat; weather
  forecasts.

## Acceptance criteria

- [x] Sun geometry matches FAO-56's worked examples and the closed form for an equator-facing slope; the noon sun is north in a Hyderabad June — test: `sun_test.TestFAO56Examples`, `TestBeamRatioClosedForm`, `TestNoonSunSide`, `TestSlopeLight`, `TestShadowNorth`, `TestYear`
- [x] NASA POWER's light is read; a gap in it leaves the rest of the climate — test: `nasapower_test.TestClimate`, `TestSunlightIsOptional`
- [x] A dull sky lowers a sun crop, a shade crop in the open sun is a risk, part-shade crops say they fit under trees — test: `agro_test.TestDullSkyLowersASunCrop`, `TestShadeCrops`, `TestSunnyEnoughAtHyderabad`
- [x] A north-facing slope loses winter light, a south-facing one gains — test: `agro_test.TestSlope`, `planner_test.TestSlopeFromTheSurvey`
- [x] Short-day crops sown late are a risk; long-day crops name their days; no sunlight means no light score — test: `agro_test.TestDayLength`, `TestNoSunlight`
- [x] Every crop has light needs; a calendar has every sowing — test: `agro_test.TestEveryCropHasLightNeeds`, `TestCalendar`
- [x] Plan and plant-group shade follow the season: kharif shade falls south at Hyderabad, rabi shade north — test: `agro_test.TestShadeFollowsTheSeason`, `planting_test.TestShadeFollowsTheSeason`, `TestShade`
- [x] The sun report's notes and slope; the API route and its errors — test: `planner_test.TestSunAtAFarm`, `farmapi_test.TestPlanningRoutes`, `TestPlanningErrors`
- [x] Agent tools `get_sun` and `compare_crops` — test: `farmagent_test.TestSunAndCompareTools`, `TestPlanningToolKinds`
- [x] The Sun and seasons tab: the year, the noon sun, the calendar, a week explained, crops added and removed — test: `web/apps/farm/src/seasons.test.tsx`
- [x] On Claude: the noon sun's side and day length, crops compared by month, cocoa needs shade — eval: `make farm-eval` (the three sun scenarios)

## Notes

- Light thresholds are starter values (full sun good from 15 MJ/m² a
  day, poor at 9); they need review like the rest of the rules.
- Screenshots in the project files (`farm/s1` onwards).
