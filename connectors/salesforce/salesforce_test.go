package salesforce_test

import (
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/connectors/salesforce"
	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
	"github.com/bsamanvith/agentkit/pkg/toolset/toolsettest"
)

// recorder is a transport that answers every request with an empty query
// page and records where each went and with which token.
type recorder struct {
	mu   sync.Mutex
	urls []string
	auth []string
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	r.urls = append(r.urls, req.URL.Scheme+"://"+req.URL.Host+req.URL.Path)
	r.auth = append(r.auth, req.Header.Get("Authorization"))
	r.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"totalSize": 0, "done": true, "records": []}`)), Request: req,
	}, nil
}

func (r *recorder) seen() ([]string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.urls...), append([]string(nil), r.auth...)
}

func build(t *testing.T, rt http.RoundTripper, settings string, src credentials.Source, opts ...salesforce.Option) ([]tool.Tool, error) {
	t.Helper()
	opts = append([]salesforce.Option{
		salesforce.WithHTTPClient(&http.Client{Transport: rt}),
		salesforce.WithLookupEnv(func(k string) (string, bool) { return "env-token", k == salesforce.DefaultTokenEnv }),
	}, opts...)
	if settings == "" {
		settings = "{}"
	}
	return salesforce.Factory(opts...).Build(t.Context(), toolset.BuildInput{Settings: json.RawMessage(settings), Credentials: src})
}

func find(t *testing.T, tools []tool.Tool, name string) tool.Tool {
	t.Helper()
	for _, tl := range tools {
		if tl.Definition().Name == name {
			return tl
		}
	}
	t.Fatalf("no tool %s", name)
	return nil
}

const selectContacts = `{"query": "SELECT Id FROM Contact"}`

// Criterion: a query that is not a SELECT, or holds a ";", is refused
// before any request.
func TestQueryMustBeSelect(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{"SELECT Id FROM Contact", "  select Name from Account where Name like 'A%'", "SELECT\tId FROM Lead"} {
		if err := salesforce.CheckQuery(ok); err != nil {
			t.Errorf("%q: %v, want ok", ok, err)
		}
	}
	rec := &recorder{}
	tools, err := build(t, rec, `{"instance_url": "https://acme.my.salesforce.com"}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	q := find(t, tools, "salesforce_query")
	for _, bad := range []string{
		"", "DELETE FROM Contact", "SELECTId FROM Contact", "UPDATE Contact SET x = 1",
		"SELECT Id FROM Contact; DELETE FROM Contact", "FIND {x}", strings.Repeat("SELECT Id ", 3000),
	} {
		in, _ := json.Marshal(map[string]string{"query": bad})
		if _, err := q.Invoke(t.Context(), in); !errors.Is(err, tool.ErrInvalidInput) {
			t.Errorf("%.40q: err %v, want ErrInvalidInput", bad, err)
		}
	}
	if urls, _ := rec.seen(); len(urls) != 0 {
		t.Errorf("requests sent: %v", urls)
	}
}

// Criterion (property): whatever the text, its escaped form has no
// unescaped reserved character, so it cannot end FIND {...} or add an
// operator, and unescaping it gives the text back (control characters as
// spaces).
func TestSOSLEscapeCannotBreakOut(t *testing.T) {
	t.Parallel()
	const reserved = `\?&|!{}[]()^~*:"'+-`
	pool := []rune(reserved + "abcXYZ 09é漢\n\t\x00")
	rng := rand.New(rand.NewPCG(1, 2))
	for range 5000 {
		n := rng.IntN(30)
		text := make([]rune, n)
		for i := range text {
			text[i] = pool[rng.IntN(len(pool))]
		}
		esc := []rune(salesforce.EscapeSOSL(string(text)))
		var back []rune
		for i := 0; i < len(esc); i++ {
			r := esc[i]
			switch {
			case r == '\\':
				if i+1 == len(esc) || !strings.ContainsRune(reserved, esc[i+1]) {
					t.Fatalf("%q -> %q: a backslash escapes nothing reserved", string(text), string(esc))
				}
				back = append(back, esc[i+1])
				i++
			case strings.ContainsRune(reserved, r):
				t.Fatalf("%q -> %q: %q is not escaped", string(text), string(esc), r)
			case r < ' ':
				t.Fatalf("%q -> %q: control character kept", string(text), string(esc))
			default:
				back = append(back, r)
			}
		}
		want := strings.Map(func(r rune) rune {
			if r < ' ' {
				return ' '
			}
			return r
		}, string(text))
		if string(back) != want {
			t.Fatalf("%q -> %q unescapes to %q", string(text), string(esc), string(back))
		}
		q := salesforce.SOSL(string(text), []string{"Account"}, 5)
		if !strings.HasPrefix(q, "FIND {") || !strings.HasSuffix(q, "} IN ALL FIELDS RETURNING Account(Id, Name) LIMIT 5") {
			t.Fatalf("SOSL %q", q)
		}
	}
}

// Criterion: with a slot, each call goes to the org in the caller's token
// (instance_url) with that token; an instance_url off Salesforce's domains,
// or none, fails the call without a request.
func TestInstanceFromToken(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	src := toolsettest.NewKeySource("salesforce")
	tok := func(at, instance string) credentials.OAuth2 {
		o := credentials.OAuth2{AccessToken: credentials.NewSecret(at)}
		if instance != "" {
			o.Extra = map[string]string{"instance_url": instance}
		}
		return o
	}
	src.Set("ann", tok("ann-at", "https://acme.my.salesforce.com"))
	src.Set("bob", tok("bob-at", "https://globex--uat.sandbox.my.salesforce.com/"))
	src.Set("eve", tok("eve-at", "https://acme.my.salesforce.com.evil.example"))
	src.Set("dan", tok("dan-at", ""))
	tools, err := build(t, rec, "", src)
	if err != nil {
		t.Fatal(err)
	}
	q := find(t, tools, "salesforce_query")
	for _, u := range []string{"ann", "bob"} {
		if _, err := q.Invoke(toolsettest.AsUser(t, u), json.RawMessage(selectContacts)); err != nil {
			t.Fatalf("%s: %v", u, err)
		}
	}
	if _, err := q.Invoke(toolsettest.AsUser(t, "eve"), json.RawMessage(selectContacts)); err == nil || !strings.Contains(err.Error(), "not on a Salesforce domain") {
		t.Errorf("eve: err %v, want the host refused", err)
	}
	if _, err := q.Invoke(toolsettest.AsUser(t, "dan"), json.RawMessage(selectContacts)); !errors.Is(err, salesforce.ErrNoInstance) {
		t.Errorf("dan: err %v, want ErrNoInstance", err)
	}
	if _, err := q.Invoke(toolsettest.AsUser(t, "carol"), json.RawMessage(selectContacts)); err == nil || !strings.Contains(err.Error(), "no Salesforce org is connected") {
		t.Errorf("carol: err %v, want not connected", err)
	}
	urls, auth := rec.seen()
	if strings.Join(urls, ",") != "https://acme.my.salesforce.com/services/data/v61.0/query,https://globex--uat.sandbox.my.salesforce.com/services/data/v61.0/query" ||
		strings.Join(auth, ",") != "Bearer ann-at,Bearer bob-at" {
		t.Errorf("requests %v with %v, want each user's org and token", urls, auth)
	}

	// Without a slot, instance_url is checked when the toolset is built.
	for _, bad := range []string{`{"instance_url": "https://evil.example"}`, `{"instance_url": "http://acme.my.salesforce.com"}`, `{}`} {
		if _, err := build(t, rec, bad, nil); !errors.Is(err, toolset.ErrInvalidSettings) {
			t.Errorf("%s: err %v, want ErrInvalidSettings", bad, err)
		}
	}
	if _, err := build(t, rec, `{"instance_url": "https://acme.my.salesforce.com"}`, src); !errors.Is(err, toolset.ErrInvalidSettings) {
		t.Errorf("a slot and instance_url: err %v, want ErrInvalidSettings", err)
	}
}

// api_version picks the path's version.
func TestAPIVersionSetting(t *testing.T) {
	t.Parallel()
	rec := &recorder{}
	tools, err := build(t, rec, `{"instance_url": "https://acme.my.salesforce.com", "api_version": "v62.0"}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := find(t, tools, "salesforce_query").Invoke(t.Context(), json.RawMessage(selectContacts)); err != nil {
		t.Fatal(err)
	}
	if urls, auth := rec.seen(); urls[0] != "https://acme.my.salesforce.com/services/data/v62.0/query" || auth[0] != "Bearer env-token" {
		t.Errorf("request %v %v", urls, auth)
	}
	if _, err := build(t, rec, `{"instance_url": "https://acme.my.salesforce.com", "api_version": "62"}`, nil); !errors.Is(err, toolset.ErrInvalidSettings) {
		t.Errorf("api_version 62: err %v", err)
	}
}

// Criterion: writes are absent by default and medium risk when on.
func TestGatingFollowsSettings(t *testing.T) {
	t.Parallel()
	names := func(tools []tool.Tool) string {
		var out []string
		for _, tl := range tools {
			out = append(out, tl.Definition().Name)
		}
		return strings.Join(out, ",")
	}
	base := `"instance_url": "https://acme.my.salesforce.com"`
	tools, err := build(t, &recorder{}, `{`+base+`}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(tools); got != "salesforce_query,salesforce_search,salesforce_get_record,salesforce_describe_object" {
		t.Errorf("default tools %s, want the read tools", got)
	}
	tools, err = build(t, &recorder{}, `{`+base+`, "read_only": false}`, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 6 {
		t.Fatalf("tools %s, want six", names(tools))
	}
	for _, tl := range tools[4:] {
		if m := tl.Definition().Metadata; m.Kind != tool.KindWrite || m.Risk != tool.RiskMedium {
			t.Errorf("%s: kind %s risk %s, want write and medium", tl.Definition().Name, m.Kind, m.Risk)
		}
	}
}
