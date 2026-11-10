package farmapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/bsamanvith/agentkit/apps/farm/notify"
)

// WithNotify serves the message lanes' settings with h: the lanes and what
// they spent, the people who get messages, their consent per lane, push
// subscriptions, Telegram links, test messages, the outbox, and email
// unsubscribe links. Without it, those routes are 503.
func WithNotify(h *notify.Hub) Option { return func(x *api) { x.hub = h } }

func (a *api) notifyRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/notify", a.notifying(a.notifyStatus))
	mux.HandleFunc("GET /api/notify/recipients", a.notifying(a.recipients))
	mux.HandleFunc("POST /api/notify/recipients", a.notifying(a.createRecipient))
	mux.HandleFunc("GET /api/notify/recipients/{id}", a.notifying(a.getRecipient))
	mux.HandleFunc("PUT /api/notify/recipients/{id}", a.notifying(a.updateRecipient))
	mux.HandleFunc("DELETE /api/notify/recipients/{id}", a.notifying(a.deleteRecipient))
	mux.HandleFunc("PUT /api/notify/recipients/{id}/consent/{lane}", a.notifying(a.consent))
	mux.HandleFunc("POST /api/notify/recipients/{id}/push", a.notifying(a.addPush))
	mux.HandleFunc("DELETE /api/notify/recipients/{id}/push", a.notifying(a.dropPush))
	mux.HandleFunc("POST /api/notify/recipients/{id}/link", a.notifying(a.linkCode))
	mux.HandleFunc("POST /api/notify/recipients/{id}/test", a.notifying(a.testMessage))
	mux.HandleFunc("GET /api/notify/outbox", a.notifying(a.outbox))
	mux.HandleFunc("GET /api/notify/deliveries", a.notifying(a.deliveries))
	mux.HandleFunc("/api/notify/unsubscribe", a.notifying(func(w http.ResponseWriter, r *http.Request) {
		if a.hub.Unsubscribe == nil {
			http.NotFound(w, r)
			return
		}
		a.hub.Unsubscribe.ServeHTTP(w, r)
	}))
}

func (a *api) notifying(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a.hub == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody{Error: "messages are not configured"})
			return
		}
		h(w, r)
	}
}

func (a *api) notifyStatus(w http.ResponseWriter, r *http.Request) {
	lanes, err := a.hub.Status(r.Context(), a.now())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"lanes": lanes, "routes": a.hub.Routes, "push_key": a.hub.PushKey, "telegram_bot": a.hub.TelegramBot,
	})
}

func (a *api) recipients(w http.ResponseWriter, r *http.Request) {
	rs, err := a.hub.Dispatcher.Store().ListRecipients(r.Context())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if rs == nil {
		rs = []notify.Recipient{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"recipients": rs})
}

// recipientBody is what the settings page may set: not consent, push
// subscriptions or link codes, which have their own routes.
type recipientBody struct {
	Name      string             `json:"name"`
	Phone     string             `json:"phone,omitempty"`
	Email     string             `json:"email,omitempty"`
	Lang      string             `json:"lang,omitempty"`
	UTCOffset int                `json:"utc_offset,omitempty"`
	Quiet     *notify.QuietHours `json:"quiet,omitempty"`
	Farms     []string           `json:"farms,omitempty"`
	// Consent opts in to these lanes on create (the person ticked them).
	Consent []string `json:"consent,omitempty"`
}

func (b recipientBody) apply(r *notify.Recipient) {
	r.Name, r.Phone, r.Email, r.Lang, r.UTCOffset, r.Quiet, r.Farms = strings.TrimSpace(b.Name),
		strings.ReplaceAll(strings.TrimSpace(b.Phone), " ", ""), strings.TrimSpace(b.Email), b.Lang, b.UTCOffset, b.Quiet, b.Farms
}

func (a *api) createRecipient(w http.ResponseWriter, r *http.Request) {
	var b recipientBody
	if err := decode(w, r, &b); err != nil {
		a.fail(w, r, err)
		return
	}
	now := a.now().UTC()
	rc := notify.Recipient{ID: notify.NewID("person_"), Consent: map[string]notify.Consent{}, CreatedAt: now, UpdatedAt: now}
	b.apply(&rc)
	for _, lane := range b.Consent {
		if _, ok := a.hub.Lanes[lane]; !ok {
			writeJSON(w, http.StatusBadRequest, errorBody{Error: "no lane " + lane})
			return
		}
		rc.Consent[lane] = notify.Consent{OptedIn: true, At: now, Source: "settings"}
	}
	if err := a.hub.Dispatcher.Store().PutRecipient(r.Context(), rc); err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, rc)
}

func (a *api) getRecipient(w http.ResponseWriter, r *http.Request) {
	rc, err := a.hub.Dispatcher.Store().GetRecipient(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rc)
}

func (a *api) updateRecipient(w http.ResponseWriter, r *http.Request) {
	var b recipientBody
	if err := decode(w, r, &b); err != nil {
		a.fail(w, r, err)
		return
	}
	if len(b.Consent) > 0 {
		writeJSON(w, http.StatusBadRequest, errorBody{Error: "set consent with PUT /api/notify/recipients/{id}/consent/{lane}"})
		return
	}
	rc, err := a.hub.Dispatcher.Store().Update(r.Context(), r.PathValue("id"), func(rc *notify.Recipient) error {
		phone, email := rc.Phone, rc.Email
		b.apply(rc)
		// A new number or address has not agreed to anything yet.
		if rc.Phone != phone {
			delete(rc.Consent, "whatsapp")
			delete(rc.Consent, "sms")
		}
		if rc.Email != email {
			delete(rc.Consent, "email")
		}
		rc.UpdatedAt = a.now().UTC()
		return nil
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rc)
}

func (a *api) deleteRecipient(w http.ResponseWriter, r *http.Request) {
	if err := a.hub.Dispatcher.Store().DeleteRecipient(r.Context(), r.PathValue("id")); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) consent(w http.ResponseWriter, r *http.Request) {
	var b struct {
		OptedIn bool `json:"opted_in"`
	}
	if err := decode(w, r, &b); err != nil {
		a.fail(w, r, err)
		return
	}
	rc, err := a.hub.SetConsent(r.Context(), r.PathValue("id"), r.PathValue("lane"), b.OptedIn, a.now())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rc)
}

func (a *api) addPush(w http.ResponseWriter, r *http.Request) {
	var s notify.PushSubscription
	if err := decode(w, r, &s); err != nil {
		a.fail(w, r, err)
		return
	}
	rc, err := a.hub.AddPush(r.Context(), r.PathValue("id"), s, a.now())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, rc)
}

func (a *api) dropPush(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Endpoint string `json:"endpoint"`
	}
	if err := decode(w, r, &b); err != nil {
		a.fail(w, r, err)
		return
	}
	if err := a.hub.DropPush(r.Context(), r.PathValue("id"), b.Endpoint); err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *api) linkCode(w http.ResponseWriter, r *http.Request) {
	code, err := a.hub.LinkCode(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out := map[string]string{"code": code}
	if a.hub.TelegramBot != "" {
		out["telegram_url"] = "https://t.me/" + a.hub.TelegramBot + "?start=" + code
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *api) testMessage(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Lane string `json:"lane,omitempty"`
	}
	if err := decode(w, r, &b); err != nil {
		a.fail(w, r, err)
		return
	}
	rcs, err := a.hub.Test(r.Context(), r.PathValue("id"), b.Lane, "A test from your farm app", "If you can read this, farm alerts will reach you here.")
	if rcs == nil {
		rcs = []notify.Receipt{}
	}
	body := map[string]any{"receipts": rcs, "sent": err == nil}
	switch {
	case err == nil:
	case errorsIsAny(err, notify.ErrNotFound, notify.ErrInvalid):
		a.fail(w, r, err)
		return
	default:
		body["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, body)
}

func (a *api) outbox(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	items, err := a.hub.Dispatcher.Store().ListItems(r.Context(), notify.ItemFilter{Recipient: q.Get("recipient"), Notice: q.Get("notice"), Status: q.Get("status"), Limit: limitOf(r)})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if items == nil {
		items = []notify.Item{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *api) deliveries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	ds, err := a.hub.Dispatcher.Store().ListDeliveries(r.Context(), notify.DeliveryFilter{Message: q.Get("message"), Recipient: q.Get("recipient"), Lane: q.Get("lane"), Limit: limitOf(r)})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if ds == nil {
		ds = []notify.Delivery{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": ds})
}

func errorsIsAny(err error, targets ...error) bool {
	for _, t := range targets {
		if errors.Is(err, t) {
			return true
		}
	}
	return false
}
