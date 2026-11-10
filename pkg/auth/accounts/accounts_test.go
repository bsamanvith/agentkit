package accounts_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/auth/accounts"
)

func TestGoogleSignUpThenSignInIsTheSameAccount(t *testing.T) {
	r := newRig(t)
	phone := r.browser("10.0.0.1")
	back := phone.google(gid("g-1"), map[string]any{"return": "/plots?x=1"})
	if back.Path != "/plots" || back.Query().Get("signin") != "" {
		t.Fatalf("callback sent the browser to %s", back)
	}
	first := phone.me()
	if first == "" {
		t.Fatal("not signed in after the callback")
	}
	res := phone.do(http.MethodGet, "/auth/me", nil)
	if n := res.body["account"].(map[string]any)["name"]; n != "Farmer g-1" {
		t.Errorf("new account's name = %v, want the provider's", n)
	}
	ids := res.body["identities"].([]any)
	if len(ids) != 1 || ids[0].(map[string]any)["lane"] != "google" {
		t.Errorf("identities = %v", ids)
	}

	other := r.browser("10.0.0.2")
	other.google(gid("g-1"), nil)
	if got := other.me(); got != first {
		t.Errorf("second sign-in with the same Google account: account %q, want %q", got, first)
	}
	someone := r.browser("10.0.0.3")
	someone.google(gid("g-2"), nil)
	if got := someone.me(); got == first || got == "" {
		t.Errorf("another Google account signed in as %q", got)
	}
}

func TestALaneWithoutSignUpSignsInOnlyExistingAccounts(t *testing.T) {
	r := newRig(t)
	b := r.browser("10.0.0.1")
	b.google(gid("g-1"), nil)
	acc := b.me()
	// The email lane may not sign up: an unknown address gets no code and
	// the same answer.
	res := b.do(http.MethodPost, "/auth/email/send", map[string]any{"to": "New@Example.com"})
	if res.code != http.StatusAccepted || res.body["to"] != "new@example.com" {
		t.Fatalf("send to an unknown address: %d %v", res.code, res.body)
	}
	if n := len(r.outbox.Messages()); n != 1 {
		// The browser is signed in, so it is a link, and gets its code.
		t.Fatalf("%d messages sent for a signed-in link, want 1", n)
	}
	stranger := r.browser("10.0.0.9")
	stranger.do(http.MethodPost, "/auth/email/send", map[string]any{"to": "other@example.com"})
	if n := len(r.outbox.Messages()); n != 1 {
		t.Errorf("a code was sent to an address no account has, through a lane that cannot sign up")
	}
	// Linked, it signs in.
	code := r.outbox.LastCode(t, "new@example.com")
	if res := b.do(http.MethodPost, "/auth/email/verify", map[string]any{"to": "new@example.com", "code": code, "link": true}); res.code != http.StatusOK {
		t.Fatalf("link email: %d %v", res.code, res.body)
	}
	fresh := r.browser("10.0.0.4")
	fresh.do(http.MethodPost, "/auth/email/send", map[string]any{"to": "new@example.com"})
	code = r.outbox.LastCode(t, "new@example.com")
	if res := fresh.do(http.MethodPost, "/auth/email/verify", map[string]any{"to": "new@example.com", "code": code}); res.code != http.StatusOK {
		t.Fatalf("sign in by email: %d %v", res.code, res.body)
	}
	if got := fresh.me(); got != acc {
		t.Errorf("signed in by the linked email as %q, want %q", got, acc)
	}
	// Google is a sign-up lane, phone too; a redirect lane without sign-up
	// answers no_account at the callback.
	r2 := newRig(t, accounts.WithLanes())
	svc, err := accounts.New(r2.store, accounts.WithLanes(r2.google), accounts.WithClock(r2.clock))
	if err != nil {
		t.Fatal(err)
	}
	r2.h = svc.Handler("/auth")
	back := r2.browser("10.0.0.1").google(gid("g-new"), nil)
	if back.Query().Get("signin") != "failed" || back.Query().Get("reason") != "no_account" {
		t.Errorf("sign-in without sign-up came back to %s", back)
	}
}

func TestLinkedIdentitiesSignInToOneAccount(t *testing.T) {
	r := newRig(t)
	b := r.browser("10.0.0.1")
	b.google(gid("g-1"), nil)
	acc := b.me()

	// Link a phone while signed in.
	b.do(http.MethodPost, "/auth/phone/send", map[string]any{"to": "98765 43210"})
	code := r.outbox.LastCode(t, "+919876543210")
	if res := b.do(http.MethodPost, "/auth/phone/verify", map[string]any{"to": "+91 98765 43210", "code": code, "link": true}); res.code != http.StatusOK {
		t.Fatalf("link phone: %d %v", res.code, res.body)
	}
	// And a password.
	if res := b.do(http.MethodPost, "/auth/password", map[string]any{"username": "Ravi.K", "password": "a long enough password"}); res.code != http.StatusOK {
		t.Fatalf("set password: %d %v", res.code, res.body)
	}
	if res := b.do(http.MethodGet, "/auth/me", nil); len(res.body["identities"].([]any)) != 3 {
		t.Fatalf("identities after linking: %v", res.body["identities"])
	}

	viaPhone := r.browser("10.0.0.2")
	viaPhone.do(http.MethodPost, "/auth/phone/send", map[string]any{"to": "9876543210"})
	code = r.outbox.LastCode(t, "+919876543210")
	viaPhone.do(http.MethodPost, "/auth/phone/verify", map[string]any{"to": "9876543210", "code": code})
	viaPassword := r.browser("10.0.0.3")
	if res := viaPassword.do(http.MethodPost, "/auth/password/check", map[string]any{"id": "ravi.k", "secret": "a long enough password"}); res.code != http.StatusOK {
		t.Fatalf("password sign-in: %d %v", res.code, res.body)
	}
	for name, got := range map[string]string{"phone": viaPhone.me(), "password": viaPassword.me()} {
		if got != acc {
			t.Errorf("signed in by %s as %q, want the account %q", name, got, acc)
		}
	}

	// Someone else's Google account cannot be linked to this one...
	other := r.browser("10.0.0.4")
	other.google(gid("g-2"), nil)
	back := b.google(gid("g-2"), map[string]any{"link": true})
	if back.Query().Get("reason") != "linked_elsewhere" {
		t.Errorf("linking another account's identity came back to %s", back)
	}
	// ... and the phone cannot be linked to theirs.
	other.me()
	other.do(http.MethodPost, "/auth/phone/send", map[string]any{"to": "9876543210"})
	code = r.outbox.LastCode(t, "+919876543210")
	if res := other.do(http.MethodPost, "/auth/phone/verify", map[string]any{"to": "9876543210", "code": code, "link": true}); res.code != http.StatusConflict {
		t.Errorf("linking a phone another account has: %d %v", res.code, res.body)
	}
}

func TestTheLastIdentityStays(t *testing.T) {
	r := newRig(t)
	b := r.browser("10.0.0.1")
	b.google(gid("g-1"), nil)
	b.me()
	res := b.do(http.MethodPost, "/auth/unlink", map[string]any{"lane": "google", "subject": "g-1"})
	if res.code != http.StatusConflict || errCode(res) != "last_identity" {
		t.Fatalf("unlink the only identity: %d %v", res.code, res.body)
	}
	b.do(http.MethodPost, "/auth/password", map[string]any{"username": "ravi", "password": "a long enough password"})
	if res := b.do(http.MethodPost, "/auth/unlink", map[string]any{"lane": "google", "subject": "g-1"}); res.code != http.StatusNoContent {
		t.Fatalf("unlink with another identity left: %d %v", res.code, res.body)
	}
	// Someone else's identity is not found from this account.
	o := r.browser("10.0.0.2")
	o.google(gid("g-2"), nil)
	if res := b.do(http.MethodPost, "/auth/unlink", map[string]any{"lane": "google", "subject": "g-2"}); res.code != http.StatusNotFound {
		t.Errorf("unlink another account's identity: %d", res.code)
	}
}

func TestAGoogleAccountIsLinkedThroughTheRedirect(t *testing.T) {
	r := newRig(t)
	b := r.browser("10.0.0.1")
	b.google(gid("g-1"), nil)
	acc := b.me()
	b.csrf = ""
	if res := b.do(http.MethodPost, "/auth/google/start", map[string]any{"link": true}); res.code != http.StatusForbidden {
		t.Errorf("starting a link without the CSRF token: %d, want 403", res.code)
	}
	b.me()
	if u := b.google(gid("g-2"), map[string]any{"link": true}); u.Query().Get("signin") != "" {
		t.Fatalf("linking a second Google account came back %v", u)
	}
	other := r.browser("10.0.0.2")
	other.google(gid("g-2"), nil)
	if got := other.me(); got != acc {
		t.Errorf("the linked Google account signs in as %q, want %q", got, acc)
	}
}

func TestABurstOfGuessesStaysWithinTheLimit(t *testing.T) {
	r := newRig(t)
	b := r.browser("10.0.0.1")
	b.google(gid("g-1"), nil)
	b.me()
	b.do(http.MethodPost, "/auth/password", map[string]any{"username": "ravi", "password": "a long enough password"})
	var wg sync.WaitGroup
	var mu sync.Mutex
	wrong := 0
	for i := range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := r.browser(fmt.Sprintf("10.1.%d.1", i)).do(http.MethodPost, "/auth/password/check", map[string]any{"id": "ravi", "secret": "guess guess"})
			if res.code == http.StatusUnauthorized {
				mu.Lock()
				wrong++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wrong != 10 {
		t.Errorf("%d of 40 parallel guesses were checked, want exactly 10 (the rest throttled)", wrong)
	}
}

func TestPasswordsFailAlikeAndAreThrottled(t *testing.T) {
	r := newRig(t)
	b := r.browser("10.0.0.1")
	b.google(gid("g-1"), nil)
	b.me()
	b.do(http.MethodPost, "/auth/password", map[string]any{"username": "ravi", "password": "a long enough password"})
	if res := b.do(http.MethodPost, "/auth/password", map[string]any{"username": "ravi", "password": "short"}); res.code != http.StatusBadRequest {
		t.Errorf("a short password: %d", res.code)
	}
	x := r.browser("10.0.0.2")
	wrong := x.do(http.MethodPost, "/auth/password/check", map[string]any{"id": "ravi", "secret": "not it at all"})
	unknown := x.do(http.MethodPost, "/auth/password/check", map[string]any{"id": "nobody", "secret": "not it at all"})
	if wrong.code != http.StatusUnauthorized || unknown.code != wrong.code || unknown.body["error"] != wrong.body["error"] {
		t.Errorf("wrong password %d %v, unknown user %d %v: want the same 401", wrong.code, wrong.body, unknown.code, unknown.body)
	}
	for range 9 { // with the first, ten wrong passwords for ravi
		x.do(http.MethodPost, "/auth/password/check", map[string]any{"id": "ravi", "secret": "guess guess"})
	}
	res := x.do(http.MethodPost, "/auth/password/check", map[string]any{"id": "ravi", "secret": "a long enough password"})
	if res.code != http.StatusTooManyRequests || res.hdr.Get("Retry-After") == "" {
		t.Fatalf("after 10 failures, even the right password: %d %v, want 429 with Retry-After", res.code, res.body)
	}
	// From elsewhere too: the username is held back, not just the address.
	if res := r.browser("10.0.0.3").do(http.MethodPost, "/auth/password/check", map[string]any{"id": "RAVI", "secret": "a long enough password"}); res.code != http.StatusTooManyRequests {
		t.Errorf("another address, same username: %d, want 429", res.code)
	}
	r.clock.Advance(16 * time.Minute)
	if res := x.do(http.MethodPost, "/auth/password/check", map[string]any{"id": "ravi", "secret": "a long enough password"}); res.code != http.StatusOK {
		t.Errorf("after the window: %d %v", res.code, res.body)
	}
}

func TestSignInCodesWorkOnceAndExpire(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	acc, err := r.svc.CreateAccount(ctx, accounts.Account{Name: "Venkatamma", Phone: "+91 98480 12345"})
	if err != nil {
		t.Fatal(err)
	}
	if acc.Phone != "+919848012345" {
		t.Errorf("phone stored as %q", acc.Phone)
	}
	code, err := r.svc.IssueSignInCode(ctx, acc.ID, 7*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 14 || code[4] != '-' || code[9] != '-' {
		t.Errorf("code %q is not XXXX-XXXX-XXXX", code)
	}
	b := r.browser("10.0.0.1")
	typed := strings.ToLower(strings.ReplaceAll(code, "-", " "))
	if res := b.do(http.MethodPost, "/auth/invite/check", map[string]any{"secret": typed}); res.code != http.StatusOK {
		t.Fatalf("sign in with the code as typed (%q): %d %v", typed, res.code, res.body)
	}
	if got := b.me(); got != acc.ID {
		t.Errorf("signed in as %q, want %q", got, acc.ID)
	}
	if res := r.browser("10.0.0.2").do(http.MethodPost, "/auth/invite/check", map[string]any{"secret": code}); res.code != http.StatusUnauthorized {
		t.Errorf("the code a second time: %d, want 401", res.code)
	}
	late, _ := r.svc.IssueSignInCode(ctx, acc.ID, time.Hour)
	r.clock.Advance(time.Hour)
	if res := r.browser("10.0.0.3").do(http.MethodPost, "/auth/invite/check", map[string]any{"secret": late}); res.code != http.StatusUnauthorized {
		t.Errorf("an expired code: %d, want 401", res.code)
	}
	// A code made for another purpose does not sign in.
	join, _ := r.svc.IssueCode(ctx, "join", map[string]string{"account": acc.ID}, time.Hour)
	if res := r.browser("10.0.0.4").do(http.MethodPost, "/auth/invite/check", map[string]any{"secret": join}); res.code != http.StatusUnauthorized {
		t.Errorf("a join code at the sign-in lane: %d, want 401", res.code)
	}
	if _, err := r.svc.RedeemCode(ctx, "join", join); err != nil {
		t.Errorf("the join code was used up by the failed sign-in: %v", err)
	}
}

func TestASignInCodeIsRedeemedOnceUnderARace(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	acc, _ := r.svc.CreateAccount(ctx, accounts.Account{Name: "A"})
	code, _ := r.svc.IssueSignInCode(ctx, acc.ID, time.Hour)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for range 10 {
		wg.Go(func() {
			if _, err := r.svc.RedeemCode(ctx, accounts.PurposeSignIn, code); err == nil {
				mu.Lock()
				ok++
				mu.Unlock()
			} else if !errors.Is(err, accounts.ErrBadSecret) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if ok != 1 {
		t.Errorf("%d redeems succeeded, want 1", ok)
	}
}

func TestOneTimeCodes(t *testing.T) {
	r := newRig(t)
	b := r.browser("10.0.0.1")
	if res := b.do(http.MethodPost, "/auth/phone/send", map[string]any{"to": "12345"}); res.code != http.StatusBadRequest {
		t.Errorf("a bad number: %d", res.code)
	}
	b.do(http.MethodPost, "/auth/phone/send", map[string]any{"to": "9876543210"})
	code := r.outbox.LastCode(t, "+919876543210")
	if msg := r.outbox.Messages()[0].Text; !strings.HasPrefix(msg, code) {
		t.Errorf("the message does not start with the code: %q", msg)
	}
	for range accounts.OneTimeCodeAttempts {
		wrong := "000000"
		if code == wrong {
			wrong = "111111"
		}
		if res := b.do(http.MethodPost, "/auth/phone/verify", map[string]any{"to": "9876543210", "code": wrong}); res.code != http.StatusUnauthorized {
			t.Fatalf("a wrong code: %d", res.code)
		}
	}
	if res := b.do(http.MethodPost, "/auth/phone/verify", map[string]any{"to": "9876543210", "code": code}); res.code != http.StatusUnauthorized {
		t.Errorf("the right code after %d wrong ones: %d, want 401 (used up)", accounts.OneTimeCodeAttempts, res.code)
	}

	r.clock.Advance(16 * time.Minute) // past the guessing and sending windows
	b.do(http.MethodPost, "/auth/phone/send", map[string]any{"to": "9876543210"})
	code = r.outbox.LastCode(t, "+919876543210")
	r.clock.Advance(accounts.OneTimeCodeTTL)
	if res := b.do(http.MethodPost, "/auth/phone/verify", map[string]any{"to": "9876543210", "code": code}); res.code != http.StatusUnauthorized {
		t.Errorf("an expired code: %d", res.code)
	}

	// A new code replaces the last.
	b.do(http.MethodPost, "/auth/phone/send", map[string]any{"to": "9876543210"})
	old := r.outbox.LastCode(t, "+919876543210")
	b.do(http.MethodPost, "/auth/phone/send", map[string]any{"to": "9876543210"})
	code = r.outbox.LastCode(t, "+919876543210")
	if old != code {
		if res := b.do(http.MethodPost, "/auth/phone/verify", map[string]any{"to": "9876543210", "code": old}); res.code != http.StatusUnauthorized {
			t.Errorf("a replaced code: %d", res.code)
		}
	}
	res := b.do(http.MethodPost, "/auth/phone/verify", map[string]any{"to": "9876543210", "code": code})
	if res.code != http.StatusOK || res.body["created"] != true {
		t.Fatalf("the latest code: %d %v", res.code, res.body)
	}
	if res := b.do(http.MethodPost, "/auth/phone/verify", map[string]any{"to": "9876543210", "code": code}); res.code != http.StatusUnauthorized {
		t.Errorf("the same code twice: %d", res.code)
	}

	// Three codes to one number per window, then 429.
	if res := b.do(http.MethodPost, "/auth/phone/send", map[string]any{"to": "9876543210"}); res.code != http.StatusTooManyRequests {
		t.Errorf("a fourth code in the window: %d, want 429", res.code)
	}
	// A failed send leaves no code behind.
	r.outbox.Fail(errors.New("gateway down"))
	if res := b.do(http.MethodPost, "/auth/phone/send", map[string]any{"to": "9000000000"}); res.code != http.StatusInternalServerError {
		t.Errorf("a failed send: %d", res.code)
	}
}

func TestCrossSiteRequestsAreRefused(t *testing.T) {
	r := newRig(t)
	b := r.browser("10.0.0.1")
	b.google(gid("g-1"), nil)
	b.me()
	for _, c := range []struct {
		name, path string
		hdr        []string
	}{
		{"sign-in from another site", "/auth/password/check", []string{"Origin", "https://evil.example"}},
		{"sign-in with no Origin", "/auth/phone/send", []string{"Origin", ""}},
		{"account change without the token", "/auth/password", []string{accounts.CSRFHeader, ""}},
		{"account change with a wrong token", "/auth/signout-all", []string{accounts.CSRFHeader, "nope"}},
	} {
		res := b.do(http.MethodPost, c.path, map[string]any{}, c.hdr...)
		if res.code != http.StatusForbidden || errCode(res) != "csrf" {
			t.Errorf("%s: %d %v, want 403 csrf", c.name, res.code, res.body)
		}
	}
}

func TestSessionsAreLongButEnd(t *testing.T) {
	r := newRig(t, accounts.WithSessionTimeouts(30*24*time.Hour, 60*24*time.Hour))
	b := r.browser("10.0.0.1")
	b.google(gid("g-1"), nil)
	var cookie *http.Cookie
	for _, c := range b.cookies {
		if c.Name == "agentkit_account" {
			cookie = c
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge != 60*24*3600 {
		t.Fatalf("session cookie = %+v", cookie)
	}
	// Used every few weeks, it lasts until the absolute limit.
	for range 2 {
		r.clock.Advance(29 * 24 * time.Hour)
		if b.me() == "" {
			t.Fatal("signed out while still in use")
		}
	}
	r.clock.Advance(3 * 24 * time.Hour)
	if b.me() != "" {
		t.Error("the session outlived its absolute limit")
	}
	b.google(gid("g-1"), nil)
	r.clock.Advance(31 * 24 * time.Hour)
	if b.me() != "" {
		t.Error("the session outlived its idle limit")
	}
}

func TestSignOutAndSignOutEverywhere(t *testing.T) {
	r := newRig(t)
	phone, laptop := r.browser("10.0.0.1"), r.browser("10.0.0.2")
	phone.google(gid("g-1"), nil)
	laptop.google(gid("g-1"), nil)
	phone.me()
	laptop.me()
	if res := phone.do(http.MethodPost, "/auth/signout", nil); res.code != http.StatusNoContent {
		t.Fatalf("sign out: %d", res.code)
	}
	if phone.me() != "" || laptop.me() == "" {
		t.Fatal("sign out ended the wrong sessions")
	}
	phone.google(gid("g-1"), nil)
	phone.me()
	if res := laptop.do(http.MethodPost, "/auth/signout-all", nil); res.code != http.StatusNoContent {
		t.Fatalf("sign out everywhere: %d %v", res.code, res.body)
	}
	if phone.me() != "" || laptop.me() != "" {
		t.Error("a session lived on after sign-out everywhere")
	}
}

func TestRedirectFlowIsBoundToItsBrowserAndUsedOnce(t *testing.T) {
	r := newRig(t)
	victim, attacker := r.browser("10.0.0.1"), r.browser("10.0.0.2")
	res := attacker.do(http.MethodPost, "/auth/google/start", map[string]any{})
	state := r.google.flows[len(r.google.flows)-1].State
	r.google.codes["c"] = gid("attacker")
	// The attacker's callback URL opened in the victim's browser (login
	// CSRF) does not sign the victim in.
	back := victim.callback("/auth/google/callback?state=" + state + "&code=c")
	if back.Query().Get("signin") != "failed" || victim.me() != "" {
		t.Errorf("a callback without its browser's flow cookie: %s, signed in %q", back, victim.me())
	}
	_ = res
	// In its own browser it works once.
	back = attacker.callback("/auth/google/callback?state=" + state + "&code=c")
	if back.Query().Get("signin") != "" || attacker.me() == "" {
		t.Fatalf("own callback: %s", back)
	}
	attacker.cookies["agentkit_account_flow"] = &http.Cookie{Name: "agentkit_account_flow", Value: state, Path: "/auth/google/callback"}
	if back := attacker.callback("/auth/google/callback?state=" + state + "&code=c"); back.Query().Get("reason") != "expired" {
		t.Errorf("a used flow again: %s", back)
	}
	// Flows expire.
	b := r.browser("10.0.0.3")
	b.do(http.MethodPost, "/auth/google/start", map[string]any{})
	st := r.google.flows[len(r.google.flows)-1].State
	r.clock.Advance(accounts.FlowTTL + time.Second)
	if back := b.callback("/auth/google/callback?state=" + st + "&code=c"); back.Query().Get("reason") != "expired" {
		t.Errorf("an expired flow: %s", back)
	}
	// A refusal at the provider comes back as "denied".
	b.do(http.MethodPost, "/auth/google/start", map[string]any{"return": "/x"})
	st = r.google.flows[len(r.google.flows)-1].State
	if back := b.callback("/auth/google/callback?state=" + st + "&error=access_denied"); back.Path != "/x" || back.Query().Get("reason") != "denied" {
		t.Errorf("a denied sign-in: %s", back)
	}
}

func TestReturnAddressesStayOnThisSite(t *testing.T) {
	r := newRig(t)
	for _, ret := range []string{"https://evil.example/", "//evil.example/x", "/\\evil.example", "javascript:alert(1)", "evil"} {
		b := r.browser("10.0.0.1")
		back := b.google(gid("g-1"), map[string]any{"return": ret})
		if back.Host != "" || back.Path != "/" {
			t.Errorf("return %q sent the browser to %s", ret, back)
		}
	}
}

func TestAdminEmailsGetTheAdminRole(t *testing.T) {
	r := newRig(t, accounts.WithAdminEmails("Owner@Gmail.com"))
	b := r.browser("10.0.0.1")
	id := gid("owner")
	b.google(id, nil)
	acc, _ := r.store.Account(context.Background(), b.me())
	if !slices.Contains(acc.Roles, accounts.RoleAdmin) {
		t.Errorf("roles = %v, want admin", acc.Roles)
	}
	unverified := gid("x")
	unverified.Email, unverified.EmailVerified = "owner@gmail.com", false
	c := r.browser("10.0.0.2")
	c.google(unverified, nil)
	acc, _ = r.store.Account(context.Background(), c.me())
	if slices.Contains(acc.Roles, accounts.RoleAdmin) {
		t.Error("an unverified email made an admin")
	}
}

func TestNewRefusesBadLanes(t *testing.T) {
	store := accounts.NewMemoryStore()
	for name, opts := range map[string][]accounts.Option{
		"a name twice":           {accounts.WithLanes(accounts.NewInviteLane(store, nil), accounts.NewInviteLane(store, nil))},
		"sign-up through none":   {accounts.WithSignUp("google")},
		"a bad name":             {accounts.WithLanes(&fakeProvider{name: "Google!"})},
		"timeouts upside down":   {accounts.WithSessionTimeouts(48*time.Hour, time.Hour)},
		"a lane of another kind": {accounts.WithLanes(wrongKind{})},
	} {
		if _, err := accounts.New(store, opts...); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

type wrongKind struct{}

func (wrongKind) Name() string        { return "x" }
func (wrongKind) Kind() accounts.Kind { return accounts.KindCode }

func TestNormalizePhone(t *testing.T) {
	for in, want := range map[string]string{
		"9876543210":       "+919876543210",
		"098765 43210":     "+919876543210",
		"+91 98765-43210":  "+919876543210",
		"0091 9876543210":  "+919876543210",
		"+44 20 7946 0958": "+442079460958",
	} {
		got, err := accounts.NormalizePhone(in, "91")
		if err != nil || got != want {
			t.Errorf("NormalizePhone(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "12345", "+91 12345 67890", "98765x43210", "+91 98765 432101"} {
		if got, err := accounts.NormalizePhone(in, "91"); !errors.Is(err, accounts.ErrInvalid) {
			t.Errorf("NormalizePhone(%q) = %q, %v; want ErrInvalid", in, got, err)
		}
	}
	if _, err := accounts.NormalizePhone("9876543210", ""); err == nil {
		t.Error("a number without a country code, with no default: no error")
	}
}
