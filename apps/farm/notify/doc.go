// Package notify sends the farm app's messages to people over lanes: the
// in-app inbox, web push, email, WhatsApp, SMS, Telegram. Every lane is an
// implementation of one interface, [Lane], and is used through [Guard],
// which makes it a [Sender]: it checks the person has an address on the
// lane and agreed to hear on it, never sends one message twice on one lane,
// keeps a lane under its daily and monthly cost cap, and records each
// delivery and its status.
//
// The composites are Senders too, so they nest:
//
//   - [FanOut] sends to every one of its senders at once.
//   - [Chain] tries its senders in order and stops at the first that sends:
//     the next one is the fallback when one fails, is off, has no address
//     for the person, or is over its cap.
//   - [Router] picks a sender by the message's kind and severity.
//
// The tree is configuration ([Parse]), for example
//
//	urgent=fanout(push,telegram,chain(whatsapp,sms)); warning=fanout(push,telegram); default=push
//
// so switching lanes is a setting, not a code change.
//
// The [Dispatcher] keeps an outbox in the [Store]: a message is queued
// once (its ID is its idempotency key), held through the person's quiet
// hours unless it is urgent, sent through the tree, tried again with
// backoff while a lane fails for a while, and dropped when it expires.
// Lanes report delivery and read status, and STOP, through [Inbound].
//
// Lanes live in subpackages (email, whatsapp, sms, telegram, webpush); the
// farm binary picks them in one place. The conformance suites for lanes and
// stores are in notifytest. See apps/farm/docs/MESSAGES.md.
package notify
