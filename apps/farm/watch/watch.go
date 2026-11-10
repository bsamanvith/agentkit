// Package watch is the farm app's side of the automations (package
// automation): the plots as targets, the fixed rules (the week's weather
// advice, the week's crop tasks) and the automations that come with the
// app.
package watch

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/advise"
	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/automation"
	"github.com/bsamanvith/agentkit/apps/farm/calendar"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/planner"
	"github.com/bsamanvith/agentkit/apps/farm/weather"
	"github.com/bsamanvith/agentkit/apps/farm/work"
)

// ErrNoForecast is returned when the app has no forecast source.
var ErrNoForecast = errors.New("watch: no weather forecast source")

// The rules' names.
const (
	RuleWeather = "weather_advice"
	RuleTasks   = "crop_tasks"
)

// ForecastDays is how far ahead the weekly check looks.
const ForecastDays = 7

// Watch reads plots' weather and calendars. It is safe for concurrent use.
type Watch struct {
	plan     *planner.Planner
	forecast weather.Source // nil: no weather
	work     *work.Service  // nil: the week's tasks straight from the calendar
}

// UseRoadmap makes the week's crop work come from the plots' roadmaps, so
// that tasks the farmer has done, skipped or marked not needed are left
// out and the tasks follow the real sowing date. Call it before the
// engine starts.
func (w *Watch) UseRoadmap(s *work.Service) { w.work = s }

// New returns a Watch over the planner's farms and library, with forecasts
// from src (nil for none).
func New(p *planner.Planner, src weather.Source) *Watch { return &Watch{plan: p, forecast: src} }

// HasWeather reports whether there is a forecast source.
func (w *Watch) HasWeather() bool { return w.forecast != nil }

// Targets implements automation.Targets over the farms.
func (w *Watch) Targets(ctx context.Context, s automation.Scope) ([]automation.Target, error) {
	all, err := w.plan.Farms().List(ctx)
	if err != nil {
		return nil, err
	}
	var out []automation.Target
	for _, f := range all {
		if len(s.Farms) > 0 && !slices.Contains(s.Farms, f.ID) {
			continue
		}
		has := f.Plan != nil && len(f.Plan.Zones) > 0
		if s.WithPlan && !has {
			continue
		}
		out = append(out, automation.Target{ID: f.ID, Name: f.Name, Point: geo.Centroid(f.Boundary.Ring()), HasPlan: has})
	}
	return out, nil
}

// Week returns a plot's week: the forecast read against its crops, with
// advice only where there is something to do, and its calendar's tasks.
func (w *Watch) Week(ctx context.Context, farmID string) (advise.Report, error) {
	if w.forecast == nil {
		return advise.Report{}, ErrNoForecast
	}
	f, err := w.plan.Farms().Get(ctx, farmID)
	if err != nil {
		return advise.Report{}, err
	}
	return w.week(ctx, f, nil)
}

func (w *Watch) week(ctx context.Context, f farm.Farm, fc *weather.Forecast) (advise.Report, error) {
	if fc == nil || len(fc.Days) < ForecastDays {
		got, err := w.forecast.Forecast(ctx, geo.Centroid(f.Boundary.Ring()), ForecastDays)
		if err != nil {
			return advise.Report{}, err
		}
		fc = &got
	}
	return advise.Advise(w.plan.Library(), f, *fc, w.plan.Today()), nil
}

// Calendar returns the calendar of a plot's saved plan.
func (w *Watch) Calendar(ctx context.Context, farmID string) (calendar.Calendar, error) {
	f, err := w.plan.Farms().Get(ctx, farmID)
	if err != nil {
		return calendar.Calendar{}, err
	}
	if f.Plan == nil || len(f.Plan.Zones) == 0 {
		return calendar.Calendar{}, fmt.Errorf("%w: plot %s has no saved crop plan", agro.ErrInvalid, farmID)
	}
	return calendar.ForPlan(w.plan.Library(), *f.Plan, irrigated(f))
}

func irrigated(f farm.Farm) bool { return f.Profile != nil && len(f.Profile.Water) > 0 }

// WeatherRule is the weekly weather check: the forecast read against the
// plot's crops, a notice for each piece of advice, none in a quiet week.
func (w *Watch) WeatherRule() automation.Handler {
	return automation.HandlerFunc(func(ctx context.Context, j automation.Job) (automation.Outcome, error) {
		if w.forecast == nil {
			return automation.Outcome{}, ErrNoForecast
		}
		f, err := w.plan.Farms().Get(ctx, j.Target.ID)
		if err != nil {
			return automation.Outcome{}, err
		}
		r, err := w.week(ctx, f, j.Forecast)
		if err != nil {
			return automation.Outcome{}, err
		}
		out := automation.Outcome{Note: fmt.Sprintf("%d days, %.0f mm rain, %d crops; %d pieces of advice", len(r.Days), r.Totals.RainMM, len(r.Crops), len(r.Advice))}
		for _, a := range r.Advice {
			out.Notices = append(out.Notices, Notice(f, a))
		}
		return out, nil
	})
}

// Notice is a piece of advice as a notice to the farmer.
func Notice(f farm.Farm, a advise.Item) automation.Notice {
	sev := automation.Advice
	switch a.Severity {
	case advise.Urgent:
		sev = automation.Urgent
	case advise.Warning:
		sev = automation.Warning
	}
	return automation.Notice{Key: a.Key, Severity: sev, Title: a.Title + " · " + f.Name, Body: a.Action + "\n\nWhy: " + a.Why, Day: a.Day}
}

// TasksRule is the weekly crop work: the plan's tasks due in the next
// seven days, as one notice; none when there are none.
func (w *Watch) TasksRule() automation.Handler {
	return automation.HandlerFunc(func(ctx context.Context, j automation.Job) (automation.Outcome, error) {
		f, err := w.plan.Farms().Get(ctx, j.Target.ID)
		if err != nil {
			return automation.Outcome{}, err
		}
		if f.Plan == nil || len(f.Plan.Zones) == 0 {
			return automation.Outcome{Note: "no crop plan"}, nil
		}
		cal, err := calendar.ForPlan(w.plan.Library(), *f.Plan, irrigated(f))
		if err != nil {
			return automation.Outcome{}, err
		}
		today := w.plan.Today()
		due := calendar.Due(cal, today, today.AddDate(0, 0, 6))
		if w.work != nil {
			open, err := w.work.Due(ctx, f.ID, today, today.AddDate(0, 0, 6))
			if err != nil {
				return automation.Outcome{}, err
			}
			due = due[:0]
			for _, t := range open {
				due = append(due, calendar.Task{Date: t.Date, Kind: t.Kind, Crop: t.Crop, Zone: t.Zone, Title: t.Title, Detail: t.Detail})
			}
		}
		var lines []string
		for _, t := range due {
			if t.Kind == calendar.Scout && len(due) > 1 {
				continue // a routine walk is not worth a notice of its own
			}
			d, _ := time.Parse(agro.DateLayout, t.Date)
			line := fmt.Sprintf("• %s: %s", d.Format("Mon 2 Jan"), t.Title)
			if name := cropName(cal, t); name != "" {
				line += " (" + name + ")"
			}
			if t.Detail != "" {
				line += ". " + t.Detail
			}
			lines = append(lines, line)
		}
		if len(lines) == 0 {
			return automation.Outcome{Note: "no work due this week"}, nil
		}
		week := today.Format(agro.DateLayout)
		return automation.Outcome{Note: fmt.Sprintf("%d tasks due", len(lines)), Notices: []automation.Notice{{
			Key: "tasks:" + f.ID + ":" + week, Severity: automation.Advice, Day: week,
			Title: "This week's work · " + f.Name,
			Body:  strings.Join(lines, "\n") + "\n\n" + calendar.Note,
		}}}, nil
	})
}

func cropName(cal calendar.Calendar, t calendar.Task) string {
	for _, z := range cal.Zones {
		if z.Zone == t.Zone {
			return z.CropName
		}
	}
	return t.Crop
}

// Built-in automation IDs.
const (
	WeeklyWeather = "weekly-weather"
	RainWatch     = "rain-watch"
	NewPlan       = "new-plan-weather"
	WeeklyTasks   = "weekly-tasks"
	FieldBrief    = "weekly-field-brief"
)

// Builtins are the automations that come with the app. Schedules are in
// UTC (the binary carries no time zone database): 00:30 UTC is 06:00 in
// India. withSkills adds the agent's weekly field brief, off until the
// farmer turns it on.
func Builtins(withWeather, withSkills bool) []automation.Automation {
	var out []automation.Automation
	if withWeather {
		out = append(out,
			automation.Automation{
				ID: WeeklyWeather, Name: "Weekly weather check", Enabled: true,
				Description: "Every Monday at 6 am (India), reads each plot's 7-day forecast against its crops and alerts only when there is something to do.",
				Trigger:     automation.Trigger{Kind: automation.OnSchedule, Schedule: "30 0 * * 1"},
				Action:      automation.Action{Kind: automation.DoRule, Rule: RuleWeather},
			},
			automation.Automation{
				ID: RainWatch, Name: "Heavy rain watch", Enabled: true,
				Description: "Every 6 hours, looks for 50 mm or more of rain in a day over the next 3 days at each plot, and alerts with what to do before it.",
				Trigger: automation.Trigger{
					Kind: automation.OnWeather, Schedule: "0 */6 * * *",
					Weather: &automation.Condition{Metric: automation.MetricRainDay, Op: ">=", Value: 50, Days: 3},
				},
				Action: automation.Action{Kind: automation.DoRule, Rule: RuleWeather},
			},
			automation.Automation{
				ID: NewPlan, Name: "Weather check for a new plan", Enabled: true,
				Description: "When a plot's crop plan is saved, checks the week's weather against it at once.",
				Trigger:     automation.Trigger{Kind: automation.OnEvent, Event: farm.EventPlanChanged},
				Scope:       automation.Scope{WithPlan: true},
				Action:      automation.Action{Kind: automation.DoRule, Rule: RuleWeather},
			},
		)
	}
	out = append(out, automation.Automation{
		ID: WeeklyTasks, Name: "This week's crop work", Enabled: true,
		Description: "Every Monday at 6:30 am (India), lists the work each plot's crop calendar has for the week (weeding, top-dressing, irrigation, harvest).",
		Trigger:     automation.Trigger{Kind: automation.OnSchedule, Schedule: "0 1 * * 1"},
		Scope:       automation.Scope{WithPlan: true},
		Action:      automation.Action{Kind: automation.DoRule, Rule: RuleTasks},
	})
	if withSkills && withWeather {
		out = append(out, automation.Automation{
			ID: FieldBrief, Name: "Weekly field brief (assistant)", Enabled: false,
			Description: "Every Sunday at 6 pm (India), the assistant reads each planted plot's forecast and calendar with the weather-check skill and writes to the farmer only if something needs doing.",
			Trigger:     automation.Trigger{Kind: automation.OnSchedule, Schedule: "30 12 * * 0"},
			Scope:       automation.Scope{WithPlan: true},
			Action:      automation.Action{Kind: automation.DoSkill, Skill: "weather-check", Prompt: "Check this plot's coming week and tell the farmer only what they must do."},
		})
	}
	return out
}

// Rules returns the engine options that register the rules.
func (w *Watch) Rules() []automation.Option {
	opts := []automation.Option{automation.WithRule(RuleTasks, "The crop calendar's work due this week, as one notice.", w.TasksRule())}
	if w.forecast != nil {
		opts = append(opts, automation.WithRule(RuleWeather, "The 7-day forecast read against the plot's crops: a notice per action needed, none in a quiet week.", w.WeatherRule()))
	}
	return opts
}
