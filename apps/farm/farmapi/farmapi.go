// Package farmapi is the farm app's HTTP API, under /api/: farms, boundary
// verification and the assistant. Bodies are JSON both ways; errors are
// {"error": "..."} with the status that says what kind:
//
//	400  the request is malformed (farm.ErrInvalid)
//	404  no such farm or conversation
//	422  the boundary failed verification; the body also has its "report"
//	503  the assistant is not configured (no model key)
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
package farmapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farmagent"
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

// WithCountry reports the country plots are expected in, in /api/config.
func WithCountry(code string) Option { return func(x *api) { x.country = code } }

// WithLogger logs failed requests (default slog.Default()).
func WithLogger(l *slog.Logger) Option { return func(x *api) { x.log = l } }

type api struct {
	svc       *farm.Service
	assistant Assistant
	country   string
	log       *slog.Logger
}

// New returns the API's handler; mount it at "/api/".
func New(svc *farm.Service, opts ...Option) http.Handler {
	a := &api{svc: svc, log: slog.Default()}
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
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, errorBody{Error: "no such route"})
	})
	return mux
}

type errorBody struct {
	Error  string       `json:"error"`
	Report *farm.Report `json:"report,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *api) fail(w http.ResponseWriter, r *http.Request, err error) {
	var inv *farm.InvalidError
	switch {
	case errors.As(err, &inv):
		writeJSON(w, http.StatusUnprocessableEntity, errorBody{Error: err.Error(), Report: &inv.Report})
	case errors.Is(err, farm.ErrInvalid):
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
	writeJSON(w, http.StatusOK, map[string]any{"assistant": a.assistant != nil, "country": a.country})
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
