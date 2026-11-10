package farmapi

import (
	"errors"
	"net/http"

	"github.com/bsamanvith/agentkit/apps/farm/work"
)

// WithRoadmap serves plots' season roadmaps and records what was done with
// s; without it, those routes are 503.
func WithRoadmap(s *work.Service) Option { return func(x *api) { x.work = s } }

func (a *api) roadmapping(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.work == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: "the season roadmap is not configured"})
			return
		}
		h(w, r)
	}
}

func (a *api) roadmap(w http.ResponseWriter, r *http.Request) {
	rm, err := a.work.Roadmap(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rm)
}

type eventsBody struct {
	Events []work.Event `json:"events"`
}

type recorded struct {
	Added   int          `json:"added"`
	Roadmap work.Roadmap `json:"roadmap"`
}

type refusedEvent struct {
	Error   string `json:"error"`
	EventID string `json:"event_id"`
	Index   int    `json:"index"`
}

// MaxEvents is the most events one request can carry (a phone's queue).
const MaxEvents = 500

func (a *api) recordWork(w http.ResponseWriter, r *http.Request) {
	var body eventsBody
	if err := decode(w, r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	if len(body.Events) == 0 || len(body.Events) > MaxEvents {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "send between 1 and 500 events"})
		return
	}
	n, rm, err := a.work.Record(r.Context(), r.PathValue("id"), body.Events)
	var bad *work.EventError
	if errors.As(err, &bad) {
		// Which event, so a phone drops that change and resends the rest.
		writeJSON(w, http.StatusBadRequest, refusedEvent{Error: err.Error(), EventID: bad.ID, Index: bad.Index})
		return
	}
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, recorded{Added: n, Roadmap: rm})
}
