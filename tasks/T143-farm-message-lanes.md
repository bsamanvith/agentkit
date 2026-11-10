---
id: T143
title: The farm app's message lanes: one Sender interface, lanes for web push, Telegram, email, WhatsApp and SMS, composites configured as a tree, consent, quiet hours, idempotency, retries, delivery status and cost caps
epic: E25
status: review
depends_on: [T139]
---

# T143: message lanes

## Context

The owner (2026-11-10): "Add whatsapp lane, sms lane and email lane ...
keep it pluggable and switchable. Ensure that it's properly handled", and
"Lanes are just streams / implementations ... we can have it composite and
chained too, like a notif can chain and send to sms + gmail + phone +
whatsapp etc., or just one, depending on impl we pick or configure." The
roadmap's "Lanes" section: grow the automation engine's `Channel` into
lanes with a router; consent and STOP, quiet hours, idempotency, retries,
delivery status, per-lane cost caps. Free first; flag paid. WhatsApp's Meta
verification and SMS's DLT registration are the owner's one-time setups,
pending, so those lanes stay off until their keys exist. Design:
[MESSAGES.md](../apps/farm/docs/MESSAGES.md).

## Scope

- `apps/farm/notify`: `Lane`, `Sender`, `Guard` (address, consent,
  idempotency, caps, records, STOP on a lane's "gone"), `FanOut`, `Chain`,
  `Router`, `Parse` (the tree from configuration), `Dispatcher` (outbox,
  quiet hours, retries, expiry), `Inbound` (status, STOP, START, chat
  link), `Hub` (what the API serves), unsubscribe links; the `Store`
  interface with `notifytest.RunStore` and `notify/sqlite`; `notifytest.RunLane`
  and a fake lane.
- Lanes: `webpush` (VAPID, RFC 8291 encryption), `telegram` (bot, webhook
  or polling, start-link), `email` (SMTP), `whatsapp` (Cloud API,
  templates, signed webhook), `sms` (MSG91 DLT flow, Twilio with signed
  callbacks); the log lane.
- `apps/farm/watch.Messages`: notices to messages; `farmapi`
  `/api/notify/*`; `cmd/farm`: `buildNotify`, `-notify`, `-notify-routes`,
  `-notify-caps`, `-public-url`, `/hooks/*`.

## Out of scope

- The web settings page and the service worker (a follow-up PR in this
  task).
- Recipients as farmer accounts (T144 sign-in); escalation when a lane took
  a message but did not deliver it in time; MSG91 delivery reports.

## Acceptance criteria

- [x] A lane sends only to a person with an address on it who opted in (push by subscribing); a STOP stops even push — test: `notify_test.TestGuardSendsOnlyWithAddressAndConsent`
- [x] A message is sent at most once per lane; a temporary failure is tried again, a final one is not — test: `notify_test.TestGuardSendsAMessageOncePerLane`
- [x] A lane saying the person left records their STOP — test: `notify_test.TestGoneIsAStop`
- [x] A paid lane stops at its daily count and monthly cost caps, the chain falls back, the admin is told once — test: `notify_test.TestCostCaps`
- [x] Chain falls through skipped and failed lanes, stops at the first that sends, never sends on an earlier lane after a later one got through; fan-out sends on all and asks for a retry when one failed for a while — test: `notify_test.TestChain`, `TestFanOut`
- [x] Routes parse from configuration, the most specific wins, bad trees are refused — test: `notify_test.TestRouterAndParse`
- [x] The outbox holds advice through quiet hours and sends urgent at once; retries with doubling waits without sending twice; gives up, expires, and skips removed people — test: `notify_test.TestDispatcherQuietHours`, `TestDispatcherRetriesWithoutSendingTwice`, `TestDispatcherGivesUpAndExpires`, `TestDispatcherNothingToSend`, `TestQuietUntil`
- [x] STOP and START, Telegram links with one-time codes, status that only moves forward — test: `notify_test.TestInbound`, `TestIsStop`
- [x] The store passes the conformance suite on SQLite (atomic updates, one claim per item and per delivery across workers) — test: `notify/sqlite.TestStore`
- [x] Every lane passes `notifytest.RunLane` against a fake of its provider (sends to the address, refuses no address, temporary, rejected and gone failures, a cancelled context) — test: `email.TestLane`, `whatsapp.TestLane`, `sms.TestMSG91`, `sms.TestTwilio`, `telegram.TestLane`, `webpush.TestLane`
- [x] Web push encrypts as RFC 8291's example, byte for byte, and signs VAPID tokens that verify — test: `webpush.TestRFC8291Example`, `TestKeysPersistAndSign`, `TestGoneSubscriptions`
- [x] WhatsApp sends the template in the person's language with clean parameters, replies as text; its webhook checks the subscription token and the signature and reports status and STOP — test: `whatsapp.TestTemplateAndReply`, `TestWebhook`
- [x] SMS: MSG91 DLT variables at 30 characters; Twilio's request and signed callbacks — test: `sms.TestMSG91SendsDLTVariables`, `TestTwilioRequest`, `TestTwilioWebhook`
- [x] Telegram links a chat by its start code, /stop and /start, by webhook or polling — test: `telegram.TestWebhookLinksAndStops`, `TestPoll`
- [x] Email in Telugu with a one-click unsubscribe header — test: `email.TestTeluguAndUnsubscribe`
- [x] Notices become one message per person who hears about the plot, once — test: `watch_test.TestNoticesBecomeMessages`
- [x] The API: lanes and spend, people, consent, push subscriptions, Telegram links, tests by a lane or the tree, the outbox, unsubscribe (a page on GET, only POST unsubscribes, forged tokens refused); 503 when not configured — test: `farmapi_test.TestNotifyRoutes`, `TestNotifyNotConfigured`
- [x] The farm binary starts with every paid lane off and says why, sends a test through the log lane — manual run in a cloud session (PR description)
- [ ] The web settings page and the service worker — follow-up PR
- [ ] Live sends on Telegram, email, WhatsApp and SMS — needs a bot token, an SMTP account, Meta verification and DLT registration
