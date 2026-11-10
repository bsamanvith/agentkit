package farmagent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/advise"
	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/automation"
	"github.com/bsamanvith/agentkit/apps/farm/calendar"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/watch"
	"github.com/bsamanvith/agentkit/pkg/harness"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// The weather and crop-care tools' names.
const (
	GetWeekTool      = "get_week"
	CropCalendarTool = "crop_calendar"
	ListAlertsTool   = "list_alerts"
	NotifyTool       = "notify_farmer"
)

// WatchSystem is added to the system prompt with the weather and
// crop-care tools.
const WatchSystem = `
You also look after the farmer's crops week by week.

- The week's forecast at a plot, read against its crops' stages, comes from get_week: its advice lists only what needs doing, and an empty list means a quiet week. The forecast and the advice are the tools'; do not forecast weather yourself.
- A crop's calendar (when to prepare, sow, weed, top-dress, irrigate, scout and harvest) comes from crop_calendar, for the saved plan or for crops and sowing dates the farmer is considering. Its timings are general rules for the kind of crop: say so, and that the seed packet or local package of practices has the variety's.
- list_alerts shows what the app has already told the farmer about a plot.`

// WithWatch gives the assistant the weather and crop-care tools over w
// (get_week, crop_calendar) and, with a store, list_alerts.
func WithWatch(w *watch.Watch, store automation.Store) Option {
	return func(s *setup) { s.watch, s.alerts = w, store }
}

type calendarPart struct {
	Crop string `json:"crop" jsonschema:"a crop id, from list_crops"`
	With string `json:"with,omitempty" jsonschema:"an intercrop's id, sown with it"`
	Sow  string `json:"sow" jsonschema:"the sowing (or planting) date, YYYY-MM-DD"`
}

type calendarIn struct {
	Farm  string         `json:"farm" jsonschema:"the farm's id, from list_farms"`
	Parts []calendarPart `json:"parts,omitempty" jsonschema:"crops and sowing dates to draw a calendar for; leave out for the farm's saved plan"`
	From  string         `json:"from,omitempty" jsonschema:"only tasks on or after this date (YYYY-MM-DD)"`
	To    string         `json:"to,omitempty" jsonschema:"only tasks on or before this date (YYYY-MM-DD)"`
}

type calendarOut struct {
	Farm  string `json:"farm"`
	Today string `json:"today"`
	calendar.Calendar
	Saved bool `json:"saved_plan"`
}

type alertsIn struct {
	Farm   string `json:"farm" jsonschema:"the farm's id, from list_farms"`
	Unread bool   `json:"unread,omitempty" jsonschema:"only alerts the farmer has not read"`
}

// WatchTools returns get_week and crop_calendar over w, and list_alerts
// over store when it is not nil.
func WatchTools(w *watch.Watch, p plannerish, store automation.Store) ([]tool.Tool, error) {
	var tools []tool.Tool
	add := func(t tool.Tool, err error) error {
		if err == nil {
			tools = append(tools, t)
		}
		return err
	}
	errs := []error{
		add(tool.Func(CropCalendarTool, "Draw a crop calendar for a farm: for each zone of its saved plan (or the crops and sowing dates given in parts) the work by date and days after sowing, with the crop's stage: land preparation, seed treatment, sowing, gap filling, weeding, nitrogen top-dressing, critical irrigations, scouting and the harvest window. Give from/to for a stretch such as this week.",
			func(ctx context.Context, in calendarIn) (calendarOut, error) {
				f, err := p.Farms().Get(ctx, in.Farm)
				if err != nil {
					return calendarOut{}, err
				}
				out := calendarOut{Farm: f.ID, Today: p.Today().Format(agro.DateLayout)}
				plan := agro.Plan{}
				if len(in.Parts) > 0 {
					for _, part := range in.Parts {
						plan.Zones = append(plan.Zones, agro.Zone{Crop: part.Crop, With: part.With, Sow: part.Sow})
					}
				} else {
					if f.Plan == nil || len(f.Plan.Zones) == 0 {
						return calendarOut{}, &tool.Error{Tool: CropCalendarTool, Kind: tool.ErrInvalidInput, Err: fmt.Errorf("farm %s has no saved plan: give parts", in.Farm)}
					}
					plan, out.Saved = *f.Plan, true
				}
				cal, err := calendar.ForPlan(p.Library(), plan, f.Profile != nil && len(f.Profile.Water) > 0)
				if err != nil {
					return calendarOut{}, err
				}
				if in.From != "" || in.To != "" {
					from, to := time.Time{}, time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)
					if in.From != "" {
						if from, err = time.Parse(agro.DateLayout, in.From); err != nil {
							return calendarOut{}, &tool.Error{Tool: CropCalendarTool, Kind: tool.ErrInvalidInput, Err: err}
						}
					}
					if in.To != "" {
						if to, err = time.Parse(agro.DateLayout, in.To); err != nil {
							return calendarOut{}, &tool.Error{Tool: CropCalendarTool, Kind: tool.ErrInvalidInput, Err: err}
						}
					}
					for i := range cal.Zones {
						cal.Zones[i].Tasks = slices.DeleteFunc(cal.Zones[i].Tasks, func(t calendar.Task) bool {
							return t.Date < from.Format(agro.DateLayout) || t.Date > to.Format(agro.DateLayout)
						})
					}
				}
				out.Calendar = cal
				return out, nil
			}, read)),
	}
	if w != nil && w.HasWeather() {
		errs = append(errs, add(tool.Func(GetWeekTool, "Get a farm's coming week: the daily forecast (rain, temperature, wind, humidity, evapotranspiration), each crop of its plan with its stage today, the plan's work due this week, and advice: only what the farmer must do because of the weather (heavy rain, harvest before rain, wind, heat, cold, a dry week at a critical stage, fertiliser timing, disease weather), each with a severity, a day, the action and why. Empty advice means nothing needs doing.",
			func(ctx context.Context, in farmIn) (advise.Report, error) { return w.Week(ctx, in.Farm) }, read)))
	}
	if store != nil {
		errs = append(errs, add(tool.Func(ListAlertsTool, "List the alerts the app has sent the farmer about a farm (newest first): weather warnings, the week's work, the assistant's notes, with whether they were read.",
			func(ctx context.Context, in alertsIn) (map[string][]automation.Notice, error) {
				ns, err := listAlerts(ctx, p, store, in)
				return map[string][]automation.Notice{"alerts": ns}, err
			}, read)))
	}
	return tools, errors.Join(errs...)
}

// listAlerts lists a farm's notices, or every farm's; with sign-in (a
// holding in ctx), only the plots ctx may see, so a farmer never reads
// another farm's alerts through the assistant or MCP.
func listAlerts(ctx context.Context, p plannerish, store automation.Store, in alertsIn) ([]automation.Notice, error) {
	const limit = 20
	if _, scoped := farm.HoldingFrom(ctx); !scoped {
		return store.ListNotices(ctx, automation.NoticeFilter{Farm: in.Farm, Unread: in.Unread, Limit: limit})
	}
	var plots []string
	if in.Farm != "" {
		if _, err := p.Farms().Get(ctx, in.Farm); err != nil {
			return nil, err
		}
		plots = []string{in.Farm}
	} else {
		fs, err := p.Farms().List(ctx)
		if err != nil {
			return nil, err
		}
		for _, f := range fs {
			plots = append(plots, f.ID)
		}
	}
	out := []automation.Notice{}
	for _, id := range plots {
		ns, err := store.ListNotices(ctx, automation.NoticeFilter{Farm: id, Unread: in.Unread, Limit: limit})
		if err != nil {
			return nil, err
		}
		out = append(out, ns...)
	}
	slices.SortStableFunc(out, func(x, y automation.Notice) int { return y.CreatedAt.Compare(x.CreatedAt) })
	return out[:min(len(out), limit)], nil
}

// plannerish is what the calendar tool needs of the planner.
type plannerish interface {
	Farms() *farm.Service
	Library() *agro.Library
	Today() time.Time
}

// SkillRunnerSystem is added to the system prompt of automation runs.
const SkillRunnerSystem = `

You are running as a background automation, not in a chat: no farmer is reading your reply. Tell the farmer something only with notify_farmer, and only when they need to act; a quiet week needs no notice. Each notice: a short title, then what to do and why in two or three plain sentences, in the language the farmer uses in the app (English unless the plot's name or notes say otherwise). Your final reply is a one-line log of what you found.`

type notifyIn struct {
	Farm     string `json:"farm" jsonschema:"the farm's id"`
	Severity string `json:"severity" jsonschema:"urgent (act within a day or two or lose crop), warning (plan this week's work around it) or advice"`
	Title    string `json:"title" jsonschema:"a short title, under 60 characters"`
	Body     string `json:"body" jsonschema:"what to do and why, two or three plain sentences"`
	Day      string `json:"day,omitempty" jsonschema:"the day it concerns, YYYY-MM-DD"`
}

type collector struct {
	mu      sync.Mutex
	farm    string
	notices []automation.Notice
}

type collectorKey struct{}

// SkillRunner runs automation actions of kind skill: an agent with the
// assistant's tools and skills plus notify_farmer, one fresh transcript a
// job. It is an automation.Handler.
type SkillRunner struct {
	h      *harness.Harness
	alerts automation.Store // nil: the prompt lists no earlier alerts
}

// NewSkillRunner returns a skill runner on model with the assistant's
// options (the skills must be among them).
func NewSkillRunner(model llm.Model, svc *farm.Service, opts ...Option) (*SkillRunner, error) {
	var s setup
	for _, o := range opts {
		o(&s)
	}
	if s.skills == nil {
		return nil, errors.New("farmagent: a skill runner needs WithSkills")
	}
	gs, err := groups(svc, s)
	if err != nil {
		return nil, err
	}
	var tools []tool.Tool
	var system strings.Builder
	for _, g := range gs {
		tools = append(tools, g.tools...)
		system.WriteString(g.system)
	}
	system.WriteString(SkillRunnerSystem)
	notify, err := tool.Func(NotifyTool, "Send the farmer an alert about this farm, in the app's inbox. Only when they need to act.",
		func(ctx context.Context, in notifyIn) (map[string]string, error) {
			c, _ := ctx.Value(collectorKey{}).(*collector)
			if c == nil {
				return nil, errors.New("notify_farmer works only in an automation run")
			}
			if in.Farm != c.farm {
				return nil, &tool.Error{Tool: NotifyTool, Kind: tool.ErrInvalidInput, Err: fmt.Errorf("this run is for farm %s, not %s", c.farm, in.Farm)}
			}
			sev := in.Severity
			if sev != automation.Urgent && sev != automation.Warning {
				sev = automation.Advice
			}
			if strings.TrimSpace(in.Title) == "" || strings.TrimSpace(in.Body) == "" {
				return nil, &tool.Error{Tool: NotifyTool, Kind: tool.ErrInvalidInput, Err: errors.New("give a title and a body")}
			}
			c.mu.Lock()
			defer c.mu.Unlock()
			if len(c.notices) >= 5 {
				return nil, &tool.Error{Tool: NotifyTool, Kind: tool.ErrInvalidInput, Err: errors.New("five notices is the most for one run: put the rest together")}
			}
			c.notices = append(c.notices, automation.Notice{
				Severity: sev, Title: in.Title, Body: in.Body, Day: in.Day,
				Key: "skill:" + strings.ToLower(strings.TrimSpace(in.Title)) + ":" + in.Day,
			})
			return map[string]string{"status": "queued for the farmer"}, nil
		}, write)
	if err != nil {
		return nil, err
	}
	reg, err := tool.NewRegistry(append(tools, notify))
	if err != nil {
		return nil, err
	}
	h, err := harness.New(model, reg, append([]harness.Option{harness.WithSystem(system.String()), harness.WithMaxTurns(16)}, s.harness...)...)
	if err != nil {
		return nil, err
	}
	return &SkillRunner{h: h, alerts: s.alerts}, nil
}

// Handle implements automation.Handler: it runs the job's skill for the
// job's plot and returns the notices the agent sent.
func (r *SkillRunner) Handle(ctx context.Context, j automation.Job) (automation.Outcome, error) {
	c := &collector{farm: j.Target.ID}
	ctx = context.WithValue(ctx, collectorKey{}, c)
	prompt := fmt.Sprintf("Automation %q (%s) for farm %s (%q). Load the skill %q with skill_load and follow it for this farm.",
		j.Automation.Name, j.Reason, j.Target.ID, j.Target.Name, j.Automation.Action.Skill)
	if p := strings.TrimSpace(j.Automation.Action.Prompt); p != "" {
		prompt += "\n\nThe automation's instructions: " + p
	}
	if r.alerts != nil {
		// What the farmer has not read yet: do not say it again in other words.
		unread, err := r.alerts.ListNotices(ctx, automation.NoticeFilter{Farm: j.Target.ID, Unread: true, Limit: 10})
		if err == nil && len(unread) > 0 {
			prompt += "\n\nAlerts the farmer has not read yet (do not repeat these, even in other words):"
			for _, n := range unread {
				prompt += "\n- " + n.Title
			}
		}
	}
	res, err := harness.Collect(r.h.Run(ctx, harness.Input{Message: harness.UserText(prompt)}))
	if err != nil {
		return automation.Outcome{}, fmt.Errorf("farmagent: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	note := strings.TrimSpace(res.Final.Text())
	if len(note) > 500 {
		note = note[:500]
	}
	return automation.Outcome{Notices: c.notices, Note: note}, nil
}
