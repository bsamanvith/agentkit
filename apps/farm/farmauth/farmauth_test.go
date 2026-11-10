package farmauth_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/automation"
	autosqlite "github.com/bsamanvith/agentkit/apps/farm/automation/sqlite"
	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farm/farmtest"
	"github.com/bsamanvith/agentkit/apps/farm/farmapi"
	"github.com/bsamanvith/agentkit/apps/farm/farmauth"
	"github.com/bsamanvith/agentkit/apps/farm/geo"
	"github.com/bsamanvith/agentkit/apps/farm/members"
	"github.com/bsamanvith/agentkit/apps/farm/members/memberstest"
	"github.com/bsamanvith/agentkit/pkg/auth/accounts"
)

// boundary is about one acre near Hyderabad.
const boundary = `{"source":"map","corners":[{"lat":17.385,"lng":78.4867},{"lat":17.385,"lng":78.48730},{"lat":17.38557,"lng":78.48730},{"lat":17.38557,"lng":78.4867}]}`

type app struct {
	t   *testing.T
	srv *httptest.Server
	acc *accounts.Service
	svc *farm.Service
}

func newApp(t *testing.T, before func(*farm.Service)) *app {
	t.Helper()
	loc := &farmtest.Locator{Location: farm.Location{Village: "Nampally", State: "Telangana", Country: "India", CountryCode: "IN"}}
	svc, err := farm.NewService(&farmtest.Memory{}, farm.NewVerifier(farm.WithLocator(loc), farm.WithCountry("IN")))
	if err != nil {
		t.Fatal(err)
	}
	if before != nil {
		before(svc)
	}
	store := accounts.NewMemoryStore()
	pw, err := accounts.NewPasswordLane(store)
	if err != nil {
		t.Fatal(err)
	}
	acc, err := accounts.New(store, accounts.WithLanes(pw, accounts.NewInviteLane(store, nil)), accounts.WithCookieName("farm_session"))
	if err != nil {
		t.Fatal(err)
	}
	astore, err := autosqlite.Open(context.Background(), filepath.Join(t.TempDir(), "farm.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = astore.Close() })
	engine, err := automation.New(astore, noTargets{})
	if err != nil {
		t.Fatal(err)
	}
	gate := farmauth.New(acc, members.New(&memberstest.Memory{}, acc, svc), nil)
	mux := http.NewServeMux()
	gate.Register(mux)
	mux.Handle("/api/", gate.Protect(farmapi.New(svc, farmapi.WithAutomations(engine))))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &app{t: t, srv: srv, acc: acc, svc: svc}
}

// person is a browser: its cookies, the CSRF token and farm it uses.
type person struct {
	a       *app
	client  *http.Client
	csrf    string
	holding string
	account string
}

func (a *app) browser() *person {
	jar, _ := cookiejar.New(nil)
	return &person{a: a, client: &http.Client{Jar: jar}}
}

// user makes an account with a password and signs a browser in with it.
func (a *app) user(name string, roles ...string) *person {
	a.t.Helper()
	ctx := context.Background()
	acc, err := a.acc.CreateAccount(ctx, accounts.Account{Name: name, Roles: roles})
	if err != nil {
		a.t.Fatal(err)
	}
	username := strings.ToLower(strings.ReplaceAll(name, " ", "."))
	if _, err := a.acc.SetPassword(ctx, acc.ID, username, "a long enough password"); err != nil {
		a.t.Fatal(err)
	}
	p := a.browser()
	if code := p.do("POST", "/auth/password/check", `{"id":"`+username+`","secret":"a long enough password"}`, nil); code != http.StatusOK {
		a.t.Fatalf("sign in %s: %d", name, code)
	}
	p.refresh()
	return p
}

// refresh reads /api/me: the CSRF token and the farm acted for.
func (p *person) refresh() map[string]any {
	p.a.t.Helper()
	var me map[string]any
	if code := p.do("GET", "/api/me", "", &me); code != http.StatusOK {
		p.a.t.Fatalf("/api/me: %d %v", code, me)
	}
	p.csrf = me["csrf"].(string)
	p.account = me["account"].(map[string]any)["id"].(string)
	if p.holding == "" {
		p.holding = me["holding"].(map[string]any)["id"].(string)
	}
	return me
}

func (p *person) do(method, path, body string, out any, header ...string) int {
	p.a.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, p.a.srv.URL+path, strings.NewReader(body))
	if err != nil {
		p.a.t.Fatal(err)
	}
	if method != "GET" {
		req.Header.Set("Origin", p.a.srv.URL)
		req.Header.Set(accounts.CSRFHeader, p.csrf)
	}
	if p.holding != "" {
		req.Header.Set(farmauth.HoldingHeader, p.holding)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := p.client.Do(req)
	if err != nil {
		p.a.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			p.a.t.Fatalf("%s %s: %v in %s", method, path, err, b)
		}
	}
	return resp.StatusCode
}

func (p *person) plots() []string {
	p.a.t.Helper()
	var out struct{ Farms []farm.Farm }
	if code := p.do("GET", "/api/farms", "", &out); code != http.StatusOK {
		p.a.t.Fatalf("list plots: %d", code)
	}
	ids := []string{}
	for _, f := range out.Farms {
		ids = append(ids, f.ID)
	}
	return ids
}

func (p *person) newPlot(name string) string {
	p.a.t.Helper()
	var f farm.Farm
	if code := p.do("POST", "/api/farms", `{"name":"`+name+`","boundary":`+boundary+`}`, &f); code != http.StatusCreated {
		p.a.t.Fatalf("create plot: %d", code)
	}
	return f.ID
}

func TestTheAPIIsBehindSignIn(t *testing.T) {
	a := newApp(t, nil)
	anon := a.browser()
	if code := anon.do("GET", "/api/config", "", nil); code != http.StatusOK {
		t.Errorf("/api/config without a session: %d, want 200 (the sign-in screen needs it)", code)
	}
	for _, path := range []string{"/api/farms", "/api/me", "/api/groups", "/api/notices"} {
		if code := anon.do("GET", path, "", nil); code != http.StatusUnauthorized {
			t.Errorf("GET %s without a session: %d, want 401", path, code)
		}
	}
	if code := anon.do("GET", "/auth/lanes", "", nil); code != http.StatusOK {
		t.Errorf("/auth/lanes: %d", code)
	}
	ravi := a.user("Ravi")
	me := ravi.refresh()
	if me["role"] != "owner" || len(me["farms"].([]any)) != 1 {
		t.Errorf("a new account's /api/me = %v, want one farm of its own", me)
	}
	ravi.csrf = "wrong"
	if code := ravi.do("POST", "/api/farms", `{"name":"x","boundary":`+boundary+`}`, nil); code != http.StatusForbidden {
		t.Errorf("a change with a wrong CSRF token: %d, want 403", code)
	}
}

func TestEachFarmSeesOnlyItsOwnPlots(t *testing.T) {
	a := newApp(t, nil)
	ravi, sita := a.user("Ravi"), a.user("Sita")
	plot := ravi.newPlot("East field")
	if got := sita.plots(); len(got) != 0 {
		t.Errorf("Sita sees %v", got)
	}
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/farms/" + plot},
		{"PUT", "/api/farms/" + plot},
		{"DELETE", "/api/farms/" + plot},
		{"GET", "/api/farms/" + plot + "/geojson"},
	} {
		body := ""
		if c.method == "PUT" {
			body = `{"name":"Mine now","boundary":` + boundary + `}`
		}
		if code := sita.do(c.method, c.path, body, nil); code != http.StatusNotFound {
			t.Errorf("Sita %s %s: %d, want 404", c.method, c.path, code)
		}
	}
	// Naming Ravi's farm does not get her in.
	sita.holding = ravi.holding
	if code := sita.do("GET", "/api/farms", "", nil); code != http.StatusNotFound {
		t.Errorf("Sita acting for Ravi's farm: %d, want 404", code)
	}
}

func TestAPartnerSignsAFarmerUpAndTheFarmerSignsIn(t *testing.T) {
	a := newApp(t, nil)
	partner := a.user("Field Partner", members.AccountRolePartner)
	plain := a.user("Not A Partner")
	if code := plain.do("POST", "/api/assist", `{"name":"Venkatamma"}`, nil); code != http.StatusForbidden {
		t.Errorf("assist without the partner role: %d, want 403", code)
	}
	var res members.Assisted
	if code := partner.do("POST", "/api/assist", `{"name":"Venkatamma","phone":"98480 12345","farm":"Venkatamma's fields"}`, &res); code != http.StatusOK {
		t.Fatalf("assist: %d", code)
	}
	if res.Code == "" || res.Account.Phone != "+919848012345" || res.Holding.Name != "Venkatamma's fields" {
		t.Errorf("assisted = %+v", res)
	}
	// The partner works on the farmer's farm, e.g. maps the first plot.
	partner.holding = res.Holding.ID
	plot := partner.newPlot("Tank field")

	// The farmer types the code on their phone.
	farmer := a.browser()
	if code := farmer.do("POST", "/auth/invite/check", `{"secret":"`+strings.ToLower(res.Code)+`"}`, nil); code != http.StatusOK {
		t.Fatalf("farmer signs in with the code: %d", code)
	}
	me := farmer.refresh()
	if me["role"] != "owner" || me["holding"].(map[string]any)["id"] != res.Holding.ID {
		t.Errorf("farmer's /api/me = %v", me)
	}
	if got := farmer.plots(); len(got) != 1 || got[0] != plot {
		t.Errorf("farmer sees %v, want the plot the partner mapped", got)
	}
	var ms struct{ Members []members.MemberView }
	farmer.do("GET", "/api/holdings/"+res.Holding.ID+"/members", "", &ms)
	if len(ms.Members) != 2 || ms.Members[1].Role != members.Partner || ms.Members[1].Name != "Field Partner" {
		t.Errorf("members = %+v", ms.Members)
	}

	// The farmer loses their phone: the partner makes a new code, and the
	// lost phone's session ends.
	var code struct{ Code string }
	if c := partner.do("POST", "/api/holdings/"+res.Holding.ID+"/members/"+farmer.account+"/code", "", &code); c != http.StatusOK || code.Code == "" {
		t.Fatalf("recovery code: %d", c)
	}
	if c := farmer.do("GET", "/api/farms", "", nil); c != http.StatusUnauthorized {
		t.Errorf("the lost phone after a recovery code: %d, want 401", c)
	}
	newPhone := a.browser()
	if c := newPhone.do("POST", "/auth/invite/check", `{"secret":"`+code.Code+`"}`, nil); c != http.StatusOK {
		t.Fatalf("sign in with the recovery code: %d", c)
	}
	// The farmer can take the partner off their farm.
	newPhone.refresh()
	if c := newPhone.do("DELETE", "/api/holdings/"+res.Holding.ID+"/members/"+partner.account, "", nil); c != http.StatusNoContent {
		t.Fatalf("remove the partner: %d", c)
	}
	if c := partner.do("GET", "/api/farms/"+plot, "", nil); c != http.StatusNotFound {
		t.Errorf("the removed partner reading the plot: %d, want 404", c)
	}
	// Only an owner manages members.
	if c := partner.do("POST", "/api/holdings/"+res.Holding.ID+"/invites", `{"role":"family"}`, nil); c != http.StatusNotFound {
		t.Errorf("a removed partner inviting: %d, want 404", c)
	}
}

func TestAWorkerReadsButDoesNotChange(t *testing.T) {
	a := newApp(t, nil)
	owner := a.user("Ravi")
	plot := owner.newPlot("East field")
	var inv struct{ Code string }
	if c := owner.do("POST", "/api/holdings/"+owner.holding+"/invites", `{"role":"worker"}`, &inv); c != http.StatusOK {
		t.Fatalf("invite: %d", c)
	}
	if c := owner.do("POST", "/api/holdings/"+owner.holding+"/invites", `{"role":"partner"}`, nil); c != http.StatusBadRequest {
		t.Errorf("invite as partner: %d, want 400", c)
	}
	worker := a.user("Raju")
	var m members.Membership
	if c := worker.do("POST", "/api/join", `{"code":"`+inv.Code+`"}`, &m); c != http.StatusOK || m.Role != members.Worker {
		t.Fatalf("join: %d %+v", c, m)
	}
	if c := a.user("Someone").do("POST", "/api/join", `{"code":"`+inv.Code+`"}`, nil); c != http.StatusUnauthorized {
		t.Errorf("the join code a second time: %d, want 401", c)
	}
	worker.holding = owner.holding
	if got := worker.plots(); len(got) != 1 || got[0] != plot {
		t.Errorf("worker sees %v", got)
	}
	for _, c := range []struct{ method, path, body string }{
		{"PUT", "/api/farms/" + plot, `{"name":"x","boundary":` + boundary + `}`},
		{"DELETE", "/api/farms/" + plot, ""},
		{"POST", "/api/farms", `{"name":"x","boundary":` + boundary + `}`},
		{"POST", "/api/groups", `{"name":"g","plots":[]}`},
	} {
		if code := worker.do(c.method, c.path, c.body, nil); code != http.StatusForbidden {
			t.Errorf("worker %s %s: %d, want 403", c.method, c.path, code)
		}
	}
	if c := worker.do("POST", "/api/verify", `{"boundary":`+boundary+`}`, nil); c != http.StatusOK {
		t.Errorf("worker checking a boundary (reads only): %d", c)
	}
	if c := worker.do("PUT", "/api/holdings/"+owner.holding+"/members/"+worker.account, `{"role":"owner"}`, nil); c != http.StatusForbidden {
		t.Errorf("worker making themself owner: %d, want 403", c)
	}
	// The owner promotes them; then they can write.
	if c := owner.do("PUT", "/api/holdings/"+owner.holding+"/members/"+worker.account, `{"role":"family"}`, nil); c != http.StatusOK {
		t.Fatalf("set role: %d", c)
	}
	if c := worker.do("PUT", "/api/farms/"+plot, `{"name":"East field","boundary":`+boundary+`}`, nil); c != http.StatusOK {
		t.Errorf("family editing a plot: %d", c)
	}
	// The last owner stays.
	if c := owner.do("DELETE", "/api/holdings/"+owner.holding+"/members/"+owner.account, "", nil); c != http.StatusConflict {
		t.Errorf("the last owner leaving: %d, want 409", c)
	}
}

func TestAutomationsAreAnAdminsToChange(t *testing.T) {
	a := newApp(t, nil)
	ravi := a.user("Ravi")
	body := `{"name":"x","rule":"heavy_rain","trigger":{"kind":"schedule","every":"24h"}}`
	if c := ravi.do("POST", "/api/automations", body, nil); c != http.StatusForbidden {
		t.Errorf("a farmer making an automation: %d, want 403", c)
	}
	if c := ravi.do("POST", "/api/automations/x/run", `{}`, nil); c != http.StatusBadRequest {
		t.Errorf("a farmer running an automation on every plot: %d, want 400", c)
	}
	admin := a.user("Admin", accounts.RoleAdmin)
	if c := admin.do("POST", "/api/automations", body, nil); c == http.StatusForbidden {
		t.Errorf("an admin making an automation: %d", c)
	}
}

func TestTheFirstFarmTakesThePlotsMadeBeforeSignIn(t *testing.T) {
	var old farm.Farm
	a := newApp(t, func(svc *farm.Service) {
		var err error
		old, err = svc.Create(context.Background(), farm.Draft{Name: "From before", Boundary: farm.Boundary{Corners: []farm.Corner{
			{Point: pt(17.385, 78.4867)}, {Point: pt(17.385, 78.4873)}, {Point: pt(17.38557, 78.4873)}, {Point: pt(17.38557, 78.4867)},
		}}})
		if err != nil {
			panic(err)
		}
	})
	first := a.user("Owner")
	if got := first.plots(); len(got) != 1 || got[0] != old.ID {
		t.Errorf("the first account sees %v, want the plot from before", got)
	}
	if got := a.user("Second").plots(); len(got) != 0 {
		t.Errorf("the second account sees %v", got)
	}
}

type noTargets struct{}

func (noTargets) Targets(context.Context, automation.Scope) ([]automation.Target, error) {
	return nil, nil
}

func pt(lat, lng float64) geo.Point { return geo.Point{Lat: lat, Lng: lng} }

func TestMessageSettingsAreAnAdminsAndUnsubscribeIsOpen(t *testing.T) {
	a := newApp(t, nil)
	ravi := a.user("Ravi")
	for _, path := range []string{"/api/notify", "/api/notify/recipients", "/api/notify/outbox"} {
		if c := ravi.do("GET", path, "", nil); c != http.StatusForbidden {
			t.Errorf("a farmer reads %s (other farms' phone numbers): %d, want 403", path, c)
		}
	}
	admin := a.user("Admin", accounts.RoleAdmin)
	if c := admin.do("GET", "/api/notify/recipients", "", nil); c == http.StatusForbidden || c == http.StatusUnauthorized {
		t.Errorf("an admin reads the recipients: %d", c)
	}
	// The email unsubscribe link is opened from a mail app, with no session:
	// it reaches the API (no lanes here: 503), not the sign-in guard.
	if c := a.browser().do("GET", "/api/notify/unsubscribe?r=x&l=email&s=y", "", nil); c == http.StatusUnauthorized {
		t.Errorf("unsubscribe without a session: %d, want it let through", c)
	}
}
