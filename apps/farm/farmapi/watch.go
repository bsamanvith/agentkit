package farmapi

import (
	"net/http"
	"strconv"

	"github.com/bsamanvith/agentkit/apps/farm/automation"
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

func (a *api) notices(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ns, err := a.engine.Store().ListNotices(r.Context(), automation.NoticeFilter{Farm: q.Get("farm"), Unread: q.Get("unread") == "1" || q.Get("unread") == "true", Limit: limitOf(r)})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	unread, err := a.engine.Store().ListNotices(r.Context(), automation.NoticeFilter{Farm: q.Get("farm"), Unread: true, Limit: 1000})
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
	n, err := a.engine.Store().MarkRead(r.Context(), body.Farm, body.IDs, a.now())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"marked": n})
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
	runs, err := a.engine.RunNow(r.Context(), r.PathValue("id"), body.Farm)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": runs})
}

func (a *api) runs(w http.ResponseWriter, r *http.Request) {
	rs, err := a.engine.Store().ListRuns(r.Context(), automation.RunFilter{Automation: r.PathValue("id"), Farm: r.URL.Query().Get("farm"), Limit: limitOf(r)})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"runs": rs})
}
