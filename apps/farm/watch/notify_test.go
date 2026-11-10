package watch_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/automation"
	"github.com/bsamanvith/agentkit/apps/farm/notify"
	"github.com/bsamanvith/agentkit/apps/farm/notify/notifytest"
	notifysqlite "github.com/bsamanvith/agentkit/apps/farm/notify/sqlite"
	"github.com/bsamanvith/agentkit/apps/farm/watch"
)

func TestNoticesBecomeMessages(t *testing.T) {
	ctx := context.Background()
	s, err := notifysqlite.Open(ctx, filepath.Join(t.TempDir(), "farm.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2026, 11, 10, 6, 0, 0, 0, time.UTC)
	for _, r := range []notify.Recipient{
		{ID: "owner", Name: "Ravi", Telegram: "1", Consent: map[string]notify.Consent{"telegram": {OptedIn: true}}},
		{ID: "son", Name: "Kiran", Telegram: "2", Farms: []string{"f2"}, Consent: map[string]notify.Consent{"telegram": {OptedIn: true}}},
	} {
		if err := s.PutRecipient(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	lane := notifytest.NewFake("telegram", notify.AddrTelegram)
	d, err := notify.NewDispatcher(s, notify.Guard(lane, s))
	if err != nil {
		t.Fatal(err)
	}
	ch := watch.Messages(d, func(farm string) string { return "https://farm.example/#/farms/" + farm })
	n := automation.Notice{ID: "note_1", Farm: "f1", Severity: automation.Urgent, Title: "Heavy rain Thursday", Body: "Clear the drains.", CreatedAt: now}
	for range 2 {
		if err := ch.Deliver(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	n2 := n
	n2.ID, n2.Farm = "note_2", "f2"
	if err := ch.Deliver(ctx, n2); err != nil {
		t.Fatal(err)
	}
	d.Pass(ctx)
	got := map[string]string{}
	for _, m := range lane.Sent() {
		got[m.ID] = m.Recipient.Name + " " + m.Severity + " " + m.Link
	}
	want := map[string]string{
		"note_1:owner": "Ravi urgent https://farm.example/#/farms/f1",
		"note_2:owner": "Ravi urgent https://farm.example/#/farms/f2",
		"note_2:son":   "Kiran urgent https://farm.example/#/farms/f2",
	}
	if len(got) != len(want) || len(lane.Sent()) != 3 {
		t.Fatalf("sent %v, want %v (once each; the son hears only about f2)", got, want)
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s: %q, want %q", id, got[id], w)
		}
	}
}
