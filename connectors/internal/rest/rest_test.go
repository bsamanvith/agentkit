package rest_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/connectors/internal/rest"
	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
)

// The Google family's tests (connectors/google/common) cover the client's
// retries, limits and redaction through it; these cover what a provider
// configures.

func parse(body []byte) (string, []string) {
	return strings.TrimSpace(string(body)), []string{strings.TrimSpace(string(body))}
}

func client(t *testing.T, h http.HandlerFunc, cfg rest.Config) *rest.Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	cfg.BaseURL = srv.URL
	if cfg.Tokens == nil {
		cfg.Tokens = rest.Static("tok-1")
	}
	return rest.New(cfg)
}

func status(code int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
}

func TestStatusErrorsNameServiceAndAccount(t *testing.T) {
	t.Parallel()
	cfg := rest.Config{Service: "hubspot", Account: "HubSpot account", ParseError: parse}
	for _, c := range []struct {
		code    int
		body    string
		invalid bool
		want    string
	}{
		{400, "bad property", true, "hubspot rejected the request (status 400): bad property"},
		{409, "Contact already exists. Existing ID: 51", true, "Existing ID: 51"},
		{422, "", true, "no details"},
		{401, "", false, "the HubSpot account must be reconnected"},
		{403, "missing scope crm.objects.deals.write", false, "denied access (status 403): missing scope"},
		{404, "tok-1 not here", false, "not found (status 404): [redacted] not here"},
		{500, "boom", false, "hubspot returned status 500"},
	} {
		_, err := client(t, status(c.code, c.body), cfg).Do(t.Context(), rest.Request{Path: "/x"})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%d: err %v, want %q", c.code, err, c.want)
		}
		if _, ok := errors.AsType[*rest.InputError](err); ok != c.invalid {
			t.Errorf("%d: input error %v, want %v", c.code, ok, c.invalid)
		}
	}
}

// A provider's own rate-limit answer is retried once, like a 429.
func TestRateLimitedHook(t *testing.T) {
	t.Parallel()
	n := 0
	h := func(w http.ResponseWriter, _ *http.Request) {
		n++
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("REQUEST_LIMIT_EXCEEDED"))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}
	cfg := rest.Config{
		Service: "salesforce", ParseError: parse,
		RateLimited: func(status int, reasons []string) bool {
			return status == http.StatusForbidden && len(reasons) > 0 && reasons[0] == "REQUEST_LIMIT_EXCEEDED"
		},
	}
	if _, err := client(t, h, cfg).Do(t.Context(), rest.Request{Path: "/x"}); err != nil || n != 2 {
		t.Fatalf("err %v after %d requests, want success after a retry", err, n)
	}
}

// With sends to another address with another token, leaving c as it was.
func TestWith(t *testing.T) {
	t.Parallel()
	var got []string
	h := func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Host+" "+r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{}`))
	}
	a := httptest.NewServer(http.HandlerFunc(h))
	b := httptest.NewServer(http.HandlerFunc(h))
	t.Cleanup(a.Close)
	t.Cleanup(b.Close)
	c := rest.New(rest.Config{Service: "s", BaseURL: a.URL, Tokens: rest.Static("ta")})
	if err := c.With(b.URL+"/", rest.Static("tb")).DoJSON(t.Context(), rest.Request{Path: "/x"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := c.DoJSON(t.Context(), rest.Request{Path: "/x"}, nil); err != nil {
		t.Fatal(err)
	}
	want := strings.TrimPrefix(b.URL, "http://") + " Bearer tb," + strings.TrimPrefix(a.URL, "http://") + " Bearer ta"
	if strings.Join(got, ",") != want {
		t.Errorf("requests %q, want %q", got, want)
	}
}

// A slot's token is the caller's; no token, or another kind of credential,
// sends nothing and says what to connect.
func TestSlotTokens(t *testing.T) {
	t.Parallel()
	n := 0
	h := func(w http.ResponseWriter, r *http.Request) {
		n++
		if r.Header.Get("Authorization") != "Bearer ann-tok" {
			t.Errorf("Authorization %q", r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{}`))
	}
	src := toolsettest.NewKeySource("hubspot")
	src.Set("ann", credentials.OAuth2{AccessToken: credentials.NewSecret("ann-tok"), Extra: map[string]string{"k": "v"}})
	src.SetKey("bob", "an-api-key")
	c := client(t, h, rest.Config{Service: "hubspot", Account: "HubSpot account", Tokens: rest.FromSource(src, "HubSpot account")})
	if _, err := c.Do(toolsettest.AsUser(t, "ann"), rest.Request{Path: "/x"}); err != nil {
		t.Fatalf("ann: %v", err)
	}
	_, err := c.Do(toolsettest.AsUser(t, "carol"), rest.Request{Path: "/x"})
	if !errors.Is(err, rest.ErrNoToken) || err.Error() != "hubspot: no HubSpot account is connected; connect one and try again" {
		t.Errorf("carol: err %v", err)
	}
	_, err = c.Do(toolsettest.AsUser(t, "bob"), rest.Request{Path: "/x"})
	if !errors.Is(err, rest.ErrNotOAuth) || !strings.Contains(err.Error(), "connect the HubSpot account instead") || strings.Contains(err.Error(), "an-api-key") {
		t.Errorf("bob: err %v", err)
	}
	if n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
	tok, err := rest.SlotToken(toolsettest.AsUser(t, "ann"), src, "HubSpot account")
	if err != nil || tok.Extra["k"] != "v" {
		t.Errorf("SlotToken: %v %v, want the token with its extras", tok, err)
	}
}

func TestSettings(t *testing.T) {
	t.Parallel()
	s := rest.Settings{BaseURL: "http://localhost:9/"}
	if err := s.Validate("hubspot", "HUBSPOT_ACCESS_TOKEN"); err != nil || s.AccessTokenEnv != "HUBSPOT_ACCESS_TOKEN" || s.TokenEnvSet() || s.BaseURL != "http://localhost:9" {
		t.Errorf("defaults: %v %+v", err, s)
	}
	s = rest.Settings{AccessTokenEnv: "MINE", Timeout: "10m"}
	if err := s.Validate("hubspot", "X"); err == nil || !s.TokenEnvSet() {
		t.Errorf("timeout over the limit: %v, %+v", err, s)
	}
	err := rest.RefuseLiteralToken("hubspot", "HUBSPOT_ACCESS_TOKEN", []byte(`{"access_token": "pat-na1-secret"}`))
	if err == nil || strings.Contains(err.Error(), "pat-na1-secret") || !strings.Contains(err.Error(), "HUBSPOT_ACCESS_TOKEN") {
		t.Errorf("literal token: %v", err)
	}
}
