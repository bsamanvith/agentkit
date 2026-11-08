# Weather, care and automations

The owner asked (2026-11-08) for this week's forecast for each farmer's land,
sent only when there is something to do; skills that plan when to plant and
how to manage a crop; and a way to run fixed automations and agent skills
on many triggers that scales. Task: [T139](../../../tasks/T139-weather-and-automations.md).

Notices go to an **in-app inbox** first (the bell in the header). SMS or
WhatsApp is another `automation.Channel` later; nothing else changes.

## The pieces

| Package | What |
|---|---|
| `weather` | `Source{Name, Forecast}`: up to 16 days of daily weather for a point. Open-Meteo (daily, a 0.1° cell cache for 3 h, one call per cell however many plots ask, a 429 pauses it, one retry on 5xx) and MET Norway (hourly, summed into IST days; ET0 by Hargreaves; gusts estimated at 1.4 × wind when missing). `Fallback` asks each in turn. |
| `calendar` | A crop's stage on a day (establishment, vegetative, flowering, maturing, harvest, as shares of its season; perennials are "growing") and its care tasks by kind of crop: prepare, sow, weed, fertilise, irrigate, scout, protect, prune, reap. |
| `advise` | Rules over a plot, its plan and a week's forecast. Each item says what to do, by when and why; a quiet week says nothing. |
| `automation` | Automations (trigger, scope, action), the engine that runs them, runs and notices, a store with a conformance suite (`automationtest`) and SQLite. |
| `watch` | Joins the planner, the weather and the rules: the `weather_advice` and `crop_tasks` rules and the built-in automations. |
| `farmagent` | `get_week`, `crop_calendar`, `list_alerts`, `notify_farmer`; the `weather-check`, `draw-crop-plan` and `manage-crop` skills; `SkillRunner`, which runs a skill as an automation. |

## Weather providers

`-weather` (`FARM_WEATHER`) is a comma list tried in order, default
`open-meteo,met-norway`; each may carry its own URL (`open-meteo=https://…`).
`off` turns weather off. A new provider is a type with `Name` and
`Forecast` and a case in `buildWeather` (cmd/farm); nothing else learns its
name. A keyed one (IMD, a paid API) takes its key from the environment
in that case.

| Provider | Cost | Terms |
|---|---|---|
| Open-Meteo | Free, no key | Free API is for **non-commercial use only** (under 10,000 calls a day); commercial use needs their paid plan or a self-hosted copy (open source) |
| MET Norway | Free, no key | CC BY 4.0, commercial use allowed with credit; a User-Agent naming the app is required. The forecast's source is shown under each week |

When the app is used commercially, set `-weather met-norway,open-meteo`
(or self-host Open-Meteo) so the free, commercial-friendly provider goes
first.

## Advice rules

Thresholds follow IMD's words where it has them. They are starter values
for an agronomist to review.

| Rule | When | Says |
|---|---|---|
| `heavy_rain` | A day ≥ 64.5 mm (urgent ≥ 115.6), or ≥ 100 mm in three days | Clear drains and bunds, hold irrigation, move inputs and harvest under cover |
| `harvest_rain` | A crop at harvest and ≥ 10 mm coming | Harvest before it, or cover the crop |
| `sowing_rain` | A crop due to be sown and a wet spell coming | Sow after it, or sow into the moisture |
| `strong_wind` | Gusts ≥ 60 km/h (urgent ≥ 75) | Stake tall crops, hold sprays |
| `heat` | A day's mean above the crop's limit, or ≥ 38 °C at flowering or establishment | Irrigate in the evening, mulch, no afternoon sprays |
| `cold` | A day's mean below the crop's limit, or a low ≤ 4 °C | Light irrigation the evening before, cover nurseries |
| `dry_week` | Rainfed, a crop in the field, < 10 mm of rain against ≥ 25 mm of ET0 | Save water, mulch, weed |
| `fertiliser_timing` | A fertiliser task due and ≥ 20 mm on the day or the next | Apply on the named dry day instead |
| `disease_weather` | Humidity ≥ 85% and rain several days running | Scout for blight and mildew, which crops |

## Automations

An automation is a **trigger**, a **scope** and an **action**:

- Triggers: `schedule` (cron, UTC: the binary has no time zone database),
  `event` (`farm.created`, `farm.profile_changed`, `farm.plan_changed`, …,
  from `farm.WithObserver`) and `weather` (a condition such as
  `rain_mm >= 50` within 3 days, checked on a schedule, per plot).
- Scope: all plots, some plots, or only plots with a plan.
- Actions: a `rule` (Go code registered with `automation.WithRule`) or a
  `skill` (an agent run with the skill and the automation's prompt; it may
  send at most five notices through `notify_farmer`).

Built-ins, which can be turned on and off but not changed:

| ID | Trigger | Action |
|---|---|---|
| `weekly-weather` | Monday 06:00 IST | `weather_advice` on every plot |
| `rain-watch` | Every 6 h, rain ≥ 50 mm within 3 days | `weather_advice` |
| `new-plan-weather` | A plan is saved | `weather_advice` on that plot |
| `weekly-tasks` | Monday 06:30 IST | `crop_tasks` on plots with a plan |
| `weekly-field-brief` | Sunday 18:00 IST, off by default | the `weather-check` skill |

### How it scales

- Each schedule time is claimed in the store before it fires
  (`automation_firings`, keyed by automation and time), so several farm
  processes on one database fire it once.
- After downtime an automation fires once, for the latest missed time, not
  for every missed time.
- One job per plot, on a bounded pool of workers (`-automation-workers`,
  default 4); a job has a timeout and two retries with backoff.
- The forecast is fetched once per job and shared by the condition and the
  rule; Open-Meteo answers neighbouring plots from one cell.
- A notice is keyed (`rule:farm:day`); the same key within six days is not
  sent again, so the weekly check and the rain watch do not repeat each
  other. Skill runs see the plot's unread alerts in their prompt.
- Runs and notices older than 60 days are pruned.
- Adding a trigger kind, rule or channel touches only the engine's option
  list: `WithRule`, `WithChannels`, a new `TriggerKind` case.

## The app

- **Weather and care** tab on a plot: the week's forecast, the advice (or
  "Nothing to do because of the weather this week."), the week's work with
  each crop's stage, the season's calendar and the plot's alerts.
- **Alerts** (the bell): the inbox across plots, mark read; the
  automations with on/off, run now and recent runs; a form for a new one.

API: `GET /api/farms/{id}/week`, `GET /api/farms/{id}/calendar`,
`GET /api/notices`, `POST /api/notices/read`, `GET|POST /api/automations`,
`PUT|DELETE /api/automations/{id}`, `POST /api/automations/{id}/run`,
`GET /api/automations/{id}/runs`.

## Later

- SMS and WhatsApp channels, the farmer's language, quiet hours.
- IMD district forecasts and nowcasts as a provider.
- Price alerts as a rule over the prices feed.
