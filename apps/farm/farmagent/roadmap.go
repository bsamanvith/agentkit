package farmagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/work"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// The season roadmap's tools.
const (
	GetRoadmapTool = "get_roadmap"
	RecordWorkTool = "record_work"
)

// RoadmapSystem is added to the system prompt with the roadmap tools.
const RoadmapSystem = `
Each plot with a saved plan has a season roadmap: every task from preparing the land to the harvest, what the farmer has done, skipped or found not needed, and their own tasks.

- get_roadmap gives it, with the tasks that are overdue (open past their date: ask the farmer what happened, never assume) and the next ones.
- When the farmer says they did something ("weeded the east plot yesterday, 4 women, 1,400 rupees"), find the task on the roadmap, say back what you will record, and record it with record_work once they agree, with the day it happened and what it took. Marking the sowing done on the real day moves every later task.
- A task they want to remember that is not on the roadmap ("repair the motor on Friday") is an added task. Never record what the farmer has not told you.`

// WithRoadmap gives the assistant the roadmap tools over s.
func WithRoadmap(s *work.Service) Option { return func(x *setup) { x.work = s } }

type roadmapIn struct {
	Farm string `json:"farm" jsonschema:"the farm's id, from list_farms"`
	All  bool   `json:"all,omitempty" jsonschema:"every task of the season; leave out for the overdue ones and the next two weeks"`
}

type roadmapOut struct {
	Farm   string      `json:"farm"`
	Today  string      `json:"today"`
	Zones  []work.Zone `json:"zones"`
	Counts work.Counts `json:"counts"`
	Tasks  []work.Task `json:"tasks"`
	Notes  []string    `json:"notes,omitempty"`
	Hidden int         `json:"tasks_left_out,omitempty"`
}

type recordIn struct {
	Farm     string        `json:"farm" jsonschema:"the farm's id, from list_farms"`
	Task     string        `json:"task,omitempty" jsonschema:"the task's id from get_roadmap; leave out to add a new task"`
	Kind     string        `json:"kind" jsonschema:"done, skipped, not_needed, reopened, added or removed (only the farmer's own tasks)"`
	Day      string        `json:"day,omitempty" jsonschema:"YYYY-MM-DD: when it was done or skipped, or when an added task is due"`
	Note     string        `json:"note,omitempty" jsonschema:"what the farmer said, short"`
	Title    string        `json:"title,omitempty" jsonschema:"an added task's title, in the farmer's words"`
	TaskKind string        `json:"task_kind,omitempty" jsonschema:"an added task's kind: prepare, sow, weed, fertilise, irrigate, scout, protect, prune, harvest or other"`
	By       string        `json:"by,omitempty" jsonschema:"who did it, if the farmer said"`
	Actuals  *work.Actuals `json:"actuals,omitempty" jsonschema:"what it took: quantity and unit, cost in rupees, workers"`
}

type recordOut struct {
	Recorded bool        `json:"recorded"`
	Task     *work.Task  `json:"task,omitempty"`
	Counts   work.Counts `json:"counts"`
}

// RoadmapTools returns get_roadmap and record_work over s.
func RoadmapTools(s *work.Service) ([]tool.Tool, error) {
	get, err := tool.Func(GetRoadmapTool, "Get a farm's season roadmap: its zones with the planned and real sowing dates, counts of open, done, skipped and overdue tasks, and the tasks (overdue ones and the next two weeks, or all with all=true), each with its id, date, phase, state and what was recorded.",
		func(ctx context.Context, in roadmapIn) (roadmapOut, error) {
			r, err := s.Roadmap(ctx, in.Farm)
			if err != nil {
				return roadmapOut{}, toolErr(GetRoadmapTool, err)
			}
			out := roadmapOut{Farm: r.Farm, Today: r.Today, Zones: r.Zones, Counts: r.Counts, Notes: r.Notes}
			for _, t := range r.Tasks {
				if in.All || t.Overdue || (t.State == work.Open && t.Date <= plusDays(r.Today, 14)) {
					out.Tasks = append(out.Tasks, t)
				} else {
					out.Hidden++
				}
			}
			return out, nil
		}, read)
	if err != nil {
		return nil, err
	}
	rec, err := tool.Func(RecordWorkTool, "Record on a farm's roadmap what the farmer did, once they have agreed to what you will record: a task done, skipped, not needed or reopened (with the day it happened and, if they said, who did it and what it took), or a new task of their own (kind added, with a title and the day it is due).",
		func(ctx context.Context, in recordIn) (recordOut, error) {
			if in.Kind == work.Added {
				// A retried call must not add the task twice.
				r, err := s.Roadmap(ctx, in.Farm)
				if err != nil {
					return recordOut{}, toolErr(RecordWorkTool, err)
				}
				for _, t := range r.Tasks {
					if t.Source == work.FromFarmer && t.Title == in.Title && t.Date == in.Day {
						return recordOut{Recorded: false, Task: &t, Counts: r.Counts}, nil
					}
				}
			}
			id, err := newID()
			if err != nil {
				return recordOut{}, err
			}
			e := work.Event{ID: id, Task: in.Task, Kind: in.Kind, Day: in.Day, Note: in.Note, Title: in.Title, TaskKind: in.TaskKind, By: in.By, Actuals: in.Actuals}
			n, r, err := s.Record(ctx, in.Farm, []work.Event{e})
			if err != nil {
				return recordOut{}, toolErr(RecordWorkTool, err)
			}
			out := recordOut{Recorded: n == 1, Counts: r.Counts}
			want := in.Task
			if in.Kind == work.Added {
				want = id
			}
			if t, ok := r.Task(want); ok {
				out.Task = &t
			}
			return out, nil
		}, write)
	if err != nil {
		return nil, err
	}
	return []tool.Tool{get, rec}, nil
}

func toolErr(name string, err error) error {
	if errors.Is(err, work.ErrInvalid) {
		return &tool.Error{Tool: name, Kind: tool.ErrInvalidInput, Err: err}
	}
	return err
}

func newID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("record_work: %w", err)
	}
	return "assistant-" + hex.EncodeToString(b), nil
}

func plusDays(day string, n int) string {
	d, err := time.Parse(agro.DateLayout, day)
	if err != nil {
		return day
	}
	return d.AddDate(0, 0, n).Format(agro.DateLayout)
}
