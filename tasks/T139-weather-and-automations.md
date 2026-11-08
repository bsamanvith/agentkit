---
id: T139
title: Weather per plot, care advice and an automation engine with fixed rules and agent skills
epic: E25
status: review
depends_on: [T134, T137]
---

# T139: weather, care and automations

## Context

The owner asked (2026-11-08) for notices to every farmer about this week's
weather on their land, only when there is something to do; skills that
draw plans for when to plant and how to manage; daily or weekly runs of
fixed automations and skills; and a way to run them that scales, with
different automations for different triggers. Then: several providers and
several ways to fetch the data, because the space is fragmented. Design:
[AUTOMATIONS.md](../apps/farm/docs/AUTOMATIONS.md).

## Scope

- `apps/farm/weather`: a `Source` interface, Open-Meteo and MET Norway,
  `Fallback`; the picker `-weather` in cmd/farm.
- `apps/farm/calendar`: crop stages and care tasks from a plan.
- `apps/farm/advise`: rules over a week's forecast and the plot's crops.
- `apps/farm/automation`: automations with schedule, event and weather
  triggers; rule and skill actions; the engine; runs, notices and a store
  with a conformance suite and SQLite.
- `apps/farm/watch`: the `weather_advice` and `crop_tasks` rules and the
  built-in automations; `farm.WithObserver` events.
- `apps/farm/farmagent`: `get_week`, `crop_calendar`, `list_alerts`,
  `notify_farmer`; skills `weather-check`, `draw-crop-plan`, `manage-crop`;
  `SkillRunner`.
- `apps/farm/farmapi` routes and the web: the Weather and care tab, the
  alerts inbox and the automations page.

## Out of scope

- SMS and WhatsApp channels, the farmer's language; IMD as a provider;
  price alerts (the prices work adds them as a rule).

## Acceptance criteria

- [x] Open-Meteo answers neighbouring plots from one cached cell, a 429 pauses it, a 5xx is retried once — test: `weather.TestOpenMeteo`, `TestRateLimit`, `TestRetriesOnce`
- [x] MET Norway's hours become IST days with rain, gusts and ET0; when the first provider fails the next answers, both down is ErrUnavailable — test: `weather.TestMETNorway`, `TestFallback`, `TestHargreaves`
- [x] Crop stages and care tasks for annual and perennial plans — test: `calendar.TestStages`, `TestAnnualCalendar`, `TestPerennialCalendar`
- [x] A quiet week gives no advice; heavy rain, harvest rain, heat, cold, wind, dry weeks, fertiliser timing and disease weather each say what to do and by when — test: `advise.TestQuietWeekSaysNothing`, `TestHeavyRain`, `TestCropStageRules`, `TestRainfedDryWeek`, `TestNoPlanOnlyHarmToAnyLand`, `TestPastDaysSkipped`, `TestTodayAndTomorrow`
- [x] A schedule fires once per time, once across two processes, and once after downtime; weather and event triggers reach the right plots; failed jobs are retried — test: `automation.TestScheduleFiresOncePerTime`, `TestTwoProcessesFireOnce`, `TestWeatherTrigger`, `TestEventTrigger`, `TestRetries`
- [x] The same notice is not sent twice within six days; channels get each new notice; invalid automations are refused; built-ins only toggle — test: `automation.TestNoticesAreNotRepeated`, `TestCheck`, `TestBuiltins`
- [x] The store passes the conformance suite on SQLite — test: `automation/sqlite.TestStore`
- [x] The weekly check notifies only when action is needed; a saved plan checks the weather; weekly tasks — test: `watch.TestWeeklyWeatherOnlyWhenActionNeeded`, `TestNewPlanChecksTheWeather`, `TestWeeklyTasks`, `TestWeekAndCalendar`
- [x] The agent's tools, and a skill run as an automation that notifies at most five times and sees unread alerts — test: `farmagent.TestWatchTools`, `TestSkillRunner`
- [x] The routes, and 404s when not configured — test: `farmapi.TestWatchAndAutomationRoutes`, `TestWatchRoutesNotConfigured`
- [x] The tab, the inbox and the automations page — test: `web/apps/farm/src/alerts.test.tsx`
- [x] Against the real providers and real Claude: alerts for real plots, the skill automation, both providers comparable — manual run in a cloud session (PR description)
