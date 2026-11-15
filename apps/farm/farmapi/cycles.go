package farmapi

import (
	"net/http"
	"strconv"

	"github.com/bsamanvith/agentkit/apps/farm/cycle"
)

// WithCycles serves plots' crop cycles with s; without it, those routes
// are 503.
func WithCycles(s *cycle.Service) Option { return func(x *api) { x.cycles = s } }

func (a *api) cycling(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.cycles == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: "crop cycles are not configured"})
			return
		}
		h(w, r)
	}
}

func (a *api) cycleLine(w http.ResponseWriter, r *http.Request) {
	l, err := a.cycles.Line(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (a *api) setCycle(w http.ResponseWriter, r *http.Request) {
	pos, err := strconv.Atoi(r.PathValue("pos"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "the position is 0, 1 or 2"})
		return
	}
	var ch cycle.Change
	if r.Method == http.MethodDelete {
		ch.Clear = true
	} else if err := decode(w, r, &ch); err != nil {
		a.fail(w, r, err)
		return
	}
	l, err := a.cycles.Set(r.Context(), r.PathValue("id"), pos, ch)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

type sownBody struct {
	Day string `json:"day"`
}

func (a *api) cycleSown(w http.ResponseWriter, r *http.Request) {
	var body sownBody
	if err := decode(w, r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	l, err := a.cycles.SetSown(r.Context(), r.PathValue("id"), body.Day)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

type futureBody struct {
	Future bool `json:"future"`
}

func (a *api) cycleSettings(w http.ResponseWriter, r *http.Request) {
	var body futureBody
	if err := decode(w, r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	l, err := a.cycles.SetFuture(r.Context(), r.PathValue("id"), body.Future)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (a *api) advanceCycles(w http.ResponseWriter, r *http.Request) {
	l, err := a.cycles.Advance(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}
