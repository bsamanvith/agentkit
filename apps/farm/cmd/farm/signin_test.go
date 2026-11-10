package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/notify"
	"github.com/bsamanvith/agentkit/apps/farm/notify/notifytest"
	notifysqlite "github.com/bsamanvith/agentkit/apps/farm/notify/sqlite"
)

func TestSignInLanesComeFromSettings(t *testing.T) {
	db := filepath.Join(t.TempDir(), "farm.db")
	log := slog.New(slog.DiscardHandler)
	ctx := context.Background()
	if gate, err := signIn(ctx, config{db: db}, nil, nil, log); gate != nil || err != nil {
		t.Errorf("no lanes: %v, %v; want sign-in off", gate, err)
	}
	if gate, err := signIn(ctx, config{db: db, addr: "127.0.0.1:8090", signin: "password, invite", signup: ""}, nil, nil, log); gate == nil || err != nil {
		t.Errorf("password and invite: %v", err)
	}
	t.Setenv("FARM_GOOGLE_CLIENT_ID", "")
	for lanes, want := range map[string]string{
		"google":                   "FARM_GOOGLE_CLIENT_ID",
		"phone":                    "-code-sender",
		"email":                    "FARM_SMTP_URL",
		"password,sms":             `unknown sign-in lane "sms"`,
		"password,invite,password": "listed twice",
	} {
		_, err := signIn(ctx, config{db: db, addr: "127.0.0.1:8090", signin: lanes}, nil, nil, log)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("-signin %s: %v, want an error saying %q", lanes, err, want)
		}
	}
	t.Setenv("FARM_GOOGLE_CLIENT_ID", "id.apps.googleusercontent.com")
	t.Setenv("FARM_GOOGLE_CLIENT_SECRET", "secret")
	if _, err := signIn(ctx, config{db: db, addr: "127.0.0.1:8090", signin: "google,phone", signup: "google,phone", codeSender: "log"}, nil, nil, log); err != nil {
		t.Errorf("google and phone: %v", err)
	}
	if _, err := signIn(ctx, config{db: db, publicURL: "http://farm.example", signin: "google"}, nil, nil, log); err == nil {
		t.Error("google with a plain-http public URL: no error")
	}
}

// TestCodesGoByTheMessageLanes: the phone and email sign-in lanes send
// their codes by the message lanes -code-sender lists, in its order, using
// only lanes that are on and can carry codes (WhatsApp here has no
// authentication template); the code travels beside the message.
func TestCodesGoByTheMessageLanes(t *testing.T) {
	ctx := context.Background()
	store, err := notifysqlite.Open(ctx, filepath.Join(t.TempDir(), "notify.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	fakes := map[string]*notifytest.Fake{
		"whatsapp": notifytest.NewFake("whatsapp", notify.AddrPhone),
		"sms":      notifytest.NewFake("sms", notify.AddrPhone),
		"email":    notifytest.NewFake("email", notify.AddrEmail),
		"telegram": notifytest.NewFake("telegram", notify.AddrTelegram),
	}
	hub := &notify.Hub{Lanes: map[string]notify.LaneSetup{}}
	for n, f := range fakes {
		hub.Lanes[n] = notify.LaneSetup{Info: f.Info(), On: true, Sender: notify.Guard(f, store)}
	}
	m := &messaging{hub: hub, store: store, codes: map[string]bool{"whatsapp": false, "sms": true, "email": true, "telegram": true}}
	names := []string{"whatsapp", "sms", "email", "telegram"}
	if m.codeSender(names, notify.AddrTelegram) == nil || m.codeSender([]string{"email"}, notify.AddrPhone) != nil {
		t.Error("codeSender picked lanes of the wrong kind")
	}
	phone := laneCodes{m.codeSender(names, notify.AddrPhone)}
	if err := phone.SendCode(ctx, "+919876543210", "482913", "482913 is your sign-in code."); err != nil {
		t.Fatal(err)
	}
	if fakes["whatsapp"].Count() != 0 || fakes["sms"].Count() != 1 {
		t.Fatalf("whatsapp %d, sms %d: want the code by SMS only", fakes["whatsapp"].Count(), fakes["sms"].Count())
	}
	if got := fakes["sms"].Sent()[0]; got.Code != "482913" || got.Kind != notify.KindCode || got.Recipient.Phone != "+919876543210" {
		t.Errorf("SMS lane got %+v", got)
	}

	// Through the sign-in lanes: the email lane's code arrives by email.
	db := filepath.Join(t.TempDir(), "farm.db")
	gate, err := signIn(ctx, config{db: db, addr: "127.0.0.1:8090", signin: "phone,email", signup: "phone,email", codeSender: "sms,email"}, nil, m, slog.New(slog.DiscardHandler))
	if err != nil || gate == nil {
		t.Fatalf("phone and email by the lanes: %v", err)
	}
	if _, err := signIn(ctx, config{db: db, addr: "127.0.0.1:8090", signin: "phone", codeSender: "whatsapp"}, nil, m, slog.New(slog.DiscardHandler)); err == nil || !strings.Contains(err.Error(), "FARM_WHATSAPP_CODE_TEMPLATE") {
		t.Errorf("phone by WhatsApp with no code template: %v, want an error naming it", err)
	}
}
