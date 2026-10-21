// Package webhook tells webhooks that runs have ended (T071): it signs each
// delivery, so the receiver can check that it came from agentkit and was not
// replayed, and delivers the notifications runs keep in the store
// ([store.Notification]), retrying with backoff for a day.
//
// # Signatures
//
// Every delivery is a POST of JSON with three headers:
//
//	Agentkit-Webhook-Id: <the run's ID>        the same on every attempt: the idempotency key
//	Agentkit-Webhook-Attempt: <1, 2, ...>
//	Agentkit-Signature: t=<unix seconds>,v1=<hex HMAC-SHA256>
//
// v1 is the HMAC-SHA256, under the tenant's secret, of the timestamp, a dot
// and the body: "<t>.<body>", as Stripe signs. [Verify] checks it, and that
// the timestamp is recent. A receiver must treat a second delivery with the
// same webhook ID as the first: an attempt whose answer was lost is sent
// again.
//
// The secret is per tenant. [DeriveSecret] makes one from an operator's
// master secret, so that nothing needs storing; [EncodeSecret] is its text
// form ("whsec_..."), which a tenant's admin reads from the API.
//
// # Delivery
//
// A [Notifier] claims each due notification across tenants
// ([store.Admin.ClaimNotification]), asks the payload function for the
// body, posts it, and records the attempt: a 2xx answer delivers it;
// anything else (another status, a refused connection, a timeout, a redirect)
// is tried again after a growing delay, until a day after the first attempt,
// when it gives up. Attempts are recorded in the store, so they survive a
// restart, and a notification is posted by one notifier at a time (its
// lease). A notifier that dies between posting and recording leaves the
// notification to be posted again once its lease expires: delivery is at
// least once, and the webhook ID is how the receiver keeps it to once.
package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// The headers of a delivery.
const (
	HeaderSignature = "Agentkit-Signature"
	HeaderID        = "Agentkit-Webhook-Id"
	HeaderAttempt   = "Agentkit-Webhook-Attempt"
)

// DefaultTolerance is how old a signature's timestamp [Verify] accepts by
// default, and how far in the future.
const DefaultTolerance = 5 * time.Minute

// ErrSignature means a delivery's signature header is missing, malformed,
// does not match its body under the secret, or is too old.
var ErrSignature = errors.New("webhook signature does not verify")

// Sign returns the signature header of body, sent at t, under secret.
func Sign(secret []byte, t time.Time, body []byte) string {
	ts := strconv.FormatInt(t.Unix(), 10)
	return "t=" + ts + ",v1=" + mac(secret, ts, body)
}

func mac(secret []byte, ts string, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(ts))
	m.Write([]byte("."))
	m.Write(body)
	return hex.EncodeToString(m.Sum(nil))
}

// Verify checks a delivery's signature header against its body under secret:
// one of its v1 values must be the body's HMAC, and its timestamp within
// tolerance of now (0 means [DefaultTolerance]). It fails with an error
// matching [ErrSignature].
func Verify(secret []byte, header string, body []byte, now time.Time, tolerance time.Duration) error {
	if tolerance <= 0 {
		tolerance = DefaultTolerance
	}
	var ts string
	var sigs []string
	for part := range strings.SplitSeq(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		switch {
		case !ok:
		case k == "t":
			ts = v
		case k == "v1":
			sigs = append(sigs, v)
		}
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || len(sigs) == 0 {
		return fmt.Errorf("%w: want t=<unix seconds>,v1=<hex>", ErrSignature)
	}
	if d := now.Sub(time.Unix(sec, 0)); d > tolerance || d < -tolerance {
		return fmt.Errorf("%w: the timestamp is %v away from now", ErrSignature, d.Round(time.Second))
	}
	want := []byte(mac(secret, ts, body))
	for _, s := range sigs {
		if hmac.Equal([]byte(s), want) {
			return nil
		}
	}
	return fmt.Errorf("%w: no v1 signature matches the body", ErrSignature)
}

// DeriveSecret returns the signing secret of tenant from an operator's master
// secret: HMAC-SHA256(master, "agentkit webhook secret\x00" + tenant). The
// same master gives every replica the same secrets, and nothing is stored.
func DeriveSecret(master []byte, tenant string) []byte {
	m := hmac.New(sha256.New, master)
	m.Write([]byte("agentkit webhook secret\x00"))
	m.Write([]byte(tenant))
	return m.Sum(nil)
}

// secretPrefix starts the text form of a secret.
const secretPrefix = "whsec_"

// EncodeSecret is the text form of a secret: "whsec_" and its unpadded
// base64url.
func EncodeSecret(secret []byte) string {
	return secretPrefix + base64.RawURLEncoding.EncodeToString(secret)
}

// DecodeSecret reads what [EncodeSecret] wrote.
func DecodeSecret(s string) ([]byte, error) {
	b64, ok := strings.CutPrefix(s, secretPrefix)
	if !ok {
		return nil, fmt.Errorf("webhook secret: want %q and base64url", secretPrefix)
	}
	b, err := base64.RawURLEncoding.DecodeString(b64)
	if err != nil || len(b) == 0 {
		return nil, fmt.Errorf("webhook secret: want %q and base64url", secretPrefix)
	}
	return b, nil
}
