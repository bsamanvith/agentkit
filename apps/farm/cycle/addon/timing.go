package addon

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/cycle"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/planner"
	"github.com/bsamanvith/agentkit/apps/farm/prices"
)

// TimingAdvice is the timing addon's answer for one cycle: for its main
// crop, harvest-month advice (while sowing is ahead) and sell-or-hold
// advice, each only where a backtest on the area's past prices passed
// (prices.AdviseTiming). With neither, Reasons say why, for the farmer.
type TimingAdvice struct {
	Crop     string `json:"crop"`
	CropName string `json:"crop_name"`
	prices.TimingAdvice
}

// Windows gives the safe sowing window of a crop on a plot around a day:
// the recommender's (planner.Planner is one, through [PlannerWindows]).
type Windows interface {
	Window(ctx context.Context, f farm.Farm, crop, around string) (*agro.Window, error)
}

// PlannerWindows is the recommender's sowing window.
type PlannerWindows struct{ P *planner.Planner }

// Window implements Windows: the good window the day is in, or nil.
func (w PlannerWindows) Window(ctx context.Context, f farm.Farm, crop, around string) (*agro.Window, error) {
	r, err := w.P.Recommend(ctx, f.ID, planner.Ask{Crops: []string{crop}, Around: around})
	if err != nil {
		return nil, err
	}
	if len(r.Options) == 0 {
		return nil, nil
	}
	return r.Options[0].Window, nil
}

// Timing is the cycle addon that says when to sow and sell for price.
// Answers are kept a while: the price history and the window change
// slowly, and the line is read often.
type Timing struct {
	lib     *agro.Library
	hist    prices.History
	windows Windows
	today   func() time.Time
	ttl     time.Duration

	mu    sync.Mutex
	cache map[string]timed
}

type timed struct {
	at time.Time
	v  any
}

// NewTiming returns the timing addon over the price history; windows may
// be nil (then a cycle not yet sown is advised from its planned date).
func NewTiming(lib *agro.Library, hist prices.History, windows Windows, today func() time.Time) *Timing {
	return &Timing{lib: lib, hist: hist, windows: windows, today: today, ttl: 30 * time.Minute, cache: map[string]timed{}}
}

// Name implements cycle.Addon.
func (a *Timing) Name() string { return TimingName }

// For implements cycle.Addon. It is nil, saying nothing, for a plot with
// no known state, a crop with no mandi prices, or a cycle with no sowing.
func (a *Timing) For(ctx context.Context, f farm.Farm, c cycle.Cycle) (any, error) {
	crop := c.MainCrop()
	loc := f.Report.Location
	if crop == "" || loc == nil || loc.State == "" || c.Sow == "" || prices.Commodity(crop) == "" {
		return nil, nil
	}
	cr, err := a.lib.Crop(crop)
	if err != nil {
		return nil, nil
	}
	now := a.today()
	today := prices.DayOf(now)
	// The answer rests on the plot's place, the crop, the sowing and the
	// day: kept a while under them.
	key := f.ID + "|" + loc.State + "|" + loc.District + "|" + crop + "|" + c.Sow + "|" + string(c.SowKind) + "|" + today.String()
	a.mu.Lock()
	if e, ok := a.cache[key]; ok && now.Sub(e.at) < a.ttl {
		a.mu.Unlock()
		return e.v, nil
	}
	a.mu.Unlock()
	q := prices.TimingQuery{Crop: crop, State: loc.State, District: loc.District, DaysMin: int(cr.Days[0]), DaysMax: int(cr.Days[1]), Today: today}
	sow, err := prices.ParseDay(c.Sow)
	if err != nil {
		return nil, nil
	}
	q.Sown = sow
	if c.SowKind != cycle.Actual && a.windows != nil {
		// Not sown yet: the safe window around the planned day, while it
		// is open, so the sowing can be weighed too.
		w, err := a.windows.Window(ctx, f, crop, c.Sow)
		if err != nil && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if from, to, best, ok := window(w); ok && !to.Before(today) {
			q.Window, q.Sown = prices.SowingWindow{From: from, To: to, Best: best}, prices.Day{}
		}
	}
	adv, err := prices.AdviseTiming(ctx, a.hist, q)
	if errors.Is(err, prices.ErrInvalid) {
		return nil, nil // a question prices cannot ask: nothing to say
	}
	if err != nil {
		return nil, err
	}
	v := TimingAdvice{Crop: crop, CropName: cr.Name, TimingAdvice: adv}
	a.mu.Lock()
	if len(a.cache) >= 512 {
		a.cache = map[string]timed{}
	}
	a.cache[key] = timed{at: now, v: v}
	a.mu.Unlock()
	return v, nil
}

func window(w *agro.Window) (from, to, best prices.Day, ok bool) {
	if w == nil {
		return
	}
	var err error
	if from, err = prices.ParseDay(w.From); err != nil {
		return
	}
	if to, err = prices.ParseDay(w.To); err != nil {
		return
	}
	if best, err = prices.ParseDay(w.Best); err != nil || best.Before(from) || to.Before(best) {
		best = prices.Day{}
	}
	return from, to, best, !to.Before(from)
}

// Label names the addon for the farmer, in a note when it cannot answer.
func (a *Timing) Label() string { return "advice on when to sell" }
