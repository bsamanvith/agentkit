package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
)

func TestSignInLanesComeFromSettings(t *testing.T) {
	db := filepath.Join(t.TempDir(), "farm.db")
	log := slog.New(slog.DiscardHandler)
	ctx := context.Background()
	if gate, err := signIn(ctx, config{db: db}, nil, log); gate != nil || err != nil {
		t.Errorf("no lanes: %v, %v; want sign-in off", gate, err)
	}
	if gate, err := signIn(ctx, config{db: db, addr: "127.0.0.1:8090", signin: "password, invite", signup: ""}, nil, log); gate == nil || err != nil {
		t.Errorf("password and invite: %v", err)
	}
	t.Setenv("FARM_GOOGLE_CLIENT_ID", "")
	for lanes, want := range map[string]string{
		"google":                   "FARM_GOOGLE_CLIENT_ID",
		"phone":                    "-code-sender",
		"password,sms":             `unknown sign-in lane "sms"`,
		"password,invite,password": "listed twice",
	} {
		_, err := signIn(ctx, config{db: db, addr: "127.0.0.1:8090", signin: lanes}, nil, log)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("-signin %s: %v, want an error saying %q", lanes, err, want)
		}
	}
	t.Setenv("FARM_GOOGLE_CLIENT_ID", "id.apps.googleusercontent.com")
	t.Setenv("FARM_GOOGLE_CLIENT_SECRET", "secret")
	if _, err := signIn(ctx, config{db: db, addr: "127.0.0.1:8090", signin: "google,phone", signup: "google,phone", codeSender: "log"}, nil, log); err != nil {
		t.Errorf("google and phone: %v", err)
	}
	if _, err := signIn(ctx, config{db: db, publicURL: "http://farm.example", signin: "google"}, nil, log); err == nil {
		t.Error("google with a plain-http public URL: no error")
	}
}
