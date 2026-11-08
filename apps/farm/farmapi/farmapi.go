// Package farmapi is the farm app's HTTP API, under /api/: farms, boundary
// verification and the assistant. Bodies are JSON both ways; errors are
// {"error": "..."} with the status that says what kind:
//
//	400  the request is malformed (farm.ErrInvalid, agro.ErrInvalid), or
//	     names a soil or crop the library does not have
//	404  no such farm or conversation
//	422  the boundary failed verification, the body also has its "report";
//	     or the plan has errors, the body has its "plan_report"
//	503  the assistant is not configured (no model key), planning is not
//	     configured, or the climate could not be looked up
//
// Routes:
//
//	GET    /api/config                 what this server offers
//	GET    /api/units                  the area units
//	POST   /api/verify                 verify a draft, save nothing
//	GET    /api/farms                  list farms
//	POST   /api/farms                  create a farm (verified)
//	GET    /api/farms/{id}             one farm
//	PUT    /api/farms/{id}             replace a farm (verified again)
//	DELETE /api/farms/{id}             delete a farm
//	GET    /api/farms/{id}/geojson     the farm as a GeoJSON Feature
//	POST   /api/assistant              ask the farm assistant
//
// Crop planning (with [WithPlanner]):
//
//	GET    /api/soils                  the soil library
//	GET    /api/crops                  the crop library
//	GET    /api/farms/{id}/climate     the climate normals at the farm
//	GET    /api/farms/{id}/sun         the sun at the farm month by month (day length, sun hours, noon sun, slope)
//	PUT    /api/farms/{id}/profile     set the farm's land and water profile
//	POST   /api/farms/{id}/recommend   what to plant and when (planner.Ask)
//	POST   /api/farms/{id}/layout      cut the farm into strips {"parts": [...]}
//	POST   /api/farms/{id}/plan/check  check a plan, save nothing
//	PUT    /api/farms/{id}/plan        check and save a plan
//	DELETE /api/farms/{id}/plan        remove the plan
//
// Plot groups:
//
//	GET    /api/groups                 list groups
//	POST   /api/groups                 create a group {"name", "plots"}
//	GET    /api/groups/{id}            one group
//	PUT    /api/groups/{id}            rename it, replace its plots
//	DELETE /api/groups/{id}            delete it (its plots stay)
//
// Their plant groups and distribution (with [WithPlanner]):
//
//	GET    /api/groups/{id}/distribution          the saved one, laid out
//	POST   /api/groups/{id}/distribution/preview  lay one out, save nothing
//	PUT    /api/groups/{id}/distribution          lay out and save one
//	DELETE /api/groups/{id}/distribution          remove it
//
// The map (with [WithSurveyor]):
//
//	GET    /api/farms/{id}/survey          the saved survey, or a new one
//	POST   /api/farms/{id}/survey          survey again
//	POST   /api/farms/{id}/design/suggest  lay out a design (land.Want)
//	POST   /api/farms/{id}/design/check    check a design, save nothing
//	PUT    /api/farms/{id}/design          check and save a design
//	DELETE /api/farms/{id}/design          remove the design
//	GET    /api/locate?lat=&lng=           a point among the plots
//	GET    /api/places?q=&farm=            find a place by name
//
// Crop care (with [WithWatch]):
//
//	GET    /api/farms/{id}/week            the week's forecast, crops' stages, work due, and advice (only what needs doing)
//	GET    /api/farms/{id}/calendar        the crop calendar of the saved plan
//
// Automations and the farmer's inbox (with [WithAutomations]):
//
//	GET    /api/notices?farm=&unread=1     notices, newest first, and the unread count
//	POST   /api/notices/read               mark read {"farm", "ids"}: no ids marks all of farm (or all)
//	GET    /api/automations                the automations and the registered rules
//	POST   /api/automations                create one (automation.Automation)
//	PUT    /api/automations/{id}           replace one; a built-in one only takes "enabled"
//	DELETE /api/automations/{id}           delete one (409 for a built-in one)
//	POST   /api/automations/{id}/run       run it now {"farm"}: for one plot, or its whole scope
//	GET    /api/automations/{id}/runs      its runs, newest first (?farm=)
//
// Web research keys (with [WithResearch]):
//
//	GET    /api/research                   the providers, which are connected
//	PUT    /api/research/{provider}        save the farmer's key {"key": "..."}
//	DELETE /api/research/{provider}        remove it
package farmapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/agro"
	"github.com/bsamanvith/agentkit/apps/farm/automation"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farmagent"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/land"
	"github.com/bsamanvith/agentkit/apps/farm/planner"
	"github.com/bsamanvith/agentkit/apps/farm/planting"
	"github.com/bsamanvith/agentkit/apps/farm/research"
	"github.com/bsamanvith/agentkit/apps/farm/surveyor"
	"github.com/bsamanvith/agentkit/apps/farm/watch"
	"github.com/bsamanvith/agentkit/apps/farm/weather"
)

// MaxBody bounds a request body: a boundary of farm.MaxCorners corners
// with accuracies fits in well under it.
const MaxBody = 1 << 20

// Assistant answers the farmer's messages; *farmagent.Assistant is one.
type Assistant interface {
	Ask(ctx context.Context, conversation, message string) (farmagent.Reply, error)
}

// Option configures [New].
type Option func(*api)

// WithAssistant serves /api/assistant with a; without it, that route is 503.
func WithAssistant(a Assistant) Option { return func(x *api) { x.assistant = a } }

// WithPlanner serves the crop planning routes with p; without it, they are
// 503.
func WithPlanner(p *planner.Planner) Option { return func(x *api) { x.planner = p } }

// WithSurveyor serves the map routes with s; without it, they are 503.
func WithSurveyor(s *surveyor.Surveyor) Option { return func(x *api) { x.surveyor = s } }

// WithResearch serves the research key routes with r; without it, they
// are 503.
func WithResearch(r *research.Research) Option { return func(x *api) { x.research = r } }

// WithCountry reports the country plots are expected in, in /api/config.
func WithCountry(code string) Option { return func(x *api) { x.country = code } }

// WithLogger logs failed requests (default slog.Default()).
func WithLogger(l *slog.Logger) Option { return func(x *api) { x.log = l } }

type api struct {
	svc       *farm.Service
	assistant Assistant
	planner   *planner.Planner
	surveyor  *surveyor.Surveyor
	research  *research.Research
	watch     *watch.Watch
	engine    *automation.Engine
	country   string
	log       *slog.Logger
	now       func() time.Time
}

// New returns the API's handler; mount it at "/api/".
func New(svc *farm.Service, opts ...Option) http.Handler {
	a := &api{svc: svc, log: slog.Default(), now: time.Now}
	for _, o := range opts {
		o(a)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/config", a.config)
	mux.HandleFunc("GET /api/units", a.units)
	mux.HandleFunc("POST /api/verify", a.verify)
	mux.HandleFunc("GET /api/farms", a.list)
	mux.HandleFunc("POST /api/farms", a.create)
	mux.HandleFunc("GET /api/farms/{id}", a.get)
	mux.HandleFunc("PUT /api/farms/{id}", a.update)
	mux.HandleFunc("DELETE /api/farms/{id}", a.remove)
	mux.HandleFunc("GET /api/farms/{id}/geojson", a.geojson)
	mux.HandleFunc("POST /api/assistant", a.ask)
	mux.HandleFunc("GET /api/groups", a.groups)
	mux.HandleFunc("POST /api/groups", a.createGroup)
	mux.HandleFunc("GET /api/groups/{id}", a.getGroup)
	mux.HandleFunc("PUT /api/groups/{id}", a.updateGroup)
	mux.HandleFunc("DELETE /api/groups/{id}", a.deleteGroup)
	mux.HandleFunc("GET /api/groups/{id}/distribution", a.planning(a.distribution))
	mux.HandleFunc("POST /api/groups/{id}/distribution/preview", a.planning(a.previewDistribution))
	mux.HandleFunc("PUT /api/groups/{id}/distribution", a.planning(a.saveDistribution))
	mux.HandleFunc("DELETE /api/groups/{id}/distribution", a.planning(a.clearDistribution))
	mux.HandleFunc("GET /api/soils", a.planning(a.soils))
	mux.HandleFunc("GET /api/crops", a.planning(a.crops))
	mux.HandleFunc("GET /api/farms/{id}/climate", a.planning(a.climate))
	mux.HandleFunc("GET /api/farms/{id}/sun", a.planning(a.sun))
	mux.HandleFunc("PUT /api/farms/{id}/profile", a.planning(a.profile))
	mux.HandleFunc("POST /api/farms/{id}/recommend", a.planning(a.recommend))
	mux.HandleFunc("POST /api/farms/{id}/layout", a.planning(a.layout))
	mux.HandleFunc("POST /api/farms/{id}/plan/check", a.planning(a.checkPlan))
	mux.HandleFunc("PUT /api/farms/{id}/plan", a.planning(a.savePlan))
	mux.HandleFunc("DELETE /api/farms/{id}/plan", a.planning(a.clearPlan))
	mux.HandleFunc("GET /api/farms/{id}/survey", a.mapped(a.survey(false)))
	mux.HandleFunc("POST /api/farms/{id}/survey", a.mapped(a.survey(true)))
	mux.HandleFunc("POST /api/farms/{id}/design/suggest", a.mapped(a.suggestDesign))
	mux.HandleFunc("POST /api/farms/{id}/design/check", a.mapped(a.checkDesign))
	mux.HandleFunc("PUT /api/farms/{id}/design", a.mapped(a.saveDesign))
	mux.HandleFunc("DELETE /api/farms/{id}/design", a.mapped(a.clearDesign))
	mux.HandleFunc("GET /api/locate", a.mapped(a.locate))
	mux.HandleFunc("GET /api/places", a.mapped(a.places))
	mux.HandleFunc("GET /api/research", a.researching(a.researchStatus))
	mux.HandleFunc("PUT /api/research/{provider}", a.researching(a.connect))
	mux.HandleFunc("DELETE /api/research/{provider}", a.researching(a.disconnect))
	mux.HandleFunc("GET /api/farms/{id}/week", a.watching(a.week))
	mux.HandleFunc("GET /api/farms/{id}/calendar", a.watching(a.calendar))
	mux.HandleFunc("GET /api/notices", a.automating(a.notices))
	mux.HandleFunc("POST /api/notices/read", a.automating(a.readNotices))
	mux.HandleFunc("GET /api/automations", a.automating(a.automations))
	mux.HandleFunc("POST /api/automations", a.automating(a.createAutomation))
	mux.HandleFunc("PUT /api/automations/{id}", a.automating(a.updateAutomation))
	mux.HandleFunc("DELETE /api/automations/{id}", a.automating(a.deleteAutomation))
	mux.HandleFunc("POST /api/automations/{id}/run", a.automating(a.runAutomation))
	mux.HandleFunc("GET /api/automations/{id}/runs", a.automating(a.runs))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, errorBody{Error: "no such route"})
	})
	return mux
}

type errorBody struct {
	Error        string             `json:"error"`
	Report       *farm.Report       `json:"report,omitempty"`
	PlanReport   *agro.PlanReport   `json:"plan_report,omitempty"`
	DesignReport *land.DesignReport `json:"design_report,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *api) fail(w http.ResponseWriter, r *http.Request, err error) {
	var inv *farm.InvalidError
	var bad *planner.PlanError
	var badDesign *surveyor.DesignError
	switch {
	case errors.As(err, &badDesign):
		writeJSON(w, http.StatusUnprocessableEntity, errorBody{Error: err.Error(), DesignReport: &badDesign.Report})
	case errors.Is(err, surveyor.ErrNoGeocoder):
		writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: "place search is not configured"})
	case errors.Is(err, research.ErrUnknownProvider):
		writeJSON(w, http.StatusNotFound, errorBody{Error: err.Error()})
	case errors.As(err, &inv):
		writeJSON(w, http.StatusUnprocessableEntity, errorBody{Error: err.Error(), Report: &inv.Report})
	case errors.As(err, &bad):
		writeJSON(w, http.StatusUnprocessableEntity, errorBody{Error: err.Error(), PlanReport: &bad.Report})
	case errors.Is(err, agro.ErrNoClimate):
		a.log.WarnContext(r.Context(), "farm api: no climate", "path", r.URL.Path, "err", err)
		writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: "the climate at the plot could not be looked up; try again later"})
	case errors.Is(err, watch.ErrNoForecast), errors.Is(err, weather.ErrUnavailable):
		a.log.WarnContext(r.Context(), "farm api: no forecast", "path", r.URL.Path, "err", err)
		writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: "the weather forecast could not be had; try again later"})
	case errors.Is(err, automation.ErrBuiltin):
		writeJSON(w, http.StatusConflict, errorBody{Error: err.Error()})
	case errors.Is(err, automation.ErrInvalid):
		writeJSON(w, http.StatusBadRequest, errorBody{Error: err.Error()})
	case errors.Is(err, automation.ErrNotFound):
		writeJSON(w, http.StatusNotFound, errorBody{Error: err.Error()})
	case errors.Is(err, farm.ErrNoGroups):
		writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: err.Error()})
	case errors.Is(err, farm.ErrInvalid), errors.Is(err, agro.ErrInvalid), errors.Is(err, agro.ErrUnknown), errors.Is(err, planting.ErrInvalid):
		writeJSON(w, http.StatusBadRequest, errorBody{Error: err.Error()})
	case errors.Is(err, farm.ErrNotFound), errors.Is(err, farmagent.ErrUnknownConversation):
		writeJSON(w, http.StatusNotFound, errorBody{Error: err.Error()})
	case errors.Is(err, context.Canceled):
		// The client went away; nobody reads the answer.
	default:
		a.log.ErrorContext(r.Context(), "farm api", "method", r.Method, "path", r.URL.Path, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "internal error"})
	}
}

// decode reads a JSON body into v, refusing unknown fields and trailing data.
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, MaxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: body: %w", farm.ErrInvalid, err)
	}
	if dec.More() {
		return fmt.Errorf("%w: body: more than one JSON value", farm.ErrInvalid)
	}
	return nil
}

func (a *api) config(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"assistant": a.assistant != nil, "planner": a.planner != nil, "surveyor": a.surveyor != nil,
		"research": a.research != nil, "country": a.country,
		"weather": a.watch != nil && a.watch.HasWeather(), "automations": a.engine != nil,
	})
}

func (a *api) units(w http.ResponseWriter, _ *http.Request) {
	type unit struct {
		ID           farm.Unit `json:"id"`
		SquareMetres float64   `json:"square_metres"`
	}
	var out []unit
	for _, u := range farm.Units() {
		m, _ := u.SquareMetres()
		out = append(out, unit{u, m})
	}
	writeJSON(w, http.StatusOK, map[string]any{"units": out})
}

type verifyBody struct {
	farm.Draft
	// Except is a farm left out of the overlap check: the one being edited.
	Except string `json:"except,omitempty"`
}

func (a *api) verify(w http.ResponseWriter, r *http.Request) {
	var body verifyBody
	if err := decode(w, r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	rep, err := a.svc.Verify(r.Context(), body.Draft, body.Except)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (a *api) list(w http.ResponseWriter, r *http.Request) {
	fs, err := a.svc.List(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"farms": fs})
}

func (a *api) create(w http.ResponseWriter, r *http.Request) {
	var d farm.Draft
	if err := decode(w, r, &d); err != nil {
		a.fail(w, r, err)
		return
	}
	f, err := a.svc.Create(r.Context(), d)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/farms/"+f.ID)
	writeJSON(w, http.StatusCreated, f)
}

func (a *api) get(w http.ResponseWriter, r *http.Request) {
	f, err := a.svc.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (a *api) update(w http.ResponseWriter, r *http.Request) {
	var d farm.Draft
	if err := decode(w, r, &d); err != nil {
		a.fail(w, r, err)
		return
	}
	f, err := a.svc.Update(r.Context(), r.PathValue("id"), d)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (a *api) remove(w http.ResponseWriter, r *http.Request) {
	if err := a.svc.Delete(r.Context(), r.PathValue("id")); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// geojson writes the farm as a GeoJSON Feature (RFC 7946): a Polygon of
// the corners in the farmer's order, closed. RFC 7946 prefers anticlockwise
// outer rings, but says readers must not reject clockwise ones.
func (a *api) geojson(w http.ResponseWriter, r *http.Request) {
	f, err := a.svc.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	ring := make([][2]float64, 0, len(f.Boundary.Corners)+1)
	for _, c := range f.Boundary.Corners {
		ring = append(ring, [2]float64{c.Lng, c.Lat})
	}
	if len(ring) > 0 && ring[0] != ring[len(ring)-1] {
		ring = append(ring, ring[0])
	}
	props := map[string]any{"id": f.ID, "name": f.Name, "status": f.Report.Status}
	if m := f.Report.Measures; m != nil {
		props["area_m2"], props["acres"], props["hectares"] = m.AreaM2, m.Acres, m.Hectares
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", f.ID+".geojson"))
	w.Header().Set("Content-Type", "application/geo+json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":       "Feature",
		"properties": props,
		"geometry":   map[string]any{"type": "Polygon", "coordinates": [][][2]float64{ring}},
	})
}

type askBody struct {
	Conversation string `json:"conversation,omitempty"`
	Message      string `json:"message"`
}

func (a *api) ask(w http.ResponseWriter, r *http.Request) {
	if a.assistant == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: "the assistant is not configured: start the server with a model key"})
		return
	}
	var body askBody
	if err := decode(w, r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	reply, err := a.assistant.Ask(r.Context(), body.Conversation, body.Message)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, reply)
}

func (a *api) groups(w http.ResponseWriter, r *http.Request) {
	gs, err := a.svc.Groups(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": gs})
}

func (a *api) createGroup(w http.ResponseWriter, r *http.Request) {
	var d farm.GroupDraft
	if err := decode(w, r, &d); err != nil {
		a.fail(w, r, err)
		return
	}
	g, err := a.svc.CreateGroup(r.Context(), d)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/groups/"+g.ID)
	writeJSON(w, http.StatusCreated, g)
}

func (a *api) getGroup(w http.ResponseWriter, r *http.Request) {
	g, err := a.svc.Group(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (a *api) updateGroup(w http.ResponseWriter, r *http.Request) {
	var d farm.GroupDraft
	if err := decode(w, r, &d); err != nil {
		a.fail(w, r, err)
		return
	}
	g, err := a.svc.UpdateGroup(r.Context(), r.PathValue("id"), d)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (a *api) deleteGroup(w http.ResponseWriter, r *http.Request) {
	if err := a.svc.DeleteGroup(r.Context(), r.PathValue("id")); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) distribution(w http.ResponseWriter, r *http.Request) {
	res, err := a.planner.Distribute(r.Context(), r.PathValue("id"), nil)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (a *api) previewDistribution(w http.ResponseWriter, r *http.Request) {
	var d planting.Distribution
	if err := decode(w, r, &d); err != nil {
		a.fail(w, r, err)
		return
	}
	res, err := a.planner.Distribute(r.Context(), r.PathValue("id"), &d)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type distributionBody struct {
	Group  farm.Group      `json:"group"`
	Result planting.Result `json:"result"`
}

func (a *api) saveDistribution(w http.ResponseWriter, r *http.Request) {
	var d planting.Distribution
	if err := decode(w, r, &d); err != nil {
		a.fail(w, r, err)
		return
	}
	g, res, err := a.planner.SaveDistribution(r.Context(), r.PathValue("id"), d)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, distributionBody{g, res})
}

func (a *api) clearDistribution(w http.ResponseWriter, r *http.Request) {
	g, err := a.planner.ClearDistribution(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// planning answers 503 when the planner is not configured.
func (a *api) planning(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.planner == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: "crop planning is not configured"})
			return
		}
		h(w, r)
	}
}

func (a *api) soils(w http.ResponseWriter, _ *http.Request) {
	lib := a.planner.Library()
	writeJSON(w, http.StatusOK, map[string]any{"soils": lib.Soils(), "source": lib.SoilsSource, "textures": agro.Textures()})
}

func (a *api) crops(w http.ResponseWriter, _ *http.Request) {
	lib := a.planner.Library()
	writeJSON(w, http.StatusOK, map[string]any{"crops": lib.Crops(), "source": lib.CropsSource, "water": agro.WaterKinds()})
}

func (a *api) climate(w http.ResponseWriter, r *http.Request) {
	c, err := a.planner.Climate(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (a *api) sun(w http.ResponseWriter, r *http.Request) {
	rep, err := a.planner.Sun(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (a *api) profile(w http.ResponseWriter, r *http.Request) {
	var p agro.Profile
	if err := decode(w, r, &p); err != nil {
		a.fail(w, r, err)
		return
	}
	f, err := a.planner.SetProfile(r.Context(), r.PathValue("id"), p)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (a *api) recommend(w http.ResponseWriter, r *http.Request) {
	var ask planner.Ask
	if err := decode(w, r, &ask); err != nil {
		a.fail(w, r, err)
		return
	}
	rec, err := a.planner.Recommend(r.Context(), r.PathValue("id"), ask)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

type layoutBody struct {
	Parts []agro.Part `json:"parts"`
}

type planBody struct {
	Plan   agro.Plan       `json:"plan"`
	Report agro.PlanReport `json:"report"`
}

func (a *api) layout(w http.ResponseWriter, r *http.Request) {
	var body layoutBody
	if err := decode(w, r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	plan, rep, err := a.planner.Layout(r.Context(), r.PathValue("id"), body.Parts)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, planBody{plan, rep})
}

func (a *api) checkPlan(w http.ResponseWriter, r *http.Request) {
	var plan agro.Plan
	if err := decode(w, r, &plan); err != nil {
		a.fail(w, r, err)
		return
	}
	rep, err := a.planner.Check(r.Context(), r.PathValue("id"), plan)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (a *api) savePlan(w http.ResponseWriter, r *http.Request) {
	var plan agro.Plan
	if err := decode(w, r, &plan); err != nil {
		a.fail(w, r, err)
		return
	}
	f, err := a.planner.SavePlan(r.Context(), r.PathValue("id"), plan)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (a *api) clearPlan(w http.ResponseWriter, r *http.Request) {
	f, err := a.planner.ClearPlan(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

// mapped answers 503 when the surveyor is not configured.
func (a *api) mapped(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.surveyor == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: "the map is not configured"})
			return
		}
		h(w, r)
	}
}

// researching answers 503 when research is not configured.
func (a *api) researching(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.research == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: "web research is not configured"})
			return
		}
		h(w, r)
	}
}

func (a *api) survey(refresh bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sv, err := a.surveyor.Survey(r.Context(), r.PathValue("id"), refresh)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, sv)
	}
}

type designBody struct {
	Design land.Design       `json:"design"`
	Report land.DesignReport `json:"report"`
}

func (a *api) suggestDesign(w http.ResponseWriter, r *http.Request) {
	var want land.Want
	if err := decode(w, r, &want); err != nil {
		a.fail(w, r, err)
		return
	}
	d, rep, err := a.surveyor.Suggest(r.Context(), r.PathValue("id"), want)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, designBody{d, rep})
}

func (a *api) checkDesign(w http.ResponseWriter, r *http.Request) {
	var d land.Design
	if err := decode(w, r, &d); err != nil {
		a.fail(w, r, err)
		return
	}
	rep, err := a.surveyor.CheckDesign(r.Context(), r.PathValue("id"), d)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

func (a *api) saveDesign(w http.ResponseWriter, r *http.Request) {
	var d land.Design
	if err := decode(w, r, &d); err != nil {
		a.fail(w, r, err)
		return
	}
	f, err := a.surveyor.SaveDesign(r.Context(), r.PathValue("id"), d)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (a *api) clearDesign(w http.ResponseWriter, r *http.Request) {
	f, err := a.surveyor.ClearDesign(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (a *api) locate(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lat, err1 := strconv.ParseFloat(q.Get("lat"), 64)
	lng, err2 := strconv.ParseFloat(q.Get("lng"), 64)
	if err1 != nil || err2 != nil {
		a.fail(w, r, fmt.Errorf("%w: lat and lng must be decimal degrees", farm.ErrInvalid))
		return
	}
	l, err := a.surveyor.Locate(r.Context(), geo.Point{Lat: lat, Lng: lng})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

func (a *api) places(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("q") == "" {
		a.fail(w, r, fmt.Errorf("%w: give the place's name as q", farm.ErrInvalid))
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	found, err := a.surveyor.FindPlace(r.Context(), q.Get("q"), q.Get("farm"), limit)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]any{"places": found})
	case errors.Is(err, farm.ErrNotFound), errors.Is(err, surveyor.ErrNoGeocoder), errors.Is(err, context.Canceled):
		a.fail(w, r, err)
	default: // the geocoder failed
		a.log.WarnContext(r.Context(), "farm api: place search", "err", err)
		writeJSON(w, http.StatusBadGateway, errorBody{Error: "the place search failed; try again later"})
	}
}

func (a *api) researchStatus(w http.ResponseWriter, r *http.Request) {
	st, err := a.research.Status(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"providers": st})
}

type keyBody struct {
	Key string `json:"key"`
}

func (a *api) connect(w http.ResponseWriter, r *http.Request) {
	var body keyBody
	if err := decode(w, r, &body); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.research.Connect(r.Context(), r.PathValue("provider"), body.Key); err != nil {
		if errors.Is(err, research.ErrUnknownProvider) {
			a.fail(w, r, err)
			return
		}
		writeJSON(w, http.StatusBadRequest, errorBody{Error: err.Error()})
		return
	}
	a.researchStatus(w, r)
}

func (a *api) disconnect(w http.ResponseWriter, r *http.Request) {
	if err := a.research.Disconnect(r.Context(), r.PathValue("provider")); err != nil {
		a.fail(w, r, err)
		return
	}
	a.researchStatus(w, r)
}
