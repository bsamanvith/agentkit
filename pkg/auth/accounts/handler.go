package accounts

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// FlowTTL is how long a redirect sign-in may take, from start to callback.
const FlowTTL = 10 * time.Minute

// maxBody bounds a request body.
const maxBody = 16 << 10

// Handler serves sign-in under prefix ("/auth"), as JSON except the
// redirect lanes' callback:
//
//	GET    {prefix}/lanes                 the lanes, in order
//	GET    {prefix}/me                    the session's account and identities, and its CSRF token
//	PATCH  {prefix}/me                    {"name"}: rename
//	POST   {prefix}/signout               end this session
//	POST   {prefix}/signout-all           end every session of the account
//	POST   {prefix}/{lane}/start          {"return", "link"}: a redirect lane's URL to send the browser to
//	GET    {prefix}/{lane}/callback       where the provider sends the browser back
//	POST   {prefix}/{lane}/check          {"id", "secret"}: a secret lane
//	POST   {prefix}/{lane}/send           {"to"}: a code lane sends a code
//	POST   {prefix}/{lane}/verify         {"to", "code", "link"}: a code lane checks it
//	POST   {prefix}/password              {"username", "password"}: set the account's password
//	POST   {prefix}/unlink                {"lane", "subject"}: remove an identity
//
// A sign-in answers {"account", "created"} and sets the session cookie. A
// "link" request adds the identity to the signed-in account instead. Every
// POST needs an Origin naming this server; those of a signed-in account
// also its CSRF token. A redirect lane's redirect URL must be
// {public URL}{prefix}/{lane}/callback.
func (s *Service) Handler(prefix string) http.Handler {
	prefix = strings.TrimSuffix(prefix, "/")
	h := &handler{s: s, prefix: prefix}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+prefix+"/lanes", h.lanes)
	mux.HandleFunc("GET "+prefix+"/me", h.me)
	mux.HandleFunc("PATCH "+prefix+"/me", h.signedIn(h.rename))
	mux.HandleFunc("POST "+prefix+"/signout", h.origin(h.signOut))
	mux.HandleFunc("POST "+prefix+"/signout-all", h.signedIn(h.signOutAll))
	mux.HandleFunc("POST "+prefix+"/password", h.signedIn(h.password))
	mux.HandleFunc("POST "+prefix+"/unlink", h.signedIn(h.unlink))
	mux.HandleFunc("POST "+prefix+"/{lane}/start", h.origin(h.start))
	mux.HandleFunc("GET "+prefix+"/{lane}/callback", h.callback)
	mux.HandleFunc("POST "+prefix+"/{lane}/check", h.origin(h.check))
	mux.HandleFunc("POST "+prefix+"/{lane}/send", h.origin(h.send))
	mux.HandleFunc("POST "+prefix+"/{lane}/verify", h.origin(h.verify))
	mux.HandleFunc(prefix+"/", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusNotFound, errorBody{Error: "no such sign-in route", Code: "not_found"})
	})
	return mux
}

type handler struct {
	s      *Service
	prefix string
}

type errorBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// fail answers err. Messages are fixed per kind: they never echo a secret.
func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	var th *ThrottledError
	switch {
	case errors.As(err, &th):
		w.Header().Set("Retry-After", strconv.Itoa(int(th.RetryAfter.Seconds()+0.999)))
		writeJSON(w, http.StatusTooManyRequests, errorBody{Error: th.Error(), Code: "throttled"})
	case errors.Is(err, ErrBadSecret):
		writeJSON(w, http.StatusUnauthorized, errorBody{Error: "wrong or expired: check it and try again", Code: "bad_secret"})
	case errors.Is(err, ErrNoSession):
		writeJSON(w, http.StatusUnauthorized, errorBody{Error: "not signed in", Code: "no_session"})
	case errors.Is(err, ErrNoAccount):
		writeJSON(w, http.StatusForbidden, errorBody{Error: "no account signs in this way yet: sign in another way and link it, or ask your field partner", Code: "no_account"})
	case errors.Is(err, ErrCSRF):
		writeJSON(w, http.StatusForbidden, errorBody{Error: "refused: the request did not come from this app", Code: "csrf"})
	case errors.Is(err, ErrLinkedElsewhere):
		writeJSON(w, http.StatusConflict, errorBody{Error: "that sign-in belongs to another account", Code: "linked_elsewhere"})
	case errors.Is(err, ErrLastIdentity):
		writeJSON(w, http.StatusConflict, errorBody{Error: ErrLastIdentity.Error(), Code: "last_identity"})
	case errors.Is(err, ErrUnknownLane):
		writeJSON(w, http.StatusNotFound, errorBody{Error: "no such sign-in lane", Code: "unknown_lane"})
	case errors.Is(err, ErrInvalid):
		writeJSON(w, http.StatusBadRequest, errorBody{Error: err.Error(), Code: "invalid"})
	case errors.Is(err, ErrNotFound):
		writeJSON(w, http.StatusNotFound, errorBody{Error: "not found", Code: "not_found"})
	default:
		h.s.log.LogAttrs(r.Context(), slog.LevelError, "accounts: request failed", slog.String("path", r.URL.Path), slog.String("error", err.Error()))
		writeJSON(w, http.StatusInternalServerError, errorBody{Error: "something went wrong", Code: "internal"})
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errors.Join(ErrInvalid, errors.New("the body is not the JSON this route takes"))
	}
	return nil
}

// origin refuses a request without a same-origin Origin header: a sign-in
// from another site (login CSRF) is refused like any other.
func (h *handler) origin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.s.SameOrigin(r) {
			h.fail(w, r, ErrCSRF)
			return
		}
		next(w, r)
	}
}

type meKey struct{}

// signedIn needs a session and its CSRF token, and puts its [Me] in the
// context.
func (h *handler) signedIn(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := h.s.CheckCSRF(r); err != nil {
			h.fail(w, r, err)
			return
		}
		me, err := h.s.Me(r)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), meKey{}, me)))
	}
}

func meOf(r *http.Request) Me { me, _ := r.Context().Value(meKey{}).(Me); return me }

func (h *handler) lanes(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"lanes": h.s.Lanes()})
}

type meBody struct {
	Account    Account    `json:"account"`
	Identities []Identity `json:"identities"`
	Lane       string     `json:"lane"`
	CSRF       string     `json:"csrf"`
}

func (h *handler) me(w http.ResponseWriter, r *http.Request) {
	me, err := h.s.Me(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	ids, err := h.s.Identities(r.Context(), me.Account.ID)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, meBody{Account: me.Account, Identities: ids, Lane: me.Lane, CSRF: me.CSRF})
}

func (h *handler) rename(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := decode(w, r, &in); err != nil {
		h.fail(w, r, err)
		return
	}
	acc, err := h.s.Rename(r.Context(), meOf(r).Account.ID, in.Name)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"account": acc})
}

func (h *handler) signOut(w http.ResponseWriter, r *http.Request) {
	c, err := h.s.SignOut(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	http.SetCookie(w, c)
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) signOutAll(w http.ResponseWriter, r *http.Request) {
	if err := h.s.SignOutAll(r.Context(), meOf(r).Account.ID); err != nil {
		h.fail(w, r, err)
		return
	}
	c, _ := h.s.SignOut(r)
	http.SetCookie(w, c)
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) password(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decode(w, r, &in); err != nil {
		h.fail(w, r, err)
		return
	}
	id, err := h.s.SetPassword(r.Context(), meOf(r).Account.ID, in.Username, in.Password)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"identity": id})
}

func (h *handler) unlink(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Lane    string `json:"lane"`
		Subject string `json:"subject"`
	}
	if err := decode(w, r, &in); err != nil {
		h.fail(w, r, err)
		return
	}
	if err := h.s.Unlink(r.Context(), meOf(r).Account.ID, in.Lane, in.Subject); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// signIn finishes a sign-in with id: the account, a session cookie, and
// {"account", "created"}.
func (h *handler) signIn(w http.ResponseWriter, r *http.Request, id Identity) {
	acc, created, err := h.s.SignIn(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	c, err := h.s.StartSession(r, acc.ID, id.Lane)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	http.SetCookie(w, c)
	writeJSON(w, http.StatusOK, map[string]any{"account": acc, "created": created})
}

// link adds id to the signed-in account (the request passed CheckCSRF).
func (h *handler) link(w http.ResponseWriter, r *http.Request, id Identity) {
	me, err := h.s.Me(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	got, err := h.s.Link(r.Context(), me.Account.ID, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	got.Secret = ""
	writeJSON(w, http.StatusOK, map[string]any{"identity": got})
}

// throttled returns a *ThrottledError if the address or the id has failed
// too often.
func (h *handler) throttled(r *http.Request, id string) error {
	now := h.s.clock.Now()
	d := max(h.s.failures.wait(h.s.clientAddr(r), now), h.s.failuresByID.wait(id, now))
	if d > 0 {
		return &ThrottledError{RetryAfter: d}
	}
	return nil
}

// counted records the outcome of an attempt for id: a wrong secret counts
// against the address and the id, a success forgets the id's.
func (h *handler) counted(r *http.Request, id string, err error) {
	switch {
	case err == nil:
		h.s.failuresByID.forget(id)
	case errors.Is(err, ErrBadSecret):
		h.s.failures.add(h.s.clock.Now(), h.s.clientAddr(r))
		h.s.failuresByID.add(h.s.clock.Now(), id)
		h.s.log.LogAttrs(r.Context(), slog.LevelInfo, "accounts: wrong secret", slog.String("lane", r.PathValue("lane")), slog.String("addr", h.s.clientAddr(r)))
	}
}

func (h *handler) check(w http.ResponseWriter, r *http.Request) {
	lane, ok := h.s.lanes[r.PathValue("lane")].(SecretLane)
	if !ok {
		h.fail(w, r, ErrUnknownLane)
		return
	}
	var in struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	if err := decode(w, r, &in); err != nil {
		h.fail(w, r, err)
		return
	}
	idKey := lane.Name() + ":" + strings.ToLower(strings.TrimSpace(in.ID))
	if err := h.throttled(r, idKey); err != nil {
		h.fail(w, r, err)
		return
	}
	id, err := lane.Check(r.Context(), in.ID, in.Secret)
	h.counted(r, idKey, err)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.signIn(w, r, id)
}

func (h *handler) send(w http.ResponseWriter, r *http.Request) {
	lane, ok := h.s.lanes[r.PathValue("lane")].(CodeLane)
	if !ok {
		h.fail(w, r, ErrUnknownLane)
		return
	}
	var in struct {
		To string `json:"to"`
	}
	if err := decode(w, r, &in); err != nil {
		h.fail(w, r, err)
		return
	}
	to, err := lane.Normalize(in.To)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	now, addr, dest := h.s.clock.Now(), h.s.clientAddr(r), lane.Name()+":"+to
	if d := max(h.s.sends.wait(dest, now), h.s.sendsByAddr.wait(addr, now)); d > 0 {
		h.fail(w, r, &ThrottledError{RetryAfter: d})
		return
	}
	h.s.sends.add(now, dest)
	h.s.sendsByAddr.add(now, addr)
	// A lane that cannot sign anyone up sends only to destinations an
	// account has, so it costs nothing to try others; the answer is the
	// same either way, so it does not tell who has an account. A signed-in
	// person linking a new destination is always sent one.
	send := h.s.signUp[lane.Name()]
	if !send {
		_, err := h.s.store.Identity(r.Context(), lane.Name(), to)
		switch {
		case err == nil:
			send = true
		case !errors.Is(err, ErrNotFound):
			h.fail(w, r, err)
			return
		}
		if !send && h.s.CheckCSRF(r) == nil {
			_, err := h.s.Me(r)
			send = err == nil
		}
	}
	if send {
		if err := lane.Send(r.Context(), to); err != nil {
			h.fail(w, r, err)
			return
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"to": to})
}

func (h *handler) verify(w http.ResponseWriter, r *http.Request) {
	lane, ok := h.s.lanes[r.PathValue("lane")].(CodeLane)
	if !ok {
		h.fail(w, r, ErrUnknownLane)
		return
	}
	var in struct {
		To   string `json:"to"`
		Code string `json:"code"`
		Link bool   `json:"link,omitempty"`
	}
	if err := decode(w, r, &in); err != nil {
		h.fail(w, r, err)
		return
	}
	if in.Link {
		if err := h.s.CheckCSRF(r); err != nil {
			h.fail(w, r, err)
			return
		}
	}
	to, err := lane.Normalize(in.To)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	idKey := lane.Name() + ":" + to
	if err := h.throttled(r, idKey); err != nil {
		h.fail(w, r, err)
		return
	}
	id, err := lane.Verify(r.Context(), to, in.Code)
	h.counted(r, idKey, err)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if in.Link {
		h.link(w, r, id)
		return
	}
	h.signIn(w, r, id)
}

// flowCookie is the cookie that binds a redirect flow to the browser that
// started it.
func (h *handler) flowCookie(r *http.Request) string {
	if h.s.Secure(r) {
		return "__Secure-" + h.s.cookieName + "_flow"
	}
	return h.s.cookieName + "_flow"
}

func (h *handler) start(w http.ResponseWriter, r *http.Request) {
	lane, ok := h.s.lanes[r.PathValue("lane")].(RedirectLane)
	if !ok {
		h.fail(w, r, ErrUnknownLane)
		return
	}
	var in struct {
		Return string `json:"return,omitempty"`
		Link   bool   `json:"link,omitempty"`
	}
	if r.ContentLength != 0 {
		if err := decode(w, r, &in); err != nil {
			h.fail(w, r, err)
			return
		}
	}
	data := map[string]string{"lane": lane.Name(), "return": localPath(in.Return)}
	if in.Link {
		if err := h.s.CheckCSRF(r); err != nil {
			h.fail(w, r, err)
			return
		}
		me, err := h.s.Me(r)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		data["link"] = me.Account.ID
	}
	f := Flow{State: random(), Verifier: random() + random()[:11], Nonce: random()}
	data["verifier"], data["nonce"] = f.Verifier, f.Nonce
	h.s.expire(r.Context())
	if err := h.s.store.PutChallenge(r.Context(), "flow:"+sha(f.State), Challenge{Purpose: purposeFlow, Data: data, Expires: h.s.clock.Now().Add(FlowTTL)}); err != nil {
		h.fail(w, r, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: h.flowCookie(r), Value: f.State, Path: h.prefix + "/" + lane.Name() + "/callback",
		MaxAge: int(FlowTTL / time.Second), HttpOnly: true, Secure: h.s.Secure(r), SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]string{"url": lane.AuthCodeURL(f)})
}

// callback finishes a redirect flow and sends the browser back into the
// app: to the page it started from, with "signin=failed&reason=..." in the
// query when it did not work.
func (h *handler) callback(w http.ResponseWriter, r *http.Request) {
	lane, ok := h.s.lanes[r.PathValue("lane")].(RedirectLane)
	if !ok {
		h.fail(w, r, ErrUnknownLane)
		return
	}
	ctx, q := r.Context(), r.URL.Query()
	http.SetCookie(w, &http.Cookie{Name: h.flowCookie(r), Value: "", Path: h.prefix + "/" + lane.Name() + "/callback", MaxAge: -1, HttpOnly: true, Secure: h.s.Secure(r), SameSite: http.SameSiteLaxMode})
	back := func(ret, reason string) {
		if reason != "" {
			u, _ := url.Parse(ret)
			v := u.Query()
			v.Set("signin", "failed")
			v.Set("reason", reason)
			u.RawQuery = v.Encode()
			ret = u.String()
			h.s.log.LogAttrs(ctx, slog.LevelInfo, "accounts: redirect sign-in failed", slog.String("lane", lane.Name()), slog.String("reason", reason), slog.String("addr", h.s.clientAddr(r)))
		}
		http.Redirect(w, r, ret, http.StatusSeeOther)
	}
	state := q.Get("state")
	c, err := r.Cookie(h.flowCookie(r))
	if err != nil || state == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) != 1 {
		back("/", "expired")
		return
	}
	k := "flow:" + sha(state)
	ch, err := h.s.store.Challenge(ctx, k)
	if err != nil || ch.Purpose != purposeFlow || ch.Data["lane"] != lane.Name() || h.s.store.DeleteChallenge(ctx, k) != nil || !h.s.clock.Now().Before(ch.Expires) {
		back("/", "expired")
		return
	}
	ret := localPath(ch.Data["return"])
	if e := q.Get("error"); e != "" {
		back(ret, "denied")
		return
	}
	id, err := lane.Finish(ctx, Flow{State: state, Verifier: ch.Data["verifier"], Nonce: ch.Data["nonce"]}, q.Get("code"))
	if err != nil {
		h.s.log.LogAttrs(ctx, slog.LevelWarn, "accounts: provider sign-in", slog.String("lane", lane.Name()), slog.String("error", err.Error()))
		back(ret, "provider")
		return
	}
	if link := ch.Data["link"]; link != "" {
		me, err := h.s.Me(r)
		if err != nil || me.Account.ID != link {
			back(ret, "session")
			return
		}
		if _, err := h.s.Link(ctx, link, id); err != nil {
			reason := "internal"
			if errors.Is(err, ErrLinkedElsewhere) {
				reason = "linked_elsewhere"
			}
			back(ret, reason)
			return
		}
		back(ret, "")
		return
	}
	acc, _, err := h.s.SignIn(ctx, id)
	if err != nil {
		reason := "internal"
		if errors.Is(err, ErrNoAccount) {
			reason = "no_account"
		}
		back(ret, reason)
		return
	}
	cookie, err := h.s.StartSession(r, acc.ID, id.Lane)
	if err != nil {
		back(ret, "internal")
		return
	}
	http.SetCookie(w, cookie)
	back(ret, "")
}

// localPath is p if it is a path on this server ("/..."), else "/": a
// return address never sends the browser to another site.
func localPath(p string) string {
	if p == "" || p[0] != '/' || strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\\\r\n\t") {
		return "/"
	}
	u, err := url.Parse(p)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return "/"
	}
	return p
}
