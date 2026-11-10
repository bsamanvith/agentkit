package whatsapp_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/notify"
	"github.com/bsamanvith/agentkit/apps/farm/notify/notifytest"
	"github.com/bsamanvith/agentkit/apps/farm/notify/whatsapp"
)

// graph is a fake of the Cloud API's messages endpoint.
type graph struct {
	mu    sync.Mutex
	got   []string
	auth  []string
	fails []func(w http.ResponseWriter)
}

func (g *graph) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	g.mu.Lock()
	defer g.mu.Unlock()
	if r.URL.Path != "/v23.0/PHONE_ID/messages" {
		http.Error(w, `{"error":{"message":"unknown path","code":100}}`, http.StatusBadRequest)
		return
	}
	g.auth = append(g.auth, r.Header.Get("Authorization"))
	if len(g.fails) > 0 {
		f := g.fails[0]
		g.fails = g.fails[1:]
		f(w)
		return
	}
	g.got = append(g.got, string(body))
	_, _ = io.WriteString(w, `{"messaging_product":"whatsapp","contacts":[{"input":"x","wa_id":"x"}],"messages":[{"id":"wamid.HBgM1"}]}`)
}

func apiErr(status, code int) func(w http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": "nope", "code": code}})
	}
}

func newLane(t *testing.T, g *graph) *whatsapp.Lane {
	t.Helper()
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	l, err := whatsapp.New(whatsapp.Config{
		Token: "TOKEN", PhoneNumberID: "PHONE_ID", URL: srv.URL + "/v23.0", Languages: []string{"en", "te"},
		AppSecret: "app-secret", VerifyToken: "verify-me",
	})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestLane(t *testing.T) {
	notifytest.RunLane(t, func(t *testing.T) notifytest.LaneHarness {
		g := &graph{}
		return notifytest.LaneHarness{
			Lane: newLane(t, g),
			To:   notify.Recipient{ID: "r1", Phone: "+919876543210"},
			Fail: func(kind string) bool {
				f := map[string]func(http.ResponseWriter){
					notifytest.FailTemporary: apiErr(http.StatusBadRequest, 130429), // the throughput limit
					notifytest.FailRejected:  apiErr(http.StatusBadRequest, 132001), // no such template
					notifytest.FailGone:      apiErr(http.StatusBadRequest, 131050), // stopped messages from businesses
				}[kind]
				g.mu.Lock()
				g.fails = append(g.fails, f)
				g.mu.Unlock()
				return true
			},
			Got: func() []string {
				g.mu.Lock()
				defer g.mu.Unlock()
				return append([]string(nil), g.got...)
			},
		}
	})
}

func TestTemplateAndReply(t *testing.T) {
	g := &graph{}
	l := newLane(t, g)
	to := notify.Recipient{ID: "r1", Phone: "+919876543210", Lang: "te"}
	m := notify.Message{ID: "m1", Kind: notify.KindAlert, Severity: notify.Urgent, Title: "Heavy rain", Body: "Clear the drains.\n\tToday.     Now.", Recipient: to}
	rc, err := l.Deliver(t.Context(), m)
	if err != nil || rc.ProviderID != "wamid.HBgM1" {
		t.Fatalf("Deliver = %+v, %v", rc, err)
	}
	var sent struct {
		To       string `json:"to"`
		Type     string `json:"type"`
		Template struct {
			Name     string `json:"name"`
			Language struct {
				Code string `json:"code"`
			} `json:"language"`
			Components []struct {
				Parameters []struct {
					Text string `json:"text"`
				} `json:"parameters"`
			} `json:"components"`
		} `json:"template"`
	}
	if err := json.Unmarshal([]byte(g.got[0]), &sent); err != nil {
		t.Fatal(err)
	}
	if sent.To != "919876543210" || sent.Type != "template" || sent.Template.Name != "farm_alert" || sent.Template.Language.Code != "te" {
		t.Errorf("sent %s", g.got[0])
	}
	if p := sent.Template.Components[0].Parameters; len(p) != 2 || p[0].Text != "Heavy rain" || p[1].Text != "Clear the drains. Today. Now." {
		t.Errorf("parameters %+v: want the title and the text with no new lines, tabs or runs of spaces", p)
	}
	if g.auth[0] != "Bearer TOKEN" {
		t.Errorf("Authorization %q", g.auth[0])
	}
	to.Lang = "hi" // no Hindi template: the first language
	m.Recipient, m.Kind = to, notify.KindReply
	if _, err := l.Deliver(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(g.got[1], `"type":"text"`) || !strings.Contains(g.got[1], "Heavy rain\\n\\nClear") {
		t.Errorf("a reply is plain text: %s", g.got[1])
	}
}

type report struct {
	mu   sync.Mutex
	logs []string
}

func (r *report) add(s string) { r.mu.Lock(); r.logs = append(r.logs, s); r.mu.Unlock() }

func (r *report) Status(_ context.Context, lane, id, status, errText string) {
	r.add("status " + lane + " " + id + " " + status + " " + errText)
}

func (r *report) Stop(_ context.Context, lane string, _ notify.AddressKind, addr string) {
	r.add("stop " + lane + " " + addr)
}

func (r *report) Start(_ context.Context, lane string, _ notify.AddressKind, addr string) {
	r.add("start " + lane + " " + addr)
}

func (r *report) Link(context.Context, string, string, notify.AddressKind, string) (string, error) {
	return "", notify.ErrNotFound
}

func TestWebhook(t *testing.T) {
	l := newLane(t, &graph{})
	rep := &report{}
	h := l.Webhook(rep)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hooks/whatsapp?hub.mode=subscribe&hub.verify_token=verify-me&hub.challenge=1158201444", nil))
	if rec.Code != 200 || rec.Body.String() != "1158201444" {
		t.Errorf("subscription check = %d %q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/hooks/whatsapp?hub.mode=subscribe&hub.verify_token=wrong&hub.challenge=1", nil))
	if rec.Code != http.StatusForbidden {
		t.Errorf("a wrong verify token = %d", rec.Code)
	}

	body := `{"object":"whatsapp_business_account","entry":[{"id":"1","changes":[{"field":"messages","value":{
		"statuses":[{"id":"wamid.A","status":"read","recipient_id":"919876543210"},
		            {"id":"wamid.B","status":"failed","errors":[{"code":131026,"title":"Message undeliverable"}]}],
		"messages":[{"from":"919876543210","type":"text","text":{"body":"STOP"}},
		            {"from":"919800000000","type":"button","button":{"text":"Start"}},
		            {"from":"919811111111","type":"text","text":{"body":"when should I sow?"}}]}}]}]}`
	sign := func(b string) string {
		mac := hmac.New(sha256.New, []byte("app-secret"))
		mac.Write([]byte(b))
		return "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	for name, sig := range map[string]string{"unsigned": "", "badly signed": sign(body + " ")} {
		rec = httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/hooks/whatsapp", strings.NewReader(body))
		req.Header.Set("X-Hub-Signature-256", sig)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s = %d, want 401", name, rec.Code)
		}
	}
	if len(rep.logs) != 0 {
		t.Fatalf("unsigned events were reported: %v", rep.logs)
	}
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/hooks/whatsapp", strings.NewReader(body))
	req.Header.Set("X-Hub-Signature-256", sign(body))
	h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Fatalf("signed = %d", rec.Code)
	}
	want := []string{
		"status whatsapp wamid.A read ",
		"status whatsapp wamid.B failed 131026 Message undeliverable",
		"stop whatsapp +919876543210",
		"start whatsapp +919800000000",
	}
	if strings.Join(rep.logs, "\n") != strings.Join(want, "\n") {
		t.Errorf("reported:\n%s\nwant:\n%s", strings.Join(rep.logs, "\n"), strings.Join(want, "\n"))
	}
}

// TestCodeTemplate: a sign-in code goes as the authentication template,
// the code in the body and the copy-code button; without one, no code is
// sent in the alert template.
func TestCodeTemplate(t *testing.T) {
	g := &graph{}
	srv := httptest.NewServer(g)
	t.Cleanup(srv.Close)
	m := notify.Message{
		ID: "c1", Kind: notify.KindCode, Severity: notify.Urgent, Body: "482913 is your sign-in code.", Code: "482913",
		Recipient: notify.Recipient{ID: "signin:x", Phone: "+919876543210"},
	}
	plain, _ := whatsapp.New(whatsapp.Config{Token: "TOKEN", PhoneNumberID: "PHONE_ID", URL: srv.URL + "/v23.0"})
	if _, err := plain.Deliver(t.Context(), m); !errors.Is(err, notify.ErrRejected) || len(g.got) != 0 {
		t.Fatalf("no code template: %v after %d sends, want ErrRejected and none", err, len(g.got))
	}
	// The alert template is in Telugu too; the code template only in English.
	m.Recipient.Lang = "te"
	l, _ := whatsapp.New(whatsapp.Config{Token: "TOKEN", PhoneNumberID: "PHONE_ID", URL: srv.URL + "/v23.0", Languages: []string{"te", "en"}, CodeTemplate: "farm_code"})
	if _, err := l.Deliver(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	var sent struct {
		To       string `json:"to"`
		Type     string `json:"type"`
		Template struct {
			Name     string `json:"name"`
			Language struct {
				Code string `json:"code"`
			} `json:"language"`
			Components []struct {
				Type       string `json:"type"`
				SubType    string `json:"sub_type"`
				Index      string `json:"index"`
				Parameters []struct {
					Text string `json:"text"`
				} `json:"parameters"`
			} `json:"components"`
		} `json:"template"`
	}
	if err := json.Unmarshal([]byte(g.got[0]), &sent); err != nil {
		t.Fatal(err)
	}
	c := sent.Template.Components
	if sent.To != "919876543210" || sent.Type != "template" || sent.Template.Language.Code != "en" {
		t.Errorf("sent %s: want the template to the number, in the code template's language", g.got[0])
	}
	if sent.Template.Name != "farm_code" || len(c) != 2 || c[0].Type != "body" || c[0].Parameters[0].Text != "482913" ||
		c[1].Type != "button" || c[1].SubType != "url" || c[1].Index != "0" || c[1].Parameters[0].Text != "482913" {
		t.Errorf("sent %s: want the authentication template with the code in the body and the button", g.got[0])
	}
}
