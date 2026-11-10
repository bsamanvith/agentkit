package sms_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha1" //nolint:gosec // Twilio's signature
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/notify"
	"github.com/bsamanvith/agentkit/apps/farm/notify/notifytest"
	"github.com/bsamanvith/agentkit/apps/farm/notify/sms"
)

type fake struct {
	mu    sync.Mutex
	got   []string
	head  []http.Header
	fails []func(http.ResponseWriter)
	ok    func(http.ResponseWriter)
}

func (f *fake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.head = append(f.head, r.Header.Clone())
	if len(f.fails) > 0 {
		h := f.fails[0]
		f.fails = f.fails[1:]
		h(w)
		return
	}
	text := string(body)
	if u, err := url.QueryUnescape(text); err == nil {
		text = u
	}
	f.got = append(f.got, r.URL.Path+" "+text)
	f.ok(w)
}

func (f *fake) harness(t *testing.T, l notify.Lane, fails map[string]func(http.ResponseWriter)) notifytest.LaneHarness {
	return notifytest.LaneHarness{
		Lane: l,
		To:   notify.Recipient{ID: "r1", Phone: "+919876543210"},
		Fail: func(kind string) bool {
			h, ok := fails[kind]
			if ok {
				f.mu.Lock()
				f.fails = append(f.fails, h)
				f.mu.Unlock()
			}
			return ok
		},
		Got: func() []string {
			f.mu.Lock()
			defer f.mu.Unlock()
			return append([]string(nil), f.got...)
		},
	}
}

func answer(status int, body string) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}
}

func TestMSG91(t *testing.T) {
	notifytest.RunLane(t, func(t *testing.T) notifytest.LaneHarness {
		f := &fake{ok: answer(200, `{"message":"3763646c3058373530393831","type":"success"}`)}
		srv := httptest.NewServer(f)
		t.Cleanup(srv.Close)
		l, err := sms.MSG91(sms.MSG91Config{AuthKey: "KEY", TemplateID: "TPL", URL: srv.URL + "/api/v5/flow", VarLen: 40})
		if err != nil {
			t.Fatal(err)
		}
		return f.harness(t, l, map[string]func(http.ResponseWriter){
			notifytest.FailTemporary: answer(503, `{"type":"error","message":"busy"}`),
			notifytest.FailRejected:  answer(400, `{"type":"error","message":"Template not approved"}`),
		})
	})
}

func TestMSG91SendsDLTVariables(t *testing.T) {
	f := &fake{ok: answer(200, `{"message":"req-1","type":"success"}`)}
	srv := httptest.NewServer(f)
	defer srv.Close()
	l, _ := sms.MSG91(sms.MSG91Config{AuthKey: "KEY", TemplateID: "TPL", URL: srv.URL})
	m := notify.Message{
		ID: "m1", Title: "Heavy rain on Thursday at the east plot", Body: "Clear the drains",
		Recipient: notify.Recipient{Phone: "+919876543210"},
	}
	rc, err := l.Deliver(context.Background(), m)
	if err != nil || rc.ProviderID != "req-1" {
		t.Fatalf("Deliver = %+v, %v", rc, err)
	}
	var sent struct {
		TemplateID string              `json:"template_id"`
		Recipients []map[string]string `json:"recipients"`
	}
	_ = json.Unmarshal([]byte(strings.TrimPrefix(f.got[0], "/ ")), &sent)
	r := sent.Recipients[0]
	if sent.TemplateID != "TPL" || r["mobiles"] != "919876543210" || r["var1"] != "Heavy rain on Thursday at the…" || r["var2"] != "Clear the drains" {
		t.Errorf("sent %s: want the DLT variables at 30 characters", f.got[0])
	}
	if f.head[0].Get("authkey") != "KEY" {
		t.Error("no authkey header")
	}
}

func twilioFake(t *testing.T) (*fake, *httptest.Server) {
	f := &fake{ok: answer(201, `{"sid":"SM123","status":"queued"}`)}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func TestTwilio(t *testing.T) {
	notifytest.RunLane(t, func(t *testing.T) notifytest.LaneHarness {
		f, srv := twilioFake(t)
		l, err := sms.NewTwilio(sms.TwilioConfig{AccountSID: "AC1", AuthToken: "tok", From: "+15005550006", URL: srv.URL})
		if err != nil {
			t.Fatal(err)
		}
		return f.harness(t, l, map[string]func(http.ResponseWriter){
			notifytest.FailTemporary: answer(429, `{"code":20429,"message":"Too Many Requests","status":429}`),
			notifytest.FailRejected:  answer(400, `{"code":21211,"message":"The 'To' number is not a valid phone number.","status":400}`),
			notifytest.FailGone:      answer(400, `{"code":21610,"message":"Attempt to send to unsubscribed recipient","status":400}`),
		})
	})
}

func TestTwilioRequest(t *testing.T) {
	f, srv := twilioFake(t)
	l, _ := sms.NewTwilio(sms.TwilioConfig{AccountSID: "AC1", AuthToken: "tok", MessagingService: "MG9", URL: srv.URL, CallbackURL: "https://farm.example/hooks/twilio"})
	_, err := l.Deliver(context.Background(), notify.Message{Title: "Rain", Body: "Cover the harvest", Recipient: notify.Recipient{Phone: "+919876543210"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"/2010-04-01/Accounts/AC1/Messages.json", "Body=Rain: Cover the harvest", "MessagingServiceSid=MG9", "StatusCallback=https://farm.example/hooks/twilio", "To=+919876543210"} {
		if !strings.Contains(f.got[0], want) {
			t.Errorf("the request lacks %q: %s", want, f.got[0])
		}
	}
	if u, p, _ := (&http.Request{Header: f.head[0]}).BasicAuth(); u != "AC1" || p != "tok" {
		t.Error("no basic auth")
	}
	if _, err := sms.NewTwilio(sms.TwilioConfig{AccountSID: "AC1", AuthToken: "tok", From: "+1", MessagingService: "MG9"}); err == nil {
		t.Error("both a from number and a messaging service were taken")
	}
}

type report struct{ logs []string }

func (r *report) Status(_ context.Context, lane, id, status, errText string) {
	r.logs = append(r.logs, "status "+lane+" "+id+" "+status+" "+errText)
}

func (r *report) Stop(_ context.Context, lane string, _ notify.AddressKind, addr string) {
	r.logs = append(r.logs, "stop "+lane+" "+addr)
}

func (r *report) Start(_ context.Context, lane string, _ notify.AddressKind, addr string) {
	r.logs = append(r.logs, "start "+lane+" "+addr)
}

func (r *report) Link(context.Context, string, string, notify.AddressKind, string) (string, error) {
	return "", nil
}

func TestTwilioWebhook(t *testing.T) {
	const hook = "https://farm.example/hooks/twilio"
	l, _ := sms.NewTwilio(sms.TwilioConfig{AccountSID: "AC1", AuthToken: "tok", From: "+15005550006", CallbackURL: hook})
	rep := &report{}
	h := l.Webhook(rep)
	sign := func(form url.Values) string {
		keys := []string{}
		for k := range form {
			keys = append(keys, k)
		}
		// sorted by hand, the way Twilio's documentation shows it
		var b strings.Builder
		b.WriteString(hook)
		for _, k := range sortStrings(keys) {
			b.WriteString(k + form.Get(k))
		}
		mac := hmac.New(sha1.New, []byte("tok"))
		mac.Write([]byte(b.String()))
		return base64.StdEncoding.EncodeToString(mac.Sum(nil))
	}
	post := func(form url.Values, sig string) int {
		req := httptest.NewRequest(http.MethodPost, "/hooks/twilio", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Twilio-Signature", sig)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	status := url.Values{"MessageSid": {"SM123"}, "MessageStatus": {"undelivered"}, "ErrorCode": {"30003"}}
	if code := post(status, "bm90IGl0"); code != http.StatusUnauthorized {
		t.Errorf("a bad signature = %d", code)
	}
	if code := post(status, sign(status)); code != 200 {
		t.Errorf("a signed status = %d", code)
	}
	stop := url.Values{"From": {"+919876543210"}, "Body": {"STOP"}, "MessageSid": {"SM9"}}
	if code := post(stop, sign(stop)); code != 200 {
		t.Errorf("a signed STOP = %d", code)
	}
	want := "status sms SM123 failed twilio 30003\nstop sms +919876543210"
	if got := strings.Join(rep.logs, "\n"); got != want {
		t.Errorf("reported %q, want %q", got, want)
	}
}

func sortStrings(s []string) []string {
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
	return s
}
