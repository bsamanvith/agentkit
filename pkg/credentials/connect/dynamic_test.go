package connect_test

// The slots of toolsets added at run time (MCP OAuth, T095): Dynamic
// prepares and finishes their flows, and this package keeps them as it
// keeps an App's: the state, the browser binding, single use and expiry,
// the flow's tenant and user.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/credentials/connect"
	"github.com/bsamanvith/agentkit/pkg/credentials/memory"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// fakeDynamic is a Dynamic whose authorization server is the test: Begin
// returns a URL with the state, and Complete stores an OAuth2 token for the
// flow's user, in ctx's tenant.
type fakeDynamic struct {
	redirect string
	mu       sync.Mutex
	slots    map[string][]connect.Slot // by tenant
	begun    []string                  // tenant/user/slot/state
	done     []completed
	// authURL, if set, is where Begin sends the browser.
	authURL string
}

type completed struct {
	tenant, user, slot string
	data               string
	query              url.Values
}

const dynamicData = "pending-flow-with-verifier-" + canary

func (d *fakeDynamic) Slots(ctx context.Context) ([]connect.Slot, error) {
	t, err := tenancy.Require(ctx)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.slots[t.ID()]), nil
}

func (d *fakeDynamic) RedirectURL() string { return d.redirect }

func (d *fakeDynamic) Begin(ctx context.Context, user, slot, state string) (string, []byte, error) {
	t, _ := tenancy.Require(ctx)
	d.mu.Lock()
	d.begun = append(d.begun, strings.Join([]string{t.ID(), user, slot, state}, "/"))
	d.mu.Unlock()
	if d.authURL != "" {
		return d.authURL, []byte(dynamicData), nil
	}
	return "https://as.example/authorize?" + url.Values{"state": {state}, "client_id": {"https://agentkit.example/oauth/client-metadata.json"}}.Encode(),
		[]byte(dynamicData + "/" + slot), nil
}

func (d *fakeDynamic) Complete(ctx context.Context, user, slot string, data []byte, q url.Values) error {
	t, err := tenancy.Require(ctx)
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.done = append(d.done, completed{tenant: t.ID(), user: user, slot: slot, data: string(data), query: q})
	d.mu.Unlock()
	if q.Get("code") != "good-code" {
		return errors.New("code exchange: token endpoint: invalid_grant")
	}
	return vaultOf(ctx).put(ctx, user, slot)
}

// vaultOf is how the fake stores: through the vault the test put in ctx.
type vaultKey struct{}

type vaultPut struct{ v credentials.Vault }

func (p vaultPut) put(ctx context.Context, user, slot string) error {
	_, err := p.v.Put(ctx, user, slot, credentials.OAuth2{AccessToken: credentials.NewSecret("at-" + canary)}, 0)
	return err
}

func vaultOf(ctx context.Context) vaultPut { return ctx.Value(vaultKey{}).(vaultPut) }

func (d *fakeDynamic) calls() []completed {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.done)
}

func withDynamic(d *fakeDynamic) rigOption {
	return func(r *rig, cfg *connect.Config) {
		d.redirect = r.cb.URL + connect.CallbackPath
		cfg.Dynamic = d
		// The fake stores through the rig's vault, which exists once the
		// rig is made: the callback's context carries it.
		r.wrap = func(h http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				h.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), vaultKey{}, vaultPut{r.vault})))
			})
		}
	}
}

func newDynamicRig(t *testing.T) (*rig, *fakeDynamic) {
	t.Helper()
	d := &fakeDynamic{slots: map[string][]connect.Slot{
		"tenant-a": {
			{Name: "linear", Scope: credentials.ScopeUser, Toolsets: []string{"linear"}},
			{Name: "notion", Scope: credentials.ScopeTenant, Toolsets: []string{"notion"}},
			// A slot of the config: never the dynamic one.
			{Name: "mail", Scope: credentials.ScopeUser, Toolsets: []string{"mail"}},
			{Name: "_reserved", Scope: credentials.ScopeUser},
		},
		"tenant-b": {{Name: "github", Scope: credentials.ScopeUser, Toolsets: []string{"github"}}},
	}}
	return newRig(t, withDynamic(d)), d
}

// back is where the authorization server sends the browser: the callback
// with a code (or an error) and the state of authURL.
func (r *rig) back(authURL string, extra url.Values) string {
	r.t.Helper()
	q := url.Values{"state": {mustQuery(r.t.(*testing.T), authURL).Get("state")}}
	for k, v := range extra {
		q[k] = v
	}
	return r.cb.URL + connect.CallbackPath + "?" + q.Encode()
}

func TestDynamic(t *testing.T) {
	t.Parallel()
	t.Run("connects", func(t *testing.T) {
		t.Parallel()
		r, d := newDynamicRig(t)
		ctx := ctxOf(t, "tenant-a")
		if got := r.svc.Scopes(ctx, "linear"); !slices.Equal(got, []string{"user"}) {
			t.Errorf("Scopes(linear) = %v", got)
		}
		if got := r.svc.Scopes(ctx, "notion"); !slices.Equal(got, []string{"tenant"}) {
			t.Errorf("Scopes(notion) = %v", got)
		}
		if got := r.svc.Scopes(ctxOf(t, "tenant-b"), "linear"); got != nil {
			t.Errorf("tenant B sees A's slot: %v", got)
		}
		if got := r.svc.Scopes(ctx, "_reserved"); got != nil {
			t.Errorf("a reserved slot: %v", got)
		}
		authURL := r.start("tenant-a", "u1", "linear", returnTo)
		if len(d.begun) != 1 || !strings.HasPrefix(d.begun[0], "tenant-a/u1/linear/") {
			t.Fatalf("Begin calls %v", d.begun)
		}
		if c := r.cookieFor(authURL); c == nil || c.Path != connect.CallbackPath || !c.HttpOnly {
			t.Fatalf("binding cookie %+v", c)
		}
		resp, _ := r.callback(r.back(authURL, url.Values{"code": {"good-code"}, "iss": {"https://as.example"}}))
		if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusSeeOther || !strings.Contains(loc, "result=connected") || !strings.Contains(loc, "slot=linear") {
			t.Fatalf("callback = %d %s", resp.StatusCode, loc)
		}
		calls := d.calls()
		if len(calls) != 1 || calls[0].tenant != "tenant-a" || calls[0].user != "u1" || calls[0].slot != "linear" ||
			calls[0].data != dynamicData+"/linear" || calls[0].query.Get("iss") != "https://as.example" {
			t.Fatalf("Complete calls %+v", calls)
		}
		if _, ok := r.stored("tenant-a", "u1", "linear").(credentials.OAuth2); !ok {
			t.Fatal("nothing stored for u1")
		}
		list, err := r.svc.List(ctx, "u1")
		if err != nil {
			t.Fatal(err)
		}
		var slots []string
		for _, c := range list {
			slots = append(slots, c.Slot+"/"+c.Scope+"/"+c.Status)
			if c.Slot == "linear" && (!c.OAuth || !slices.Equal(c.Toolsets, []string{"linear"})) {
				t.Errorf("linear listed as %+v", c)
			}
		}
		want := []string{"crm/tenant/not_connected", "linear/user/connected", "mail/user/not_connected", "notion/tenant/not_connected"}
		if !slices.Equal(slots, want) {
			t.Errorf("List = %v, want %v", slots, want)
		}
		if st, _ := r.svc.Status(ctx, "u2", "linear"); st != wire.CredentialNotConnected {
			t.Errorf("u2's status %s", st)
		}
		if _, err := r.svc.Put(ctx, "u1", "linear", wire.PutCredentialRequest{Kind: wire.KindAPIKey, APIKey: "k-" + canary}); err == nil {
			t.Error("an API key put at an MCP OAuth slot")
		}
		if err := r.svc.Delete(ctx, "u1", "linear"); err != nil {
			t.Fatal(err)
		}
		if r.stored("tenant-a", "u1", "linear") != nil {
			t.Error("Delete left the connection")
		}
	})
	t.Run("the tenant's connection", func(t *testing.T) {
		t.Parallel()
		r, d := newDynamicRig(t)
		resp, _ := r.callback(r.back(r.start("tenant-a", "", "notion", ""), url.Values{"code": {"good-code"}}))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("callback = %d", resp.StatusCode)
		}
		if calls := d.calls(); len(calls) != 1 || calls[0].user != "" || calls[0].tenant != "tenant-a" {
			t.Fatalf("Complete calls %+v", calls)
		}
	})
	// An authorization server's metadata names the page the browser is
	// sent to: only a web address is, never one a page would run.
	t.Run("a sign-in page that is no web address", func(t *testing.T) {
		t.Parallel()
		for _, bad := range []string{"javascript:alert(1)", "data:text/html,x", "ftp://as.example/x", "https:///nohost", "//as.example/authorize"} {
			r, d := newDynamicRig(t)
			d.authURL = bad
			if resp, cookie, err := r.svc.Connect(ctxOf(t, "tenant-a"), "u1", "linear", wire.ConnectRequest{}); err == nil {
				t.Errorf("%s: sent the browser to %q (cookie %v)", bad, resp.AuthorizeURL, cookie)
			}
		}
	})
	t.Run("a config slot is never dynamic", func(t *testing.T) {
		t.Parallel()
		r, d := newDynamicRig(t)
		authURL := r.start("tenant-a", "u1", "mail", "")
		if len(d.begun) != 0 || !strings.HasPrefix(authURL, r.ts.AuthURL) {
			t.Fatalf("mail went to Dynamic: %s %v", authURL, d.begun)
		}
	})
	t.Run("another tenant's slot", func(t *testing.T) {
		t.Parallel()
		r, d := newDynamicRig(t)
		if _, _, err := r.svc.Connect(ctxOf(t, "tenant-b"), "u1", "linear", wire.ConnectRequest{}); err == nil {
			t.Fatal("tenant B connected A's slot")
		}
		if len(d.begun) != 0 {
			t.Fatalf("Begin calls %v", d.begun)
		}
	})
	refused := map[string]func(r *rig, authURL string) (*http.Response, string){
		"no cookie": func(r *rig, authURL string) (*http.Response, string) {
			return r.callbackWith(r.back(authURL, url.Values{"code": {"good-code"}}))
		},
		"another flow's cookie": func(r *rig, authURL string) (*http.Response, string) {
			other := r.cookieFor(r.start("tenant-a", "u2", "linear", ""))
			return r.callbackWith(r.back(authURL, url.Values{"code": {"good-code"}}), &http.Cookie{Name: r.cookieFor(authURL).Name, Value: other.Value})
		},
		"expired": func(r *rig, authURL string) (*http.Response, string) {
			r.clk.Advance(connect.DefaultStateTTL + time.Second)
			return r.callback(r.back(authURL, url.Values{"code": {"good-code"}}))
		},
		"denied": func(r *rig, authURL string) (*http.Response, string) {
			return r.callback(r.back(authURL, url.Values{"error": {"access_denied"}}))
		},
		"no code": func(r *rig, authURL string) (*http.Response, string) {
			return r.callback(r.back(authURL, nil))
		},
		"two codes": func(r *rig, authURL string) (*http.Response, string) {
			return r.callback(r.back(authURL, url.Values{"code": {"good-code", "good-code"}}))
		},
		"another state": func(r *rig, authURL string) (*http.Response, string) {
			u := r.cb.URL + connect.CallbackPath + "?" + url.Values{"code": {"good-code"}, "state": {strings.Repeat("0", 32) + ".x"}}.Encode()
			return r.callbackWith(u, r.cookieFor(authURL))
		},
	}
	for name, cb := range refused {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r, d := newDynamicRig(t)
			authURL := r.start("tenant-a", "u1", "linear", "")
			resp, _ := cb(r, authURL)
			if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusSeeOther {
				t.Fatalf("callback = %d", resp.StatusCode)
			}
			if calls := d.calls(); len(calls) != 0 {
				t.Fatalf("Complete called: %+v", calls)
			}
			if r.stored("tenant-a", "u1", "linear") != nil {
				t.Fatal("stored")
			}
		})
	}
	t.Run("used once", func(t *testing.T) {
		t.Parallel()
		r, d := newDynamicRig(t)
		target := r.back(r.start("tenant-a", "u1", "linear", ""), url.Values{"code": {"bad-code"}})
		if resp, _ := r.callback(target); resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("first callback = %d, want 502 (exchange failed)", resp.StatusCode)
		}
		if resp, _ := r.callback(target); resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("second callback = %d, want 400", resp.StatusCode)
		}
		if n := len(d.calls()); n != 1 {
			t.Fatalf("Complete called %d times, want 1", n)
		}
	})
	t.Run("the toolset removed meanwhile", func(t *testing.T) {
		t.Parallel()
		r, d := newDynamicRig(t)
		authURL := r.start("tenant-a", "u1", "linear", "")
		d.mu.Lock()
		d.slots["tenant-a"] = d.slots["tenant-a"][1:]
		d.mu.Unlock()
		if resp, _ := r.callback(r.back(authURL, url.Values{"code": {"good-code"}})); resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("callback = %d, want 502", resp.StatusCode)
		}
		if len(d.calls()) != 0 {
			t.Fatal("Complete called for a removed toolset")
		}
	})
	t.Run("the flow is sealed", func(t *testing.T) {
		t.Parallel()
		rec := &recordingStore{BlobStore: newMemoryStore()}
		d := &fakeDynamic{slots: map[string][]connect.Slot{"tenant-a": {{Name: "linear", Scope: credentials.ScopeUser}}}}
		r := newRig(t, withStore(rec), withDynamic(d))
		r.start("tenant-a", "u1", "linear", "")
		if rec.saw(dynamicData) {
			t.Fatal("the flow's data is in a blob in the clear")
		}
	})
}

// recordingStore keeps every blob written, to look for plaintext.
type recordingStore struct {
	credentials.BlobStore
	mu    sync.Mutex
	blobs [][]byte
}

func (s *recordingStore) Put(ctx context.Context, user, slot string, b credentials.Blob, ifVersion int64) (credentials.Meta, error) {
	s.mu.Lock()
	s.blobs = append(s.blobs, slices.Clone(b.Data))
	s.mu.Unlock()
	return s.BlobStore.Put(ctx, user, slot, b, ifVersion)
}

func (s *recordingStore) saw(plain string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.blobs {
		if strings.Contains(string(b), plain) {
			return true
		}
	}
	return len(s.blobs) == 0 // nothing written is no proof either
}

func newMemoryStore() credentials.BlobStore { return memory.New() }

// FuzzDynamicCallback: a fuzzed callback query, on a live flow of a
// dynamic slot from the browser that started it, reaches Complete only
// with one state (the flow's), one code and no error; and never twice.
func FuzzDynamicCallback(f *testing.F) {
	for _, seed := range []string{
		"", "state={state}", "state={state}&code=good-code", "state={state}&error=access_denied&code=good-code",
		"state={state}&state={state}&code=good-code", "code=good-code", "state=%zz", "state={state}&code=",
		"state={state}&code=good-code&code=x", "state={state}x&code=good-code", "state={state}&code=good-code&iss=https://evil",
	} {
		f.Add(seed)
	}
	d := &fakeDynamic{slots: map[string][]connect.Slot{"tenant-a": {{Name: "linear", Scope: credentials.ScopeUser}}}}
	r := newRig(f, withDynamic(d))
	f.Fuzz(func(t *testing.T, raw string) {
		resp, cookie, err := r.svc.Connect(ctxOf(t, "tenant-a"), "u1", "linear", wire.ConnectRequest{})
		if err != nil {
			t.Fatal(err)
		}
		state := mustQuery(t, resp.AuthorizeURL).Get("state")
		before := len(d.calls())
		query := strings.ReplaceAll(raw, "{state}", state)
		for range 2 {
			req := httptest.NewRequest(http.MethodGet, connect.CallbackPath, nil)
			req.URL.RawQuery = query
			req.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value})
			ctx := context.WithValue(req.Context(), vaultKey{}, vaultPut{r.vault})
			r.svc.Callback().ServeHTTP(httptest.NewRecorder(), req.WithContext(ctx))
		}
		calls := d.calls()[before:]
		q, perr := url.ParseQuery(query)
		ok := perr == nil && len(q["state"]) == 1 && q.Get("state") == state && len(q["code"]) == 1 && q.Get("code") != "" && q.Get("error") == ""
		switch {
		case len(calls) > 1:
			t.Fatalf("%q: Complete called %d times", raw, len(calls))
		case len(calls) == 1 && !ok:
			t.Fatalf("%q reached Complete", raw)
		case len(calls) == 1 && (calls[0].user != "u1" || calls[0].tenant != "tenant-a"):
			t.Fatalf("%q completed for %s/%s", raw, calls[0].tenant, calls[0].user)
		}
		_ = r.vault.Delete(ctxOf(t, "tenant-a"), "u1", "linear", 0)
	})
}
