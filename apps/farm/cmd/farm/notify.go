package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/bsamanvith/agentkit/apps/farm/notify"
	"github.com/bsamanvith/agentkit/apps/farm/notify/email"
	"github.com/bsamanvith/agentkit/apps/farm/notify/sms"
	notifysqlite "github.com/bsamanvith/agentkit/apps/farm/notify/sqlite"
	"github.com/bsamanvith/agentkit/apps/farm/notify/telegram"
	"github.com/bsamanvith/agentkit/apps/farm/notify/webpush"
	"github.com/bsamanvith/agentkit/apps/farm/notify/whatsapp"
	"github.com/bsamanvith/agentkit/pkg/netguard"
)

// DefaultCaps bound the paid lanes until the owner sets their own: costs
// in paise, counts a day and a month.
const DefaultCaps = "whatsapp:cost=14,day=300,month=5000; sms:cost=25,day=200,month=3000"

// messaging is what buildNotify made: the hub the API serves, the hooks
// the lanes' providers call, and what runs in the background.
type messaging struct {
	hub   *notify.Hub
	store *notifysqlite.Store
	hooks map[string]http.Handler
	run   []func(ctx context.Context)
}

// buildNotify is the one place the message lanes are picked: each lane is
// on when its keys are in the environment, and the tree (-notify-routes)
// names lanes whether they are on or not, so adding keys switches a lane
// on with no other change. Secrets come only from the environment.
func buildNotify(ctx context.Context, c config, log *slog.Logger) (*messaging, error) {
	store, err := notifysqlite.Open(ctx, c.db)
	if err != nil {
		return nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = store.Close()
		}
	}()
	caps, err := parseCaps(c.notifyCaps)
	if err != nil {
		return nil, err
	}
	public := strings.TrimRight(c.publicURL, "/")
	m := &messaging{store: store, hooks: map[string]http.Handler{}}
	hub := &notify.Hub{Lanes: map[string]notify.LaneSetup{}, Routes: c.notifyRoutes}
	inbound := notify.NewInbound(store, nil, log)
	secret, err := notify.LoadOrCreateSecret(sideFile(c.db, "-notify-secret"))
	if err != nil {
		return nil, err
	}
	if public != "" {
		hub.Unsubscribe = notify.NewUnsubscriber(secret, public, store)
	}

	add := func(l notify.Lane, why string, err error) error {
		if err != nil {
			return err
		}
		info := l.Info()
		lc := caps[info.Name]
		setup := notify.LaneSetup{Info: info, On: why == "", Why: why, Cost: lc.cost, Cap: lc.Cap}
		if setup.On {
			setup.Sender = notify.Guard(l, store, notify.WithCost(lc.cost), notify.WithCap(lc.Cap), notify.WithGuardLogger(log),
				notify.WithOnCap(func(lane, why string) {
					log.Error("farm: a paid message lane reached its cap; messages fall back to the next lane in their chain", "lane", lane, "why", why)
				}))
		} else {
			setup.Sender = notify.Off(info.Name)
		}
		hub.Lanes[info.Name] = setup
		return nil
	}
	off := func(info notify.LaneInfo, why string) {
		lc := caps[info.Name]
		hub.Lanes[info.Name] = notify.LaneSetup{Info: info, Why: why, Cost: lc.cost, Cap: lc.Cap, Sender: notify.Off(info.Name)}
	}

	// The log lane: always on, for trying the tree.
	if err := add(notify.Log(log), "", nil); err != nil {
		return nil, err
	}

	// Web push: free, keys made on first start.
	keys, err := webpush.LoadOrCreateKeys(sideFile(c.db, "-push-keys.json"))
	if err != nil {
		return nil, err
	}
	guard, err := netguard.New(nil)
	if err != nil {
		return nil, err
	}
	push, err := webpush.New(webpush.Config{
		Keys: keys, Subject: env("FARM_PUSH_SUBJECT", "https://github.com/bsamanvith/agentkit"), Client: guard.Client(nil), Log: log,
		Drop: func(ctx context.Context, r, endpoint string) {
			_, _ = store.Update(ctx, r, func(rc *notify.Recipient) error {
				rc.Push = slices.DeleteFunc(rc.Push, func(p notify.PushSubscription) bool { return p.Endpoint == endpoint })
				return nil
			})
		},
	})
	if err := add(push, "", err); err != nil {
		return nil, err
	}
	hub.PushKey = keys.Public()

	// Email: any SMTP server.
	if u := os.Getenv("FARM_SMTP_URL"); u != "" {
		l, err := email.New(email.Config{URL: u, From: env("FARM_EMAIL_FROM", ""), Unsubscribe: func(msg notify.Message) string {
			if hub.Unsubscribe == nil {
				return ""
			}
			return hub.Unsubscribe.Link(msg, "email")
		}})
		if err := add(l, "", err); err != nil {
			return nil, err
		}
	} else {
		off(notify.LaneInfo{Name: "email", Address: notify.AddrEmail, Note: "Email over SMTP."}, "set FARM_SMTP_URL and FARM_EMAIL_FROM")
	}

	// Telegram: free.
	if tok := os.Getenv("FARM_TELEGRAM_TOKEN"); tok != "" {
		l, err := telegram.New(telegram.Config{Token: tok, Secret: os.Getenv("FARM_TELEGRAM_SECRET"), Log: log})
		if err := add(l, "", err); err != nil {
			return nil, err
		}
		hub.TelegramBot = os.Getenv("FARM_TELEGRAM_BOT")
		if os.Getenv("FARM_TELEGRAM_SECRET") != "" && public != "" {
			m.hooks["/hooks/telegram"] = l.Webhook(inbound)
		} else {
			m.run = append(m.run, func(ctx context.Context) { l.Poll(ctx, inbound) })
		}
	} else {
		off(notify.LaneInfo{Name: "telegram", Address: notify.AddrTelegram, Note: "A Telegram bot. Free."}, "set FARM_TELEGRAM_TOKEN and FARM_TELEGRAM_BOT")
	}

	// WhatsApp: paid, needs Meta business verification.
	if tok := os.Getenv("FARM_WHATSAPP_TOKEN"); tok != "" {
		var langs []string
		for l := range strings.SplitSeq(env("FARM_WHATSAPP_LANGS", "en"), ",") {
			if l = strings.TrimSpace(l); l != "" {
				langs = append(langs, l)
			}
		}
		l, err := whatsapp.New(whatsapp.Config{
			Token: tok, PhoneNumberID: os.Getenv("FARM_WHATSAPP_PHONE_ID"), Template: os.Getenv("FARM_WHATSAPP_TEMPLATE"), Languages: langs,
			AppSecret: os.Getenv("FARM_WHATSAPP_APP_SECRET"), VerifyToken: os.Getenv("FARM_WHATSAPP_VERIFY_TOKEN"), Log: log,
		})
		if err := add(l, "", err); err != nil {
			return nil, err
		}
		m.hooks["/hooks/whatsapp"] = l.Webhook(inbound)
	} else {
		off(notify.LaneInfo{Name: "whatsapp", Address: notify.AddrPhone, Paid: true, Note: "WhatsApp Cloud API. Paid per template message."},
			"needs Meta business verification, then set FARM_WHATSAPP_TOKEN and FARM_WHATSAPP_PHONE_ID")
	}

	// SMS: paid, needs DLT registration in India.
	switch provider := env("FARM_SMS_PROVIDER", ""); {
	case provider == "msg91" || (provider == "" && os.Getenv("FARM_MSG91_KEY") != ""):
		l, err := sms.MSG91(sms.MSG91Config{AuthKey: os.Getenv("FARM_MSG91_KEY"), TemplateID: os.Getenv("FARM_MSG91_TEMPLATE")})
		if err := add(l, "", err); err != nil {
			return nil, err
		}
	case provider == "twilio" || (provider == "" && os.Getenv("FARM_TWILIO_SID") != ""):
		cb := ""
		if public != "" {
			cb = public + "/hooks/twilio"
		}
		l, err := sms.NewTwilio(sms.TwilioConfig{
			AccountSID: os.Getenv("FARM_TWILIO_SID"), AuthToken: os.Getenv("FARM_TWILIO_TOKEN"),
			From: os.Getenv("FARM_TWILIO_FROM"), MessagingService: os.Getenv("FARM_TWILIO_SERVICE"), CallbackURL: cb, Log: log,
		})
		if err := add(l, "", err); err != nil {
			return nil, err
		}
		if cb != "" {
			m.hooks["/hooks/twilio"] = l.Webhook(inbound)
		}
	case provider == "":
		off(notify.LaneInfo{Name: "sms", Address: notify.AddrPhone, Paid: true, Note: "SMS through MSG91 or Twilio. Paid per SMS."},
			"needs DLT registration with TRAI, then set FARM_MSG91_KEY and FARM_MSG91_TEMPLATE (or FARM_TWILIO_*)")
	default:
		return nil, fmt.Errorf("FARM_SMS_PROVIDER %q: want msg91 or twilio", provider)
	}

	senders := make(map[string]notify.Sender, len(hub.Lanes))
	for n, l := range hub.Lanes {
		senders[n] = l.Sender
	}
	root, err := notify.Parse(c.notifyRoutes, senders)
	if err != nil {
		return nil, fmt.Errorf("-notify-routes: %w", err)
	}
	d, err := notify.NewDispatcher(store, root, notify.WithLogger(log))
	if err != nil {
		return nil, err
	}
	hub.Dispatcher = d
	m.hub = hub
	m.run = append(m.run, func(ctx context.Context) {
		if err := d.Run(ctx); err != nil {
			log.Error("farm: messages", "err", err)
		}
	})
	var on []string
	for _, n := range root.Lanes() {
		if hub.Lanes[n].On {
			on = append(on, n)
		}
	}
	log.Info("farm: message lanes", "routes", root.Name(), "on", strings.Join(on, ","))
	ok = true
	return m, nil
}

type laneCap struct {
	notify.Cap
	cost int64
}

// parseCaps reads "lane:cost=14,day=300,month=5000,day_cost=5000,month_cost=60000; ..."
// (costs in paise).
func parseCaps(s string) (map[string]laneCap, error) {
	out := map[string]laneCap{}
	for part := range strings.SplitSeq(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lane, kvs, found := strings.Cut(part, ":")
		if !found {
			return nil, fmt.Errorf("-notify-caps %q: want lane:key=value,...", part)
		}
		var c laneCap
		for kv := range strings.SplitSeq(kvs, ",") {
			k, v, _ := strings.Cut(strings.TrimSpace(kv), "=")
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				return nil, fmt.Errorf("-notify-caps %s: %s=%q is not a whole number", lane, k, v)
			}
			switch k {
			case "cost":
				c.cost = n
			case "day":
				c.Day = int(n)
			case "month":
				c.Month = int(n)
			case "day_cost":
				c.DayCost = n
			case "month_cost":
				c.MonthCost = n
			default:
				return nil, fmt.Errorf("-notify-caps %s: %q: want cost, day, month, day_cost or month_cost", lane, k)
			}
		}
		out[strings.TrimSpace(lane)] = c
	}
	return out, nil
}

// sideFile is a file beside the database: farm.db -> farm-notify-secret.
func sideFile(db, suffix string) string {
	return strings.TrimSuffix(db, filepath.Ext(db)) + suffix
}
