package farmapi

import (
	"errors"
	"net/http"

	"github.com/bsamanvith/agentkit/apps/farm/farmagent"
)

// recommendAt answers what to sow at a place with no plot: by its name
// (with the surveyor's place search) or its position.
func (a *api) recommendAt(w http.ResponseWriter, r *http.Request) {
	var in farmagent.PlaceAsk
	if err := decode(w, r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	var find farmagent.PlaceFinder // no place search: positions only
	if a.surveyor != nil {
		find = a.surveyor
	}
	out, err := farmagent.RecommendForPlace(r.Context(), a.planner, find, in)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, out)
	case errors.Is(err, farmagent.ErrNoPlace):
		writeJSON(w, http.StatusNotFound, errorBody{Error: "no place by that name; try the mandal or district"})
	case errors.Is(err, farmagent.ErrPlaceSearch):
		a.log.WarnContext(r.Context(), "farm api: place search", "err", err)
		writeJSON(w, http.StatusBadGateway, errorBody{Error: "the place search failed; try again later"})
	default:
		a.fail(w, r, err)
	}
}
