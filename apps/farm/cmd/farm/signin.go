package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"strings"

	"github.com/bsamanvith/agentkit/apps/farm/farm"
	"github.com/bsamanvith/agentkit/apps/farm/farmauth"
	"github.com/bsamanvith/agentkit/apps/farm/members"
	memsqlite "github.com/bsamanvith/agentkit/apps/farm/members/sqlite"
	"github.com/bsamanvith/agentkit/pkg/auth/accounts"
	"github.com/bsamanvith/agentkit/pkg/auth/accounts/oidc"
	accsqlite "github.com/bsamanvith/agentkit/pkg/auth/accounts/sqlite"
)

// signIn builds the sign-in lanes c lists, the accounts and the members,
// over the -db file; nil when sign-in is off. Which lanes there are is
// configuration: each is one implementation of accounts.Lane, picked here
// by name.
func signIn(ctx context.Context, c config, svc *farm.Service, log *slog.Logger) (*farmauth.Auth, error) {
	names := list(c.signin)
	if len(names) == 0 {
		return nil, nil
	}
	store, err := accsqlite.Open(ctx, c.db)
	if err != nil {
		return nil, err
	}
	public := strings.TrimSuffix(c.publicURL, "/")
	if public == "" {
		public = "http://" + c.addr
	}
	var sender accounts.Sender
	if c.codeSender == "log" {
		sender = accounts.SenderFunc(func(ctx context.Context, to, text string) error {
			log.WarnContext(ctx, "farm: one-time code (code sender is the log: only for trying it out)", "to", to, "text", text)
			return nil
		})
	}
	var lanes []accounts.Lane
	for _, name := range names {
		var l accounts.Lane
		switch name {
		case "google":
			id, secret := os.Getenv("FARM_GOOGLE_CLIENT_ID"), os.Getenv("FARM_GOOGLE_CLIENT_SECRET")
			if id == "" || secret == "" {
				return nil, errors.New("farm: the google sign-in lane needs FARM_GOOGLE_CLIENT_ID and FARM_GOOGLE_CLIENT_SECRET (a web client in the Google Cloud console)")
			}
			l, err = oidc.New(oidc.Google(id, secret, public+farmauth.Prefix+"/google/callback"))
		case accounts.PasswordLaneName:
			l, err = accounts.NewPasswordLane(store)
		case accounts.InviteLaneName:
			l = accounts.NewInviteLane(store, nil)
		case "phone", "email":
			if sender == nil {
				return nil, fmt.Errorf("farm: the %s sign-in lane needs a way to send codes: -code-sender log (for trying it out) until message lanes come", name)
			}
			if name == "phone" {
				l = accounts.NewPhoneLane(name, "91", sender, store)
			} else {
				l = accounts.NewEmailLane(name, sender, store)
			}
		default:
			return nil, fmt.Errorf("farm: unknown sign-in lane %q (google, password, invite, phone, email)", name)
		}
		if err != nil {
			return nil, err
		}
		lanes = append(lanes, l)
	}
	// Behind a TLS-terminating proxy (an ingress, a tunnel), the proxies'
	// addresses: their X-Forwarded-For and X-Forwarded-Proto are trusted, so
	// cookies are secure, the same-origin check sees https, and attempts
	// are counted per farmer, not per proxy.
	var proxies []netip.Prefix
	for _, p := range list(os.Getenv("FARM_TRUSTED_PROXIES")) {
		pre, err := netip.ParsePrefix(p)
		if err != nil {
			addr, aerr := netip.ParseAddr(p)
			if aerr != nil {
				return nil, fmt.Errorf("farm: FARM_TRUSTED_PROXIES: %q is not an address or a prefix", p)
			}
			pre = netip.PrefixFrom(addr, addr.BitLen())
		}
		proxies = append(proxies, pre)
	}
	acc, err := accounts.New(store, accounts.WithTrustedProxies(proxies...),
		accounts.WithLanes(lanes...), accounts.WithSignUp(list(c.signup)...),
		accounts.WithAdminEmails(list(os.Getenv("FARM_ADMIN_EMAILS"))...),
		accounts.WithRoleEmails(members.AccountRolePartner, list(os.Getenv("FARM_PARTNER_EMAILS"))...),
		accounts.WithCookieName("farm_session"), accounts.WithLogger(log))
	if err != nil {
		return nil, err
	}
	mstore, err := memsqlite.Open(ctx, c.db)
	if err != nil {
		return nil, err
	}
	return farmauth.New(acc, members.New(mstore, acc, svc, members.WithLogger(log)), log), nil
}

// list splits a comma-separated setting, dropping blanks.
func list(s string) []string {
	var out []string
	for v := range strings.SplitSeq(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func orOff(s string) string {
	if strings.TrimSpace(s) == "" {
		return "off"
	}
	return s
}
