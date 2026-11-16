package watch

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/automation"
	"github.com/bsamanvith/agentkit/apps/farm/schemes"
	"github.com/bsamanvith/agentkit/apps/farm/work"
)

// RuleInsurance is the insurance cut-off rule's name.
const RuleInsurance = "insurance_cutoffs"

// WeeklyInsurance is the built-in automation that runs it.
const WeeklyInsurance = "weekly-insurance"

// InsuranceRule warns once when a crop insurance cut-off on the plot's
// season roadmap (package schemes, through the roadmap's insurance tasks)
// is within schemes.SoonDays and the farmer has not ticked the enrolment:
// one notice per cut-off, never repeated (its key holds past the date).
// Without the roadmap it says nothing.
func (w *Watch) InsuranceRule() automation.Handler {
	return automation.HandlerFunc(func(ctx context.Context, j automation.Job) (automation.Outcome, error) {
		if w.work == nil {
			return automation.Outcome{Note: "no season roadmap"}, nil
		}
		f, err := w.plan.Farms().Get(ctx, j.Target.ID)
		if err != nil {
			return automation.Outcome{}, err
		}
		if f.Plan == nil || len(f.Plan.Zones) == 0 {
			return automation.Outcome{Note: "no crop plan"}, nil
		}
		r, err := w.work.Roadmap(ctx, f.ID)
		if errors.Is(err, work.ErrInvalid) {
			return automation.Outcome{Note: "no crop plan"}, nil
		}
		if err != nil {
			return automation.Outcome{}, err
		}
		today := w.plan.Today()
		var out automation.Outcome
		for _, t := range r.Tasks {
			if t.Source != work.FromSchemes || !t.Deadline || t.State != work.Open || t.Passed {
				continue
			}
			by, err := time.Parse(agro.DateLayout, t.Date)
			if err != nil {
				continue
			}
			left := int(by.Sub(today).Hours() / 24)
			if left < 0 || left > schemes.SoonDays {
				continue
			}
			when := fmt.Sprintf("%d days left.", left)
			switch left {
			case 0:
				when = "Today is the last day."
			case 1:
				when = "1 day left."
			}
			out.Notices = append(out.Notices, automation.Notice{
				Key: "insurance:" + f.ID + ":" + t.ID + ":" + t.Date, Severity: automation.Warning, Day: t.Date,
				Title:  t.Title + " · " + f.Name,
				Body:   when + " Tick it on Season work once you have enrolled.\n\n" + t.Detail,
				Window: time.Duration(schemes.SoonDays+2) * 24 * time.Hour,
			})
		}
		out.Note = fmt.Sprintf("%d insurance cut-offs within %d days", len(out.Notices), schemes.SoonDays)
		return out, nil
	})
}
