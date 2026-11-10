// Package farmauth puts the farm app behind sign-in: pkg/auth/accounts'
// lanes and sessions at /auth/, the farms (holdings) and members of
// package members at /api/me, /api/holdings/..., /api/join and
// /api/assist, and [Auth.Protect] in front of the app's API and its MCP
// server.
//
// A protected request needs a session (401 otherwise) and, when it changes
// something, the session's CSRF token. It acts for one farm: the one named
// in the X-Farm-Holding header (or the "holding" query parameter, for
// links), else the account's first. Its context is scoped to that farm
// (farm.WithHolding), read-only when the role cannot write
// (farm.WithReadOnly), and carries the account as the tenancy user (each
// farmer's own research keys). Changing the automations themselves is an
// admin's; running one on a plot is any member's.
package farmauth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/members"
	"github.com/bsamanvith/agentkit/pkg/auth/accounts"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// HoldingHeader names the farm a request acts for.
const HoldingHeader = "X-Farm-Holding"

// Prefix is where the sign-in lanes are served.
const Prefix = "/auth"

// Auth is the farm app's sign-in and access.
type Auth struct {
	acc *accounts.Service
	mem *members.Service
	log *slog.Logger
}

// New returns the app's Auth over its accounts and members.
func New(acc *accounts.Service, mem *members.Service, log *slog.Logger) *Auth {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Auth{acc: acc, mem: mem, log: log}
}

// Access is what a protected request may do: who, for which farm, as what.
type Access struct {
	Account accounts.Account
	Holding members.Holding
	Role    members.Role
	// Farms are all the account's farms, first joined first.
	Farms []members.Membership
	CSRF  string
}

type accessKey struct{}

// From returns the request's [Access], set by [Auth.Protect].
func From(ctx context.Context) (Access, bool) {
	a, ok := ctx.Value(accessKey{}).(Access)
	return a, ok
}

// public are the API routes that answer without a session: what the web
// app needs to draw its sign-in screen.
var public = []string{"/api/config", "/api/units"}

// Protect lets through only signed-in requests, scoped to their farm; the
// public routes pass as they are.
func (a *Auth) Protect(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && slices.Contains(public, r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		ac, err := a.access(r)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		if isAutomationChange(r) && !slices.Contains(ac.Account.Roles, accounts.RoleAdmin) {
			a.fail(w, r, members.ErrForbidden)
			return
		}
		ctx := context.WithValue(r.Context(), accessKey{}, ac)
		ctx = farm.WithHolding(ctx, ac.Holding.ID)
		if !members.Can(ac.Role, members.Write) {
			ctx = farm.WithReadOnly(ctx)
		}
		ctx = tenancy.WithUser(ctx, ac.Account.ID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// isAutomationChange is a change to the app's automations (not a run of
// one): they are shared by every farm.
func isAutomationChange(r *http.Request) bool {
	if r.Method == http.MethodGet || !strings.HasPrefix(r.URL.Path, "/api/automations") {
		return false
	}
	return !strings.HasSuffix(r.URL.Path, "/run")
}

// access resolves the request's session, CSRF and farm.
func (a *Auth) access(r *http.Request) (Access, error) {
	if err := a.acc.CheckCSRF(r); err != nil {
		return Access{}, err
	}
	me, err := a.acc.Me(r)
	if err != nil {
		return Access{}, err
	}
	farms, err := a.mem.Mine(r.Context(), me.Account)
	if err != nil {
		return Access{}, err
	}
	ac := Access{Account: me.Account, Farms: farms, CSRF: me.CSRF}
	want := r.Header.Get(HoldingHeader)
	if want == "" {
		want = r.URL.Query().Get("holding")
	}
	i := 0
	if want != "" {
		i = slices.IndexFunc(farms, func(m members.Membership) bool { return m.Holding.ID == want })
		if i < 0 {
			return Access{}, members.ErrNotFound
		}
	}
	ac.Holding, ac.Role = farms[i].Holding, farms[i].Role
	return ac, nil
}

type errorBody struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (a *Auth) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, accounts.ErrNoSession):
		writeJSON(w, http.StatusUnauthorized, errorBody{Error: "sign in first", Code: "no_session"})
	case errors.Is(err, accounts.ErrCSRF):
		writeJSON(w, http.StatusForbidden, errorBody{Error: "refused: the request did not come from this app", Code: "csrf"})
	case errors.Is(err, members.ErrForbidden):
		writeJSON(w, http.StatusForbidden, errorBody{Error: err.Error(), Code: "forbidden"})
	case errors.Is(err, members.ErrLastOwner):
		writeJSON(w, http.StatusConflict, errorBody{Error: err.Error(), Code: "last_owner"})
	case errors.Is(err, members.ErrNotFound), errors.Is(err, accounts.ErrNotFound):
		writeJSON(w, http.StatusNotFound, errorBody{Error: "no such farm or member", Code: "not_found"})
	case errors.Is(err, members.ErrInvalid), errors.Is(err, accounts.ErrInvalid):
		writeJSON(w, http.StatusBadRequest, errorBody{Error: err.Error(), Code: "invalid"})
	case errors.Is(err, accounts.ErrBadSecret):
		writeJSON(w, http.StatusUnauthorized, errorBody{Error: "wrong, used or expired code", Code: "bad_secret"})
	default:
		a.log.ErrorContext(r.Context(), "farm auth", "method", r.Method, "path", r.URL.Path, "err", err)
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "internal error"})
	}
}

// Register mounts the sign-in lanes at [Prefix] and the account and farm
// routes on mux. The account routes are protected themselves; mount the
// rest of the API behind [Auth.Protect].
//
//	GET    /api/me                                     account, its farms, the farm acted for, CSRF token
//	PATCH  /api/holdings/{id}                          {"name"}: rename a farm (owner)
//	GET    /api/holdings/{id}/members                  the farm's members
//	POST   /api/holdings/{id}/invites                  {"role"}: a code that adds whoever types it (owner)
//	PUT    /api/holdings/{id}/members/{account}        {"role"} (owner)
//	DELETE /api/holdings/{id}/members/{account}        remove (owner), or leave (oneself)
//	POST   /api/holdings/{id}/members/{account}/code   a new sign-in code for a member who cannot get in (the farm's partner)
//	POST   /api/join                                   {"code"}: join a farm
//	POST   /api/assist                                 {"name", "phone", "farm"}: sign a farmer up (partners)
func (a *Auth) Register(mux *http.ServeMux) {
	mux.Handle(Prefix+"/", a.acc.Handler(Prefix))
	mux.Handle("GET /api/me", a.Protect(http.HandlerFunc(a.me)))
	mux.Handle("PATCH /api/holdings/{id}", a.Protect(http.HandlerFunc(a.rename)))
	mux.Handle("GET /api/holdings/{id}/members", a.Protect(http.HandlerFunc(a.members)))
	mux.Handle("POST /api/holdings/{id}/invites", a.Protect(http.HandlerFunc(a.invite)))
	mux.Handle("PUT /api/holdings/{id}/members/{account}", a.Protect(http.HandlerFunc(a.setRole)))
	mux.Handle("DELETE /api/holdings/{id}/members/{account}", a.Protect(http.HandlerFunc(a.remove)))
	mux.Handle("POST /api/holdings/{id}/members/{account}/code", a.Protect(http.HandlerFunc(a.signInCode)))
	mux.Handle("POST /api/join", a.Protect(http.HandlerFunc(a.join)))
	mux.Handle("POST /api/assist", a.Protect(http.HandlerFunc(a.assist)))
}

func decode(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errors.Join(members.ErrInvalid, errors.New("the body is not the JSON this route takes"))
	}
	return nil
}

type meBody struct {
	Account accounts.Account     `json:"account"`
	Farms   []members.Membership `json:"farms"`
	Holding members.Holding      `json:"holding"`
	Role    members.Role         `json:"role"`
	// Partner says whether the account may sign farmers up.
	Partner bool   `json:"partner"`
	Admin   bool   `json:"admin"`
	CSRF    string `json:"csrf"`
}

func (a *Auth) me(w http.ResponseWriter, r *http.Request) {
	ac, _ := From(r.Context())
	writeJSON(w, http.StatusOK, meBody{
		Account: ac.Account, Farms: ac.Farms, Holding: ac.Holding, Role: ac.Role, CSRF: ac.CSRF,
		Partner: slices.Contains(ac.Account.Roles, members.AccountRolePartner) || slices.Contains(ac.Account.Roles, accounts.RoleAdmin),
		Admin:   slices.Contains(ac.Account.Roles, accounts.RoleAdmin),
	})
}

func (a *Auth) rename(w http.ResponseWriter, r *http.Request) {
	ac, _ := From(r.Context())
	var in struct {
		Name string `json:"name"`
	}
	if err := decode(w, r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	h, err := a.mem.Rename(r.Context(), ac.Account.ID, r.PathValue("id"), in.Name)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func (a *Auth) members(w http.ResponseWriter, r *http.Request) {
	ac, _ := From(r.Context())
	ms, err := a.mem.Members(r.Context(), ac.Account.ID, r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": ms})
}

func (a *Auth) invite(w http.ResponseWriter, r *http.Request) {
	ac, _ := From(r.Context())
	var in struct {
		Role members.Role `json:"role"`
	}
	if err := decode(w, r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	code, err := a.mem.Invite(r.Context(), ac.Account.ID, r.PathValue("id"), in.Role)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": code, "role": in.Role, "expires": time.Now().Add(members.JoinCodeTTL).UTC()})
}

func (a *Auth) setRole(w http.ResponseWriter, r *http.Request) {
	ac, _ := From(r.Context())
	var in struct {
		Role members.Role `json:"role"`
	}
	if err := decode(w, r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	m, err := a.mem.SetRole(r.Context(), ac.Account.ID, r.PathValue("id"), r.PathValue("account"), in.Role)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (a *Auth) remove(w http.ResponseWriter, r *http.Request) {
	ac, _ := From(r.Context())
	if err := a.mem.Remove(r.Context(), ac.Account.ID, r.PathValue("id"), r.PathValue("account")); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *Auth) signInCode(w http.ResponseWriter, r *http.Request) {
	ac, _ := From(r.Context())
	code, err := a.mem.SignInCode(r.Context(), ac.Account, r.PathValue("id"), r.PathValue("account"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"code": code, "expires": time.Now().Add(members.SignInCodeTTL).UTC()})
}

func (a *Auth) join(w http.ResponseWriter, r *http.Request) {
	ac, _ := From(r.Context())
	var in struct {
		Code string `json:"code"`
	}
	if err := decode(w, r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	m, err := a.mem.Join(r.Context(), ac.Account.ID, in.Code)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (a *Auth) assist(w http.ResponseWriter, r *http.Request) {
	ac, _ := From(r.Context())
	var in struct {
		Name  string `json:"name"`
		Phone string `json:"phone,omitempty"`
		Farm  string `json:"farm,omitempty"`
	}
	if err := decode(w, r, &in); err != nil {
		a.fail(w, r, err)
		return
	}
	res, err := a.mem.Assist(r.Context(), ac.Account, in.Name, in.Phone, in.Farm)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
