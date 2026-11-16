package addon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/cycle"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/region"
	"github.com/bsamanvith/agentkit/apps/farm/schemes"
	"github.com/bsamanvith/agentkit/apps/farm/work"
)

// SchemesName is the schemes addon's key in cycle.Position.Addons.
const SchemesName = "schemes"

// Schemes is crop insurance on the season, from the schemes of each
// plot's region pack (package schemes): a cycle addon (what applies to
// each planned cycle), the season roadmap's source of insurance tasks (an
// "Enrol for crop insurance by ..." task per scheme that may cover the
// crops, and a standing "if the crop is damaged" entry), and the current
// cycle's answer for the API and the assistant ([Schemes.Farm]).
type Schemes struct {
	base  *region.Pack
	farms *farm.Service
	today func() time.Time
	work  *work.Service
	regional
}

// NewSchemes returns the schemes addon: base is the pack of a plot with
// none of its own, farms the plots [Schemes.Farm] reads, today the date.
func NewSchemes(base *region.Pack, farms *farm.Service, today func() time.Time, opts ...Option) *Schemes {
	return &Schemes{base: base, farms: farms, today: today, regional: regionalOf(opts)}
}

// UseRoadmap makes [Schemes.Farm] read the real sowing dates from the
// season roadmap. Call it before serving.
func (s *Schemes) UseRoadmap(w *work.Service) { s.work = w }

// Name implements cycle.Addon.
func (s *Schemes) Name() string { return SchemesName }

// Label names the addon for the farmer, in a note when it cannot answer.
func (s *Schemes) Label() string { return "crop insurance dates" }

func (s *Schemes) pack(f farm.Farm) *region.Pack {
	if p := s.regional.pack(f); p != nil {
		return p
	}
	return s.base
}

// For implements cycle.Addon: nil for a cycle no scheme applies to.
func (s *Schemes) For(_ context.Context, f farm.Farm, c cycle.Cycle) (any, error) {
	p := s.pack(f)
	sow, err := time.Parse(agro.DateLayout, c.Sow)
	if p == nil || err != nil {
		return nil, nil
	}
	var crops []string
	for _, a := range c.Areas {
		crops = append(crops, a.Crop, a.With)
	}
	a := schemes.For(p, schemes.Query{Crops: crops, Sow: sow, Season: c.Season, Today: s.today()})
	if len(a.Items) == 0 {
		return nil, nil
	}
	return a, nil
}

// plot is the answer for a plot's saved plan, its zones as the roadmap
// has them (the earliest sowing, real or planned, fixes the season).
func (s *Schemes) plot(f farm.Farm, zones []work.Zone) (schemes.Answer, bool) {
	p := s.pack(f)
	if p == nil || len(zones) == 0 {
		return schemes.Answer{}, false
	}
	var crops []string
	var first time.Time
	for _, z := range zones {
		crops = append(crops, z.Crop, z.With)
		day := z.PlannedSow
		if z.Sown != "" {
			day = z.Sown
		}
		if t, err := time.Parse(agro.DateLayout, day); err == nil && (first.IsZero() || t.Before(first)) {
			first = t
		}
	}
	if first.IsZero() {
		return schemes.Answer{}, false
	}
	return schemes.For(p, schemes.Query{Crops: crops, Sow: first, Today: s.today()}), true
}

// Farm returns what applies to a plot's current cycle (its saved plan);
// an answer with no items for a plot with no plan.
func (s *Schemes) Farm(ctx context.Context, farmID string) (schemes.Answer, error) {
	f, err := s.farms.Get(ctx, farmID)
	if err != nil {
		return schemes.Answer{}, err
	}
	empty := schemes.Answer{Crops: []string{}, Items: []schemes.Item{}, Today: s.today().Format(agro.DateLayout), Confirm: schemes.Confirm}
	if f.Plan == nil || len(f.Plan.Zones) == 0 {
		return empty, nil
	}
	var zones []work.Zone
	if s.work != nil {
		if r, err := s.work.Roadmap(ctx, f.ID); err == nil {
			zones = r.Zones
		}
	}
	if zones == nil {
		for i, z := range f.Plan.Zones {
			zones = append(zones, work.Zone{Zone: i, Crop: z.Crop, With: z.With, PlannedSow: z.Sow})
		}
	}
	a, ok := s.plot(f, zones)
	if !ok {
		return empty, nil
	}
	return a, nil
}

// EnrolTaskID is the roadmap ID of a scheme's enrolment task for a
// season: the same when the sowing moves within the season, so its tick
// stays.
func EnrolTaskID(scheme, season string, year int) string {
	return fmt.Sprintf("scheme-%s-%s-%d", scheme, season, year)
}

// LossTaskID is the roadmap ID of the season's standing loss-report entry.
func LossTaskID(season string, year int) string {
	return fmt.Sprintf("scheme-loss-notice-%s-%d", season, year)
}

// Tasks implements work.TaskSource.
func (s *Schemes) Tasks(_ context.Context, f farm.Farm, zones []work.Zone) ([]work.Task, []string, error) {
	a, ok := s.plot(f, zones)
	if !ok {
		return nil, nil, nil
	}
	var out []work.Task
	first := ""
	for _, z := range zones {
		day := z.PlannedSow
		if z.Sown != "" {
			day = z.Sown
		}
		if first == "" || day < first {
			first = day
		}
	}
	for _, it := range a.Items {
		if !it.Covers() || it.By == "" || it.Status == schemes.Stale {
			continue
		}
		title := "Enrol for crop insurance by " + short(it.By)
		if it.What == "sowing" {
			title = "Sow by " + short(it.By) + " to be covered by crop insurance"
		}
		if it.Kind != "insurance" {
			title = "Enrol for " + it.Name + " by " + short(it.By)
		}
		out = append(out, work.Task{
			ID: EnrolTaskID(it.Scheme, a.Season, a.Year), Source: work.FromSchemes, Zone: work.WholePlot,
			Kind: work.Insurance, Title: title, Detail: detail(it, it.Says), Date: it.By, Deadline: true,
		})
	}
	if n, from := a.LossNotice(); n > 0 {
		out = append(out, work.Task{
			ID: LossTaskID(a.Season, a.Year), Source: work.FromSchemes, Zone: work.WholePlot, Kind: work.Insurance,
			Title:  fmt.Sprintf("If the crop is damaged: report within %d hours", n),
			Detail: detail(from, from.Note), Date: first, Standing: true,
		})
	}
	if len(out) == 0 {
		return nil, nil, nil
	}
	return out, []string{schemes.Confirm}, nil
}

func short(d string) string {
	t, err := time.Parse(agro.DateLayout, d)
	if err != nil {
		return d
	}
	return t.Format("2 January")
}

// detail is a task's line: the scheme, what it says, its source.
func detail(it schemes.Item, says string) string {
	b := strings.Builder{}
	b.WriteString(it.Name + ". " + says)
	if !strings.Contains(says, "read on") && it.Source.Read != "" {
		fmt.Fprintf(&b, " From %s, read on %s.", it.Source.Name, schemes.Long(it.Source.Read))
	}
	b.WriteString(" " + schemes.Confirm)
	return b.String()
}
