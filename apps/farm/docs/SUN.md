# The sun, the slope and the seasons

The farmer asked whether the app counts the angle of the sun, how much
sunlight a plot gets, and the season. It now does, from the plot's
latitude, its slope and NASA POWER's sunlight normals. Task:
[T137](../../../tasks/T137-sun-and-seasons.md).

## Before T137

| What | Before | Now |
|---|---|---|
| Season | Each crop sown every week for a year, its whole season played against the month's heat, frost and rain; kharif, rabi or zaid | The same, and the light and day length of that season |
| Sunlight | Not counted | NASA POWER's all-sky and clear-sky light per month (`ALLSKY_SFC_SW_DWN`, `CLRSKY_SFC_SW_DWN`, MJ/m² a day); bright sun hours from it |
| Day length | Not counted | From the latitude, day by day; short- and long-day crops noted |
| Slope | Measured (fall, downhill side) in the land survey, not used | The slope's tilt and the way it faces change the light on it, month by month |
| Shade between crops | The shortest day only, the noon sun always to the south | Each month the shorter crop stands in the field; inside the tropics the summer noon sun is north of the plot, so shade falls either way |
| Crop light needs | Not recorded | `light` (full, part, shade) and `day_length` (neutral, short, long) for every crop |

## The package `sun`

Pure functions, no network:

| Function | What | Source |
|---|---|---|
| `Declination`, `DayLength`, `Extraterrestrial` | The sun's declination, the day's length and the light at the top of the atmosphere | FAO-56 eqs 21 to 25, 34 (tested on its worked examples 8 to 10) |
| `Vector`, `At` | The sun's direction and its height and bearing at an hour (solar time) | Standard spherical astronomy |
| `Incidence` | The angle between the rays and the ground's perpendicular, on flat ground or a slope | Same |
| `BeamRatio` | Direct light on a slope over flat ground, integrated over the day | Tested on Duffie and Beckman's closed form for an equator-facing slope |
| `OnSlope` | A month's light on a slope: direct × `BeamRatio`, sky light × the sky the slope sees, plus ground reflection (albedo 0.2) | Liu and Jordan's isotropic sky; diffuse share from Erbs, Klein and Duffie (1982) |
| `BrightHours` | Bright sun hours from the light | Ångström with FAO-56's default a = 0.25, b = 0.50 |
| `ShadowNorth` | Which way shadows lean (north or south) over a season | Light-weighted, 7 to 17 h |
| `Year` | Twelve `Month`s: day length, sun hours, noon sun, noon incidence, light flat and on the slope, cloud's share | Klein's representative day per month |

## Where it is used

- **Climate.** `nasapower` asks for the two light normals beside heat and
  rain. A gap in them leaves the light out (`sun.unknown`); the rest of
  the climate stands.
- **Slope.** `planner.SlopeOf` reads the survey's terrain (fall over the
  distance between the highest and lowest points, and the bearing from
  one to the other). A plot not surveyed, or flat to the 90 m elevation
  model, counts as flat. Small plots on gentle land are usually flat at
  that resolution; the survey says so.
- **Recommender** (`agro.Recommend`). Each sowing's season gets a
  `light` balance: the mean light on the slope, sun hours, the dullest
  month, the slope's factor and the day lengths. A light factor joins the
  score:

  | Crop wants | Rule (season's mean light on the ground, MJ/m² a day) | Note |
  |---|---|---|
  | Full sun | 1 from 15; falls to 0.8 at 9 and below | `sun.ok`, `sun.low` |
  | Part shade | 1 from 10; 0.8 at 6 | `sun.part` |
  | Shade | 0.7 above 17 in the open | `sun.strong`, `sun.shade` |

  A slope that changes the season's light by 7 % or more is noted
  (`sun.slope`). Day length does not change the score (Indian varieties
  are bred for local days) but is noted: short-day crops sown as the days
  shorten below 12 h flower early (`day.short`, a risk); long-day crops
  name the day lengths (`day.long`).
- **Plan checks.** `shade.tall` now asks where shadows fall over the
  shorter zone's season (`ShadowNorth` ≥ 0.15 m north or south per metre
  of height), not "the sun is always south".
- **Plant groups.** The shade effect samples each month the shorter crop
  stands in the field and names the worst.
- **Calendar.** `Request.Calendar` returns every sowing date's score;
  `compare_crops` folds it into the best score per sowing month.

## The assistant and the web

- Tools `get_sun` (the sun report, with notes) and `compare_crops` (two
  to eight crops, month by month); the `research-crops` skill; the
  planning system prompt says the noon sun is north of the plot for part
  of the year inside the tropics.
- `GET /api/farms/{id}/sun`; `POST /api/farms/{id}/recommend` takes
  `calendar`.
- The plot's **Sun and seasons** tab: the year month by month (sowing
  seasons, sunlight against a clear sky's, bright hours, day length, the
  noon sun's height and side, the rays' angle, the slope against flat
  ground, rain, heat), the noon sun seen from the east for June, March
  and December, and a sowing calendar: a square per week per crop,
  coloured by the season's score, the farm's best six to start with, any
  crop added; a square opens that sowing's reasons and risks.

## Limits

- Shade from things outside the farm (buildings, the neighbour's trees,
  hills to the east or west) is not counted: OSM rarely has their height.
- Trees are counted at their grown height; young trees cast less.
- The light thresholds are starter values from crop physiology texts and
  Indian packages of practice; they need an agronomist's review, like the
  rest of the rules.
- Times are solar; the noon sun is at true local noon, not 12:00 IST.
