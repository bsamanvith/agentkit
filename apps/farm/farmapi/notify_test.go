package farmapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farm/farmtest"
	"github.com/bsamanvith/agentkit/apps/farm/farmapi"
	"github.com/bsamanvith/agentkit/apps/farm/notify"
	"github.com/bsamanvith/agentkit/apps/farm/notify/notifytest"
	notifysqlite "github.com/bsamanvith/agentkit/apps/farm/notify/sqlite"
)

func TestNotifyRoutes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, err := farm.NewService(&farmtest.Memory{}, farm.NewVerifier())
	if err != nil {
		t.Fatal(err)
	}
	store, err := notifysqlite.Open(ctx, filepath.Join(t.TempDir(), "farm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	tg := notifytest.NewFake("telegram", notify.AddrTelegram)
	wa := notifytest.NewFake("whatsapp", notify.AddrPhone).Paid()
	hub := &notify.Hub{
		Lanes: map[string]notify.LaneSetup{
			"telegram": {Info: tg.Info(), On: true, Sender: notify.Guard(tg, store)},
			"whatsapp": {Info: wa.Info(), On: true, Cost: 14, Sender: notify.Guard(wa, store, notify.WithCost(14))},
			"sms":      {Info: notify.LaneInfo{Name: "sms", Address: notify.AddrPhone, Paid: true}, Why: "needs DLT", Sender: notify.Off("sms")},
		},
		Routes: "default=chain(whatsapp,sms,telegram)", TelegramBot: "farm_bot",
		Unsubscribe: notify.NewUnsubscriber([]byte("0123456789abcdef0123456789abcdef"), "https://farm.example", store),
	}
	senders := map[string]notify.Sender{}
	for n, l := range hub.Lanes {
		senders[n] = l.Sender
	}
	root, err := notify.Parse(hub.Routes, senders)
	if err != nil {
		t.Fatal(err)
	}
	if hub.Dispatcher, err = notify.NewDispatcher(store, root); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(farmapi.New(svc, farmapi.WithNotify(hub)))
	t.Cleanup(srv.Close)

	var cfg map[string]any
	if call(t, srv, "GET", "/api/config", "", &cfg); cfg["messages"] != true {
		t.Errorf("config %v", cfg)
	}
	var st struct {
		Lanes  []notify.LaneStatus `json:"lanes"`
		Routes string              `json:"routes"`
	}
	if code := call(t, srv, "GET", "/api/notify", "", &st); code != 200 || len(st.Lanes) != 3 || st.Lanes[0].Name != "sms" || st.Lanes[0].On || st.Lanes[0].Why != "needs DLT" {
		t.Fatalf("status: %d %+v", code, st)
	}

	var ravi notify.Recipient
	if code := call(t, srv, "POST", "/api/notify/recipients", `{"name":"Ravi","phone":"+91 98765 43210","lang":"te","consent":["whatsapp"]}`, &ravi); code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	if ravi.Phone != "+919876543210" || !ravi.OptedIn("whatsapp") || ravi.OptedIn("telegram") {
		t.Errorf("created %+v", ravi)
	}
	for body, want := range map[string]int{
		`{"name":"x","phone":"98765"}`:              http.StatusBadRequest,
		`{"name":"x","consent":["pigeon"]}`:         http.StatusBadRequest,
		`{"name":"x","consent_everything":true}`:    http.StatusBadRequest,
		`{"name":"x","email":"not-an-address"}`:     http.StatusBadRequest,
		`{"name":"Kiran","farms":["f1"]}`:           http.StatusCreated,
		`{"name":"x","quiet":{"start":-1,"end":0}}`: http.StatusBadRequest,
	} {
		if code := call(t, srv, "POST", "/api/notify/recipients", body, nil); code != want {
			t.Errorf("create %s: %d, want %d", body, code, want)
		}
	}

	// A test through one lane, then through the tree.
	var test struct {
		Sent     bool             `json:"sent"`
		Receipts []notify.Receipt `json:"receipts"`
		Error    string           `json:"error"`
	}
	if code := call(t, srv, "POST", "/api/notify/recipients/"+ravi.ID+"/test", `{"lane":"telegram"}`, &test); code != 200 || test.Sent || !strings.Contains(test.Error, "no address") {
		t.Errorf("test by telegram with no chat: %d %+v", code, test)
	}
	if code := call(t, srv, "POST", "/api/notify/recipients/"+ravi.ID+"/test", `{}`, &test); code != 200 || !test.Sent || wa.Count() != 1 {
		t.Errorf("test by the tree: %d %+v", code, test)
	}
	if code := call(t, srv, "POST", "/api/notify/recipients/"+ravi.ID+"/test", `{"lane":"pigeon"}`, nil); code != http.StatusBadRequest {
		t.Errorf("test by an unknown lane: %d", code)
	}
	if code := call(t, srv, "POST", "/api/notify/recipients/nobody/test", `{}`, nil); code != http.StatusNotFound {
		t.Errorf("test to nobody: %d", code)
	}

	// Link a Telegram chat with the code.
	var link map[string]string
	if code := call(t, srv, "POST", "/api/notify/recipients/"+ravi.ID+"/link", "", &link); code != 200 || link["telegram_url"] != "https://t.me/farm_bot?start="+link["code"] {
		t.Fatalf("link: %d %v", code, link)
	}
	if _, err := notify.NewInbound(store, nil, nil).Link(ctx, "telegram", link["code"], notify.AddrTelegram, "4242"); err != nil {
		t.Fatal(err)
	}

	// Consent off on WhatsApp: the chain falls to Telegram.
	if code := call(t, srv, "PUT", "/api/notify/recipients/"+ravi.ID+"/consent/whatsapp", `{"opted_in":false}`, &ravi); code != 200 || ravi.OptedIn("whatsapp") {
		t.Errorf("consent: %d %+v", code, ravi.Consent)
	}
	if code := call(t, srv, "PUT", "/api/notify/recipients/"+ravi.ID+"/consent/pigeon", `{"opted_in":true}`, nil); code != http.StatusBadRequest {
		t.Errorf("consent to an unknown lane: %d", code)
	}
	if code := call(t, srv, "POST", "/api/notify/recipients/"+ravi.ID+"/test", `{}`, &test); code != 200 || !test.Sent || tg.Count() != 1 || wa.Count() != 1 {
		t.Errorf("after WhatsApp off: %d %+v; telegram %d, whatsapp %d", code, test, tg.Count(), wa.Count())
	}

	// A new phone drops the old number's consent; consent is not set by PUT.
	_, _ = hub.SetConsent(ctx, ravi.ID, "whatsapp", true, ravi.CreatedAt)
	if code := call(t, srv, "PUT", "/api/notify/recipients/"+ravi.ID, `{"name":"Ravi","phone":"+919811111111"}`, &ravi); code != 200 || ravi.OptedIn("whatsapp") || !ravi.OptedIn("telegram") || ravi.Telegram != "4242" {
		t.Errorf("after a new phone: %d %+v", code, ravi)
	}
	if code := call(t, srv, "PUT", "/api/notify/recipients/"+ravi.ID, `{"name":"Ravi","consent":["whatsapp"]}`, nil); code != http.StatusBadRequest {
		t.Errorf("consent by PUT: %d", code)
	}

	// Push subscriptions.
	sub := `{"endpoint":"https://fcm.googleapis.com/fcm/send/abc","keys":{"p256dh":"BCVx","auth":"BTBZ"}}`
	if code := call(t, srv, "POST", "/api/notify/recipients/"+ravi.ID+"/push", sub, &ravi); code != 200 || len(ravi.Push) != 1 || !ravi.OptedIn("push") {
		t.Errorf("add push: %d %+v", code, ravi)
	}
	if code := call(t, srv, "POST", "/api/notify/recipients/"+ravi.ID+"/push", sub, &ravi); code != 200 || len(ravi.Push) != 1 {
		t.Errorf("the same subscription twice: %d, %d subscriptions", code, len(ravi.Push))
	}
	if code := call(t, srv, "POST", "/api/notify/recipients/"+ravi.ID+"/push", `{"endpoint":"http://10.0.0.1/x","keys":{"p256dh":"a","auth":"b"}}`, nil); code != http.StatusBadRequest {
		t.Errorf("a plain-http endpoint: %d", code)
	}
	if code := call(t, srv, "DELETE", "/api/notify/recipients/"+ravi.ID+"/push", `{"endpoint":"https://fcm.googleapis.com/fcm/send/abc"}`, nil); code != http.StatusNoContent {
		t.Errorf("drop push: %d", code)
	}

	// The outbox and deliveries.
	var out struct {
		Items []notify.Item `json:"items"`
	}
	if code := call(t, srv, "GET", "/api/notify/outbox?recipient="+ravi.ID, "", &out); code != 200 || len(out.Items) != 3 {
		t.Errorf("outbox: %d, %d items, want 3 tests", code, len(out.Items))
	}
	var ds struct {
		Deliveries []notify.Delivery `json:"deliveries"`
	}
	if code := call(t, srv, "GET", "/api/notify/deliveries?lane=whatsapp", "", &ds); code != 200 || len(ds.Deliveries) != 1 || ds.Deliveries[0].Cost != 14 {
		t.Errorf("deliveries: %d %+v", code, ds)
	}
	if call(t, srv, "GET", "/api/notify", "", &st); st.Lanes[2].Name != "whatsapp" || st.Lanes[2].TodayCount != 1 || st.Lanes[2].TodayCost != 14 {
		t.Errorf("spend: %+v", st.Lanes)
	}

	// Email unsubscribe: a page on GET, unsubscribed on POST, only with the right token.
	if _, err := store.Update(ctx, ravi.ID, func(r *notify.Recipient) error {
		r.Consent["email"] = notify.Consent{OptedIn: true, Source: "settings"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	link2 := hub.Unsubscribe.Link(notify.Message{To: ravi.ID}, "email")
	u, _ := url.Parse(link2)
	path := u.Path + "?" + u.RawQuery
	if code := call(t, srv, "GET", path, "", nil); code != 200 {
		t.Errorf("unsubscribe page: %d", code)
	}
	if r, _ := store.GetRecipient(ctx, ravi.ID); !r.OptedIn("email") {
		t.Error("GET unsubscribed (a link scanner would)")
	}
	if code := call(t, srv, "POST", strings.Replace(path, "t=", "t=x", 1), "", nil); code != http.StatusForbidden {
		t.Errorf("a forged token: %d", code)
	}
	if code := call(t, srv, "POST", path, "", nil); code != 200 {
		t.Errorf("unsubscribe: %d", code)
	}
	if r, _ := store.GetRecipient(ctx, ravi.ID); r.OptedIn("email") || r.Consent["email"].Source != "unsubscribe" {
		t.Errorf("after unsubscribe: %+v", r.Consent["email"])
	}

	if code := call(t, srv, "DELETE", "/api/notify/recipients/"+ravi.ID, "", nil); code != http.StatusNoContent {
		t.Errorf("delete: %d", code)
	}
	if code := call(t, srv, "GET", "/api/notify/recipients/"+ravi.ID, "", nil); code != http.StatusNotFound {
		t.Errorf("get after delete: %d", code)
	}
}

func TestNotifyNotConfigured(t *testing.T) {
	t.Parallel()
	svc, _ := farm.NewService(&farmtest.Memory{}, farm.NewVerifier())
	srv := httptest.NewServer(farmapi.New(svc))
	t.Cleanup(srv.Close)
	for _, p := range []string{"/api/notify", "/api/notify/recipients"} {
		if code := call(t, srv, "GET", p, "", nil); code != http.StatusServiceUnavailable {
			t.Errorf("%s: %d, want 503", p, code)
		}
	}
}
