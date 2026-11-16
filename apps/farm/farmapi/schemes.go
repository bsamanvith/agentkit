package farmapi

import (
	"context"
	"net/http"

	"github.com/bsamanvith/agentkit/apps/farm/schemes"
)

// Schemes answers what crop insurance applies to a plot's current cycle;
// *addon.Schemes is one.
type Schemes interface {
	Farm(ctx context.Context, farmID string) (schemes.Answer, error)
}

// WithSchemes serves GET /api/farms/{id}/schemes with s; without it, that
// route is 503.
func WithSchemes(s Schemes) Option { return func(x *api) { x.schemes = s } }

func (a *api) schemesOf(w http.ResponseWriter, r *http.Request) {
	if a.schemes == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: "crop insurance dates are not configured"})
		return
	}
	ans, err := a.schemes.Farm(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ans)
}
