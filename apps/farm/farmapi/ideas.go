package farmapi

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/ideas"
)

// WithIdeas serves GET /api/farms/{id}/ideas with s; without it, that
// route is 503.
func WithIdeas(s *ideas.Service) Option { return func(x *api) { x.ideas = s } }

// ideasOf answers GET /api/farms/{id}/ideas?search=&limit=.
func (a *api) ideasOf(w http.ResponseWriter, r *http.Request) {
	if a.ideas == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: "the idea check is not configured"})
		return
	}
	q := r.URL.Query()
	ask := ideas.Ask{Search: q.Get("search")}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			a.fail(w, r, fmt.Errorf("%w: limit %q: want a number", agro.ErrInvalid, v))
			return
		}
		ask.Limit = n
	}
	rep, err := a.ideas.For(r.Context(), r.PathValue("id"), ask)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}
