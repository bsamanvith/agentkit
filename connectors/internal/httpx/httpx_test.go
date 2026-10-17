package httpx_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/connectors/internal/httpx"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

func TestReadBody(t *testing.T) {
	for _, tc := range []struct {
		body string
		want error
	}{
		{"", nil},
		{"12345", nil},
		{"123456", httpx.ErrTooLarge},
	} {
		got, err := httpx.ReadBody(strings.NewReader(tc.body), 5)
		if !errors.Is(err, tc.want) {
			t.Errorf("ReadBody(%q) error = %v, want %v", tc.body, err, tc.want)
		}
		if err == nil && string(got) != tc.body {
			t.Errorf("ReadBody(%q) = %q", tc.body, got)
		}
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		value  string
		want   time.Duration
		wantOK bool
	}{
		{"", httpx.DefaultRetryWait, true},
		{"garbage", httpx.DefaultRetryWait, true},
		{"-3", httpx.DefaultRetryWait, true},
		{"0", 0, true},
		{" 2 ", 2 * time.Second, true},
		{"10", 10 * time.Second, true},
		{"11", 0, false},
		{"99999999999999999999", httpx.DefaultRetryWait, true}, // not an int64
		{now.Add(5 * time.Second).Format(http.TimeFormat), 5 * time.Second, true},
		{now.Add(-time.Minute).Format(http.TimeFormat), 0, true},
		{now.Add(time.Minute).Format(http.TimeFormat), 0, false},
	} {
		got, ok := httpx.RetryAfter(t.Context(), tc.value, now)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("RetryAfter(%q) = %v, %v; want %v, %v", tc.value, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestRetryAfterDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if _, ok := httpx.RetryAfter(ctx, "5", time.Now()); ok {
		t.Error("RetryAfter retries a wait that outlasts the context's deadline")
	}
	if d, ok := httpx.RetryAfter(ctx, "0", time.Now()); !ok || d != 0 {
		t.Errorf("RetryAfter(0) = %v, %v; want 0, true", d, ok)
	}
}

func TestSleep(t *testing.T) {
	clk := clocktest.NewFake(time.Time{})
	done := make(chan error, 1)
	go func() { done <- httpx.Sleep(t.Context(), clk, time.Second) }()
	if err := clk.WaitForTimers(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	clk.Advance(time.Second)
	if err := <-done; err != nil {
		t.Errorf("Sleep = %v, want nil", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := httpx.Sleep(ctx, clk, time.Hour); !errors.Is(err, context.Canceled) {
		t.Errorf("Sleep after cancel = %v, want context.Canceled", err)
	}
}

func TestRedact(t *testing.T) {
	if got := httpx.Redact("key k1 and k2", "k1", "", "k2"); got != "key [redacted] and [redacted]" {
		t.Errorf("Redact = %q", got)
	}
	if got := httpx.Redact("nothing", ""); got != "nothing" {
		t.Errorf("Redact with no secret = %q", got)
	}
}
