# Messages: lanes, routes and the outbox

The owner asked (2026-11-10) for WhatsApp, SMS and email lanes beside the
app, "pluggable and switchable", and composite: "a notif can chain and
send to sms + gmail + phone + whatsapp, or just one, depending on what we
configure". Task: [T143](../../../tasks/T143-farm-message-lanes.md); the
roadmap's "Lanes" section has the why.

Every notice still lands in the app's inbox first ([AUTOMATIONS.md](AUTOMATIONS.md)).
The lanes take it from there to the farmer's phone.

## The pieces

| Piece | What |
|---|---|
| `notify.Lane` | One way to reach a person: `Info()` (name, address kind, paid) and `Deliver`. It only sends. |
| `notify.Guard` | Makes a lane a `Sender`: the person has an address on it and agreed to it (opt-in recorded per lane, STOP honoured), the message was not sent on it before (one delivery row per message and lane), the lane is under its daily and monthly cap, and what happened is recorded. |
| `FanOut`, `Chain`, `Router` | Composites, also `Sender`s, so they nest. Fan-out sends on all; chain tries the next only when one is skipped or fails; router picks by kind and severity. |
| `notify.Parse` | The tree from configuration: `urgent=fanout(push,telegram,chain(whatsapp,sms)); warning=fanout(push,telegram); default=push`. |
| `notify.Dispatcher` | The outbox: queue a message once (its ID is the idempotency key), hold it through quiet hours unless urgent, send through the tree, retry with backoff while lanes fail for a while (30 s doubling to an hour, 6 tries), drop it when it expires (urgent 12 h, warning 24 h, advice 72 h). |
| `notify.Inbound` | What lanes report back: delivery and read status, STOP and START, a Telegram chat linked with a one-time code. |
| `notify/sqlite` | Recipients, the outbox and deliveries in the app's SQLite file; passes `notifytest.RunStore`. |
| `watch.Messages` | The automations' channel: each new notice becomes one message per person who hears about the plot. |
| `farmapi` `/api/notify/*` | The settings page's routes: lanes and spend, people, consent, push subscriptions, Telegram links, test messages, the outbox, email unsubscribe. |

## The lanes

| Lane | Package | Cost | One-time setup | Switch on with |
|---|---|---|---|---|
| Inbox | `automation` | Free | None | always |
| Web push | `notify/webpush` | Free | None (keys made at first start in `<db>-push-keys.json`) | always on |
| Telegram | `notify/telegram` | Free | A bot from @BotFather | `FARM_TELEGRAM_TOKEN`, `FARM_TELEGRAM_BOT`; `FARM_TELEGRAM_SECRET` for the webhook, else it polls |
| Email | `notify/email` | Free tiers (a few hundred a day) on most providers; flag the one chosen | A sending domain | `FARM_SMTP_URL` (`smtp://user:pass@host:587`), `FARM_EMAIL_FROM` |
| WhatsApp | `notify/whatsapp` (Meta Cloud API, direct) | **Paid** per template message (about ₹0.12 to ₹0.15 in India); replies within 24 hours of the farmer's message are free | Meta business verification; the `farm_alert` template (two body parameters: title, text) approved per language | `FARM_WHATSAPP_TOKEN`, `FARM_WHATSAPP_PHONE_ID`, `FARM_WHATSAPP_LANGS`, `FARM_WHATSAPP_APP_SECRET`, `FARM_WHATSAPP_VERIFY_TOKEN` |
| SMS | `notify/sms` (MSG91 or Twilio) | **Paid** per SMS (about ₹0.15 to ₹0.30) | DLT registration with TRAI: the business, the sender ID, each template (variables of 30 characters) | `FARM_MSG91_KEY`, `FARM_MSG91_TEMPLATE`; or `FARM_TWILIO_SID`, `FARM_TWILIO_TOKEN`, `FARM_TWILIO_FROM` |
| Log | `notify` | Free | None | always (for trying a tree) |

A lane without its keys is in the tree as `Off`: its messages skip it and
a chain moves on, so adding keys later switches it on with no other change.
`GET /api/notify` lists each lane, whether it is on and why not.

A new lane is a type with `Info` and `Deliver`, its test running
`notifytest.RunLane` against a fake of the provider, and a case in
`buildNotify` (`cmd/farm/notify.go`). Nothing else learns its name.

## Rules every lane follows

- **Consent.** A lane with an address sends only after the person opted
  in on it (the settings page records when and where). Push is opted in by
  subscribing. STOP (and Telugu ఆపు, Hindi बंद) on WhatsApp, SMS or
  Telegram, an email unsubscribe link, a blocked bot or an unsubscribed
  number opt the person out of that lane at once. A new phone or email drops
  that lane's consent.
- **Quiet hours.** 9 pm to 6 am in the person's time (India's by default)
  unless urgent; the message waits in the outbox until the morning.
- **Idempotency.** A message's ID is the notice's ID and the person's, so
  queueing it again does nothing; each lane sends it at most once, and a
  retry after one lane of a chain got through does not try the earlier
  lanes again. An unknown outcome (a crash mid-send) is not resent.
- **Retries.** Only for failures that may pass (the network, 5xx, rate
  limits); a rejection (bad number, unknown template) is final.
- **Delivery status.** WhatsApp and Twilio report delivered, read and
  failed by webhook (`/hooks/whatsapp`, `/hooks/twilio`, signed); a later
  status never goes back (a late "delivered" after "read").
- **Cost caps.** `-notify-caps` sets each paid lane's cost per message (in
  paise) and caps a day and a month, in counts or paise; default
  `whatsapp:cost=14,day=300,month=5000; sms:cost=25,day=200,month=3000`.
  At the cap the lane is skipped, so `chain(whatsapp, push)` falls back to
  the free lane, and the log says so once a day.

## Configuration

| Flag | Env | Default |
|---|---|---|
| `-notify` | `FARM_NOTIFY` | `true` (with the automations) |
| `-notify-routes` | `FARM_NOTIFY_ROUTES` | `urgent=fanout(push,telegram,chain(whatsapp,sms)); warning=fanout(push,telegram); digest=fanout(push,email); default=push` |
| `-notify-caps` | `FARM_NOTIFY_CAPS` | as above |
| `-public-url` | `FARM_PUBLIC_URL` | none: no links in messages, no unsubscribe links, no webhooks (Telegram polls) |

Route keys are a severity (`urgent`, `warning`, `advice`), a kind
(`alert`, `digest`, `reply`, `test`), both (`alert.urgent`), or
`default`; the most specific wins.

## Not yet

- The settings page in the web app (people, lanes, consent, push
  subscription, Telegram link) and the service worker that shows pushes.
- Escalating a chain when a lane took a message but did not deliver it
  in time (now only on failure).
- Recipients tied to farmer accounts once sign-in exists (T144): today
  they are people added in settings, each hearing about some or all plots.
- MSG91 delivery reports, inbound SMS on MSG91, WhatsApp replies answered
  by the assistant.
