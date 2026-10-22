//go:build !noagent

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/agentapi"
	"github.com/bsamanvith/agentkit/pkg/agentapi/wire"
	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/llm/llmtest"
	"github.com/bsamanvith/agentkit/pkg/store/storeopen"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
	"github.com/bsamanvith/agentkit/pkg/trigger"
)

// TestServeStaticTriggers: an agent's "triggers" in the config are stored
// as serve starts (static, in the default tenant), and a static webhook
// trigger, signed with the secret derived from $AGENTKIT_WEBHOOK_SECRET,
// starts a background run of its agent.
func TestServeStaticTriggers(t *testing.T) {
	t.Parallel()
	const config = `{"agents": [{"name": "news", "model": {"id": "m"}, "triggers": [
	  {"id": "morning", "prompt": "Summarize the news of {{date}}", "schedule": "CRON_TZ=Europe/Paris 0 9 * * 1-5"},
	  {"id": "gh", "prompt": "Triage this issue", "webhook": true}
	]}]}`
	path := filepath.Join(t.TempDir(), "agentkit.json")
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadServeConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	model := llmtest.NewFake(t, llmtest.Text("triaged"))
	c := cli{
		getenv:   func(string) string { return "" },
		newModel: func(modelFlags) (llm.Model, error) { return model, nil },
	}
	master := bytes.Repeat([]byte("k"), minWebhookSecret)
	env := serveEnv{components: []string{agentapi.Name}, addr: "127.0.0.1:0", shutdownTimeout: time.Second, webhookSecret: master}
	log := slog.New(slog.DiscardHandler)
	o, err := storeopen.Open(t.Context(), "memory")
	if err != nil {
		t.Fatal(err)
	}
	w := &agentWiring{st: openedStore{Store: o.Store, admin: o.Admin, triggers: o.Triggers, close: func() { _ = o.Close() }}}
	built, problems, err := c.buildToolsets(t.Context(), env, cfg, path, log)
	if err != nil {
		t.Fatal(err)
	}
	m, err := c.buildAgents(t.Context(), w, cfg, built, problems, env, nil, clock.Real(), log)
	if err != nil {
		t.Fatal(err)
	}
	if w.triggers == nil {
		t.Fatal("no trigger admin kept for the scheduler")
	}
	ts := httptest.NewServer(m.Component.Handler())
	t.Cleanup(ts.Close)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = m.Component.Shutdown(ctx)
	})

	var list wire.TriggerList
	getJSON(t, ts.URL+"/v1/triggers", &list)
	if len(list.Triggers) != 2 || list.Triggers[0].ID != "gh" || list.Triggers[1].ID != "morning" {
		t.Fatalf("triggers %+v, want gh and morning", list.Triggers)
	}
	for _, tr := range list.Triggers {
		if !tr.Static || tr.Agent != "news" {
			t.Errorf("trigger %+v, want a static trigger of news", tr)
		}
	}
	if list.Triggers[1].NextFireAt == nil {
		t.Errorf("morning has no next firing")
	}
	var gh wire.Trigger
	getJSON(t, ts.URL+"/v1/triggers/gh", &gh)
	if gh.Webhook == nil {
		t.Fatalf("gh %+v has no webhook", gh)
	}
	def, err := tenancy.NewTenant(agentapi.DefaultTenant)
	if err != nil {
		t.Fatal(err)
	}
	secret := hookSecret(master, def, gh.Webhook.Path[len("/v1/hooks/"):])
	if gh.Webhook.Secret != trigger.EncodeStandardSecret(secret) {
		t.Errorf("secret %q, want the one derived from the master secret", gh.Webhook.Secret)
	}

	body := []byte(`{"issue": 7}`)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+gh.Webhook.Path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	req.Header.Set(trigger.StandardHeaderID, "msg_1")
	req.Header.Set(trigger.StandardHeaderTimestamp, strconv.FormatInt(now.Unix(), 10))
	req.Header.Set(trigger.StandardHeaderSignature, trigger.SignStandard(secret, "msg_1", now, body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var out wire.HookAccepted
	if resp.StatusCode != http.StatusAccepted || json.Unmarshal(b, &out) != nil || out.Firing.RunID == "" {
		t.Fatalf("delivery = %d %s, want 202 and a run", resp.StatusCode, b)
	}
}

func getJSON(t *testing.T, url string, v any) {
	t.Helper()
	resp, err := http.Get(url) //nolint:noctx // a test against httptest
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || json.Unmarshal(b, v) != nil {
		t.Fatalf("GET %s = %d %s", url, resp.StatusCode, b)
	}
}
