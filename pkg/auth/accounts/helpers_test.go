package accounts_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/auth/accounts"
	"github.com/bsamanvith/agentkit/pkg/auth/accounts/accountstest"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

const host = "farm.example"

var t0 = time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)

// fakeProvider is a redirect lane whose "provider" signs in whoever the
// test says: Finish returns the identity queued for the code.
type fakeProvider struct {
	name  string
	codes map[string]accounts.Identity
	flows []accounts.Flow
}

func (p *fakeProvider) Name() string      { return p.name }
func (*fakeProvider) Kind() accounts.Kind { return accounts.KindRedirect }
func (p *fakeProvider) AuthCodeURL(f accounts.Flow) string {
	p.flows = append(p.flows, f)
	return "https://provider.example/auth?state=" + url.QueryEscape(f.State)
}

func (p *fakeProvider) Finish(_ context.Context, _ accounts.Flow, code string) (accounts.Identity, error) {
	id, ok := p.codes[code]
	if !ok {
		return accounts.Identity{}, errors.New("unknown code")
	}
	return id, nil
}

type rig struct {
	t      *testing.T
	svc    *accounts.Service
	store  accounts.Store
	clock  *clocktest.Fake
	h      http.Handler
	google *fakeProvider
	outbox *accountstest.Outbox
}

func newRig(t *testing.T, opts ...accounts.Option) *rig {
	t.Helper()
	store := accounts.NewMemoryStore()
	clk := clocktest.NewFake(t0)
	g := &fakeProvider{name: "google", codes: map[string]accounts.Identity{}}
	out := &accountstest.Outbox{}
	pw, err := accounts.NewPasswordLane(store)
	if err != nil {
		t.Fatal(err)
	}
	lanes := accounts.WithLanes(g, pw, accounts.NewInviteLane(store, clk),
		accounts.NewPhoneLane("phone", "91", out, store, accounts.WithCodeClock(clk)),
		accounts.NewEmailLane("email", out, store, accounts.WithCodeClock(clk)))
	svc, err := accounts.New(store, append([]accounts.Option{lanes, accounts.WithClock(clk), accounts.WithSignUp("google", "phone")}, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	return &rig{t: t, svc: svc, store: store, clock: clk, h: svc.Handler("/auth"), google: g, outbox: out}
}

// browser is one browser: its cookies, its address and the CSRF token its
// page read from /auth/me.
type browser struct {
	r       *rig
	addr    string
	cookies map[string]*http.Cookie
	csrf    string
}

func (r *rig) browser(addr string) *browser {
	return &browser{r: r, addr: addr, cookies: map[string]*http.Cookie{}}
}

type resp struct {
	code int
	body map[string]any
	hdr  http.Header
}

func (b *browser) do(method, path string, body any, header ...string) resp {
	b.r.t.Helper()
	var rd *bytes.Reader
	if body != nil {
		j, _ := json.Marshal(body)
		rd = bytes.NewReader(j)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, "http://"+host+path, rd)
	req.RemoteAddr = b.addr + ":5555"
	if method != http.MethodGet {
		req.Header.Set("Origin", "http://"+host)
		if b.csrf != "" {
			req.Header.Set(accounts.CSRFHeader, b.csrf)
		}
	}
	for i := 0; i+1 < len(header); i += 2 {
		if header[i+1] == "" {
			req.Header.Del(header[i])
		} else {
			req.Header.Set(header[i], header[i+1])
		}
	}
	for _, c := range b.cookies {
		if strings.HasPrefix(path, c.Path) {
			req.AddCookie(c)
		}
	}
	rec := httptest.NewRecorder()
	b.r.h.ServeHTTP(rec, req)
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge < 0 {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c
		}
	}
	out := resp{code: rec.Code, hdr: rec.Header(), body: map[string]any{}}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	return out
}

// me reads /auth/me, keeping the CSRF token, and returns the account id
// ("" when not signed in).
func (b *browser) me() string {
	b.r.t.Helper()
	res := b.do(http.MethodGet, "/auth/me", nil)
	if res.code != http.StatusOK {
		return ""
	}
	b.csrf, _ = res.body["csrf"].(string)
	return res.body["account"].(map[string]any)["id"].(string)
}

// google signs in at the fake provider as id and returns where the
// callback sent the browser.
func (b *browser) google(id accounts.Identity, start map[string]any) *url.URL {
	b.r.t.Helper()
	res := b.do(http.MethodPost, "/auth/google/start", start)
	if res.code != http.StatusOK {
		b.r.t.Fatalf("start: %d %v", res.code, res.body)
	}
	u, _ := url.Parse(res.body["url"].(string))
	code := "code-" + id.Subject
	b.r.google.codes[code] = id
	return b.callback("/auth/google/callback?state=" + url.QueryEscape(u.Query().Get("state")) + "&code=" + code)
}

func (b *browser) callback(path string) *url.URL {
	b.r.t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://"+host+path, nil)
	req.RemoteAddr = b.addr + ":5555"
	for _, c := range b.cookies {
		if strings.HasPrefix(req.URL.Path, c.Path) {
			req.AddCookie(c)
		}
	}
	rec := httptest.NewRecorder()
	b.r.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		b.r.t.Fatalf("callback: %d %s", rec.Code, rec.Body)
	}
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge < 0 {
			delete(b.cookies, c.Name)
		} else {
			b.cookies[c.Name] = c
		}
	}
	u, _ := url.Parse(rec.Header().Get("Location"))
	return u
}

func gid(sub string) accounts.Identity {
	return accounts.Identity{Lane: "google", Subject: sub, Email: sub + "@gmail.com", EmailVerified: true, Name: "Farmer " + sub}
}

func errCode(res resp) string { s, _ := res.body["code"].(string); return s }
