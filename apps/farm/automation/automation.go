// Package automation runs the farm app's automations: each has a trigger
// (a schedule, an event such as a saved plan, or a weather threshold
// checked on a schedule), a scope (which plots) and an action (a fixed
// rule in Go, or an agent skill), and its notices go to the farmer's
// in-app inbox, and to any other channel the app is given.
//
// The [Engine] is built to grow: rules and the skill runner are
// registered by name, so a new automation is data (a row in the store), not
// a code change; one firing fans out to a job per plot on a bounded pool of
// workers; each schedule time is claimed in the store, so it fires once
// however many processes share the store; a failed job is retried, then
// recorded; and a notice with the same key for the same plot is not sent
// twice within a window, so overlapping automations (a weekly check and a
// six-hourly rain watch) do not repeat each other. See
// apps/farm/docs/AUTOMATIONS.md.
package automation

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/weather"
	"github.com/bsamanvith/agentkit/pkg/trigger"
)

// Errors callers branch on.
var (
	ErrNotFound = errors.New("automation: not found")
	ErrInvalid  = errors.New("automation: invalid")
	// ErrBuiltin: a built-in automation cannot be deleted or redefined,
	// only turned on or off.
	ErrBuiltin = errors.New("automation: built-in")
)

// TriggerKind is what starts an automation.
type TriggerKind string

// The triggers.
const (
	// OnSchedule fires at the times of a cron schedule.
	OnSchedule TriggerKind = "schedule"
	// OnEvent fires when the app reports an event about a plot (a plan
	// saved, a plot added).
	OnEvent TriggerKind = "event"
	// OnWeather looks at each plot's forecast at the times of a schedule
	// and fires for the plots where a condition holds.
	OnWeather TriggerKind = "weather"
)

// Trigger is when an automation fires.
type Trigger struct {
	Kind TriggerKind `json:"kind"`
	// Schedule is a cron schedule, for OnSchedule and OnWeather: five
	// fields or @daily, @weekly..., optionally after "CRON_TZ=<zone> "
	// (package pkg/trigger's syntax).
	Schedule string `json:"schedule,omitempty"`
	// Event is the event's name, for OnEvent (farm.created,
	// farm.plan_changed, ...).
	Event string `json:"event,omitempty"`
	// Weather is the condition, for OnWeather.
	Weather *Condition `json:"weather,omitempty"`
}

// Metrics a weather condition can test.
const (
	MetricRainDay   = "rain_mm"       // any one day's rain
	MetricRainTotal = "rain_total_mm" // the days' rain together
	MetricTMax      = "tmax_c"        // any day's high
	MetricTMin      = "tmin_c"        // any day's low
	MetricGust      = "gust_kmh"      // any day's strongest gust
	MetricHumidity  = "humidity_pct"  // any day's mean humidity
	MetricET0Total  = "et0_total_mm"  // the days' reference evapotranspiration together
)

var metrics = []string{MetricRainDay, MetricRainTotal, MetricTMax, MetricTMin, MetricGust, MetricHumidity, MetricET0Total}

// Condition is a test of a plot's forecast: Metric Op Value over the
// next Days days (today included).
type Condition struct {
	Metric string  `json:"metric"`
	Op     string  `json:"op"` // ">=" or "<="
	Value  float64 `json:"value"`
	Days   int     `json:"days,omitempty"` // 1 to 16; 3 when 0
}

// days is the look-ahead.
func (c Condition) days() int {
	if c.Days == 0 {
		return 3
	}
	return c.Days
}

// Met tests the condition on a forecast's days and says why.
func (c Condition) Met(days []weather.Day) (bool, string) {
	days = days[:min(c.days(), len(days))]
	if len(days) == 0 {
		return false, "no forecast days"
	}
	cmp := func(v float64) bool {
		if c.Op == "<=" {
			return v <= c.Value
		}
		return v >= c.Value
	}
	say := func(v float64, day string) string {
		s := fmt.Sprintf("%s %.1f %s %g", c.Metric, v, c.Op, c.Value)
		if day != "" {
			s += " on " + day
		}
		return s
	}
	switch c.Metric {
	case MetricRainTotal, MetricET0Total:
		t := weather.Sum(days)
		v := t.RainMM
		if c.Metric == MetricET0Total {
			v = t.ET0MM
		}
		return cmp(v), say(v, fmt.Sprintf("the next %d days", len(days)))
	}
	for _, d := range days {
		var v float64
		switch c.Metric {
		case MetricRainDay:
			v = d.RainMM
		case MetricTMax:
			v = d.TMaxC
		case MetricTMin:
			v = d.TMinC
		case MetricGust:
			v = d.GustKmh
		case MetricHumidity:
			v = d.HumidityPct
		}
		if cmp(v) {
			return true, say(v, d.Date)
		}
	}
	return false, fmt.Sprintf("%s not %s %g in the next %d days", c.Metric, c.Op, c.Value, len(days))
}

// ActionKind is what an automation does.
type ActionKind string

// The actions.
const (
	// DoRule runs a fixed rule, registered in Go by name.
	DoRule ActionKind = "rule"
	// DoSkill runs an agent on a skill, with the prompt, for the plot.
	DoSkill ActionKind = "skill"
)

// Action is what an automation does for each plot it fires for.
type Action struct {
	Kind ActionKind `json:"kind"`
	// Rule is the rule's name, for DoRule.
	Rule string `json:"rule,omitempty"`
	// Skill is the skill's name, for DoSkill; Prompt says what to do with
	// it (optional).
	Skill  string `json:"skill,omitempty"`
	Prompt string `json:"prompt,omitempty"`
}

// Scope is which plots an automation fires for.
type Scope struct {
	// Farms are plot IDs; empty for every plot.
	Farms []string `json:"farms,omitempty"`
	// WithPlan keeps only plots with a saved crop plan.
	WithPlan bool `json:"with_plan,omitempty"`
}

// Automation is one automation.
type Automation struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	Trigger     Trigger `json:"trigger"`
	Scope       Scope   `json:"scope"`
	Action      Action  `json:"action"`
	Enabled     bool    `json:"enabled"`
	// Builtin automations come with the app: they can be turned on and
	// off, not changed or deleted.
	Builtin bool `json:"builtin,omitempty"`
	// LastDue is the last schedule time it fired for; zero before the
	// first.
	LastDue   time.Time `json:"last_due,omitzero"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// MaxPromptBytes bounds a skill action's prompt.
const MaxPromptBytes = 4 << 10

// check checks a's definition: its trigger's parts for its kind, a valid
// schedule, a known metric; the action's parts. It returns the schedule
// (nil for an event).
func (a Automation) check() (*trigger.Schedule, error) {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
	}
	if strings.TrimSpace(a.Name) == "" || len(a.Name) > 200 {
		return nil, bad("give a name of 1 to 200 bytes")
	}
	var sched *trigger.Schedule
	t := a.Trigger
	switch t.Kind {
	case OnSchedule, OnWeather:
		s, err := trigger.ParseSchedule(t.Schedule)
		if err != nil {
			return nil, bad("schedule %q: %v", t.Schedule, err)
		}
		sched = s
		if t.Kind == OnWeather {
			c := t.Weather
			switch {
			case c == nil:
				return nil, bad("a weather trigger needs a condition")
			case !slices.Contains(metrics, c.Metric):
				return nil, bad("metric %q: want one of %s", c.Metric, strings.Join(metrics, ", "))
			case c.Op != ">=" && c.Op != "<=":
				return nil, bad("op %q: want >= or <=", c.Op)
			case c.Days < 0 || c.Days > 16:
				return nil, bad("days %d: want 1 to 16", c.Days)
			}
		}
		if t.Event != "" {
			return nil, bad("a %s trigger takes no event", t.Kind)
		}
	case OnEvent:
		if strings.TrimSpace(t.Event) == "" || t.Schedule != "" || t.Weather != nil {
			return nil, bad("an event trigger takes an event and nothing else")
		}
	default:
		return nil, bad("trigger kind %q: want schedule, event or weather", t.Kind)
	}
	switch a.Action.Kind {
	case DoRule:
		if a.Action.Rule == "" || a.Action.Skill != "" {
			return nil, bad("a rule action names a rule and no skill")
		}
	case DoSkill:
		if a.Action.Skill == "" || a.Action.Rule != "" {
			return nil, bad("a skill action names a skill and no rule")
		}
		if len(a.Action.Prompt) > MaxPromptBytes {
			return nil, bad("the prompt is over %d bytes", MaxPromptBytes)
		}
	default:
		return nil, bad("action kind %q: want rule or skill", a.Action.Kind)
	}
	if len(a.Scope.Farms) > 10_000 {
		return nil, bad("a scope of more than 10,000 plots: leave it empty for all")
	}
	return sched, nil
}

// Run statuses.
const (
	RunNotified = "notified"  // the action ran and sent notices
	RunQuiet    = "no_action" // it ran (or its condition did not hold): nothing to tell the farmer
	RunFailed   = "failed"
)

// Run is one automation's job for one plot.
type Run struct {
	ID         string    `json:"id"`
	Automation string    `json:"automation"`
	Farm       string    `json:"farm"`
	Due        time.Time `json:"due"`
	// Reason is what started it: the schedule time, the event, the
	// weather condition, or a person.
	Reason   string    `json:"reason"`
	Status   string    `json:"status"`
	Notices  int       `json:"notices"`
	Note     string    `json:"note,omitempty"`
	Error    string    `json:"error,omitempty"`
	Attempts int       `json:"attempts"`
	Started  time.Time `json:"started"`
	Finished time.Time `json:"finished"`
}

// Severities of a notice, most pressing first.
const (
	Urgent  = "urgent"
	Warning = "warning"
	Advice  = "advice"
)

// Notice is a message to the farmer about one plot.
type Notice struct {
	ID         string `json:"id"`
	Farm       string `json:"farm"`
	Automation string `json:"automation"`
	Run        string `json:"run"`
	// Key identifies what it is about (a rule, a plot, a day): a notice
	// with the key of one sent within the window is not sent again.
	Key      string `json:"key"`
	Severity string `json:"severity"`
	Title    string `json:"title"`
	// Body says what to do and why.
	Body string `json:"body"`
	// Day is the day it concerns (YYYY-MM-DD), if any.
	Day       string     `json:"day,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	ReadAt    *time.Time `json:"read_at,omitempty"`
}

// RunFilter selects runs, newest first.
type RunFilter struct {
	Automation string
	Farm       string
	Limit      int // 50 when 0
}

// NoticeFilter selects notices, newest first.
type NoticeFilter struct {
	Farm   string
	Unread bool
	Limit  int // 50 when 0
}

// Store keeps automations, the schedule times they fired for, their runs
// and the notices. Implementations are safe for concurrent use and pass
// automationtest.RunStore.
type Store interface {
	CreateAutomation(ctx context.Context, a Automation) error // ErrInvalid if the ID is taken
	GetAutomation(ctx context.Context, id string) (Automation, error)
	ListAutomations(ctx context.Context) ([]Automation, error) // oldest first
	UpdateAutomation(ctx context.Context, a Automation) error
	DeleteAutomation(ctx context.Context, id string) error
	// SetLastDue sets only an automation's LastDue.
	SetLastDue(ctx context.Context, id string, due time.Time) error
	// ClaimFiring records that automation id fires for its schedule time
	// due; false if that time was claimed already (by this process or
	// another sharing the store).
	ClaimFiring(ctx context.Context, id string, due time.Time) (bool, error)
	AddRun(ctx context.Context, r Run) error
	ListRuns(ctx context.Context, f RunFilter) ([]Run, error)
	// AddNotice saves n unless a notice for the same farm with the same
	// key was saved at or after since; it reports whether n was saved.
	AddNotice(ctx context.Context, n Notice, since time.Time) (bool, error)
	ListNotices(ctx context.Context, f NoticeFilter) ([]Notice, error)
	// MarkRead marks notices read at at: those of ids, or with no ids
	// every unread notice of farm (every farm when farm is empty). It
	// returns how many it marked.
	MarkRead(ctx context.Context, farm string, ids []string, at time.Time) (int, error)
	// Prune removes runs and notices older than before, and firing claims.
	Prune(ctx context.Context, before time.Time) error
}
