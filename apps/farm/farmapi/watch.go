package farmapi

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/bsamanvith/agentkit/apps/farm/automation"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/watch"
)

// WithWatch serves a plot's week (forecast and advice) and crop calendar
// with w; without it, those routes are 503.
func WithWatch(w *watch.Watch) Option { return func(x *api) { x.watch = w } }

// WithAutomations serves the automations, their runs and the notices with
// e; without it, those routes are 503.
func WithAutomations(e *automation.Engine) Option { return func(x *api) { x.engine = e } }

func (a *api) watching(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.watch == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: "crop care is not configured"})
			return
		}
		h(w, r)
	}
}

func (a *api) automating(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.engine == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: "automations are not configured"})
			return
		}
		h(w, r)
	}
}

func (a *api) week(w http.ResponseWriter, r *http.Request) {
	rep, err := a.watch.Week(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (a *api) calendar(w http.ResponseWriter, r *http.Request) {
	cal, err := a.watch.Calendar(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, cal)
}

func limitOf(r *http.Request) int {
	n, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	return n
}

// scope returns the plots of the farm (holding) the request is scoped to,
// and whether it is: with sign-in, notices and runs are only of those.
func (a *api) scope(r *http.Request) ([]string, bool, error) {
	if _, ok := farm.HoldingFrom(r.Context()); !ok {
		return nil, false, nil
	}
	fs, err := a.svc.List(r.Context())
	if err != nil {
		return nil, true, err
	}
	ids := make([]string, len(fs))
	for i, f := range fs {
		ids[i] = f.ID
	}
	return ids, true, nil
}

// farmsOf returns the plots a request about plot (or, if "", every plot it
// may see) covers: nil for every plot in the app, or farm.ErrNotFound for a
// plot it may not see.
func (a *api) farmsOf(r *http.Request, plot string) ([]string, error) {
	ids, scoped, err := a.scope(r)
	switch {
	case err != nil:
		return nil, err
	case !scoped && plot == "":
		return nil, nil
	case !scoped:
		return []string{plot}, nil
	case plot == "":
		return ids, nil
	case !slices.Contains(ids, plot):
		return nil, fmt.Errorf("%w: farm %s", farm.ErrNotFound, plot)
	}
	return []string{plot}, nil
}

// listNotices lists the notices of plots (nil: all), newest first, at most
// limit.
func (a *api) listNotices(r *http.Request, plots []string, unread bool, limit int) ([]automation.Notice, error) {
	if plots == nil {
		return a.engine.Store().ListNotices(r.Context(), automation.NoticeFilter{Unread: unread, Limit: limit})
	}
	out := []automation.Notice{}
	for _, p := range plots {
		ns, err := a.engine.Store().ListNotices(r.Context(), automation.NoticeFilter{Farm: p, Unread: unread, Limit: limit})
		if err != nil {
			return nil, err
		}
		out = append(out, ns...)
	}
	slices.SortStableFunc(out, func(x, y automation.Notice) int {
		if c := y.CreatedAt.Compare(x.CreatedAt); c != 0 {
			return c
		}
		return strings.Compare(y.ID, x.ID)
	})
	if limit <= 0 {
		limit = 50
	}
	return out[:min(len(out), limit)], nil
}

func (a *api) notices(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	plots, err := a.farmsOf(r, q.Get("farm"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ns, err := a.listNotices(r, plots, q.Get("unread") == "1" || q.Get("unread") == "true", limitOf(r))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	unread, err := a.listNotices(r, plots, true, 1000)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notices": ns, "unread": len(unread)})
}

type readBody struct {
	Farm string   `json:"farm,omitempty"`
	IDs  []string `json:"ids,omitempty"`
}

func (a *api) readNotices(w http.ResponseWriter, r *http.Request) {
	var body readBody
	if err := decode(w, r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	plots, err := a.farmsOf(r, body.Farm)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if plots == nil {
		plots = []string{""}
	}
	total := 0
	for _, p := range plots {
		n, err := a.engine.Store().MarkRead(r.Context(), p, body.IDs, a.now())
		if err != nil {
			a.fail(w, r, err)
			return
		}
		total += n
	}
	writeJSON(w, http.StatusOK, map[string]int{"marked": total})
}

func (a *api) automations(w http.ResponseWriter, r *http.Request) {
	all, err := a.engine.Store().ListAutomations(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"automations": all, "rules": a.engine.Rules()})
}

func (a *api) createAutomation(w http.ResponseWriter, r *http.Request) {
	var in automation.Automation
	if err := decode(w, r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	out, err := a.engine.Create(r.Context(), in)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/automations/"+out.ID)
	writeJSON(w, http.StatusCreated, out)
}

func (a *api) updateAutomation(w http.ResponseWriter, r *http.Request) {
	var in automation.Automation
	if err := decode(w, r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	in.ID = r.PathValue("id")
	out, err := a.engine.Update(r.Context(), in)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) deleteAutomation(w http.ResponseWriter, r *http.Request) {
	if err := a.engine.Delete(r.Context(), r.PathValue("id")); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type runBody struct {
	Farm string `json:"farm,omitempty"`
}

func (a *api) runAutomation(w http.ResponseWriter, r *http.Request) {
	var body runBody
	if r.ContentLength != 0 {
		if err := decode(w, r, &body); err != nil {
			a.fail(w, r, err)
			return
		}
	}
	// A farm's member runs an automation on one of its plots; only
	// without sign-in (or as an admin, unscoped) on every plot at once.
	if _, scoped := farm.HoldingFrom(r.Context()); scoped && body.Farm == "" {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "say which plot to run it on"})
		return
	}
	if _, err := a.farmsOf(r, body.Farm); err != nil {
		a.fail(w, r, err)
		return
	}
	runs, err := a.engine.RunNow(r.Context(), r.PathValue("id"), body.Farm)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (a *api) runs(w http.ResponseWriter, r *http.Request) {
	plots, err := a.farmsOf(r, r.URL.Query().Get("farm"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if plots == nil {
		plots = []string{""}
	}
	out := []automation.Run{}
	for _, p := range plots {
		rs, err := a.engine.Store().ListRuns(r.Context(), automation.RunFilter{Automation: r.PathValue("id"), Farm: p, Limit: limitOf(r)})
		if err != nil {
			a.fail(w, r, err)
			return
		}
		out = append(out, rs...)
	}
	if len(plots) > 1 {
		slices.SortStableFunc(out, func(x, y automation.Run) int { return y.Started.Compare(x.Started) })
		limit := limitOf(r)
		if limit <= 0 {
			limit = 50
		}
		out = out[:min(len(out), limit)]
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": out})
}
