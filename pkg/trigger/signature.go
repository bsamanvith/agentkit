package trigger

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// DefaultTolerance is how far from now a webhook delivery's timestamp
// [VerifyStandard] accepts by default.
const DefaultTolerance = 5 * time.Minute

// ErrSignature means a webhook delivery's signature does not verify, or its
// timestamp is too far from now.
var ErrSignature = errors.New("webhook signature does not verify")

// secretPrefix starts the text form of a webhook secret.
const secretPrefix = "whsec_"

// The headers of a Standard Webhooks delivery (https://www.standardwebhooks.com/),
// which inbound webhooks (T072's triggers) are signed with. HTTP headers are
// case-insensitive; these are the spec's spelling.
const (
	StandardHeaderID        = "webhook-id"
	StandardHeaderTimestamp = "webhook-timestamp"
	StandardHeaderSignature = "webhook-signature"
)

// SignStandard returns the webhook-signature header of a Standard Webhooks
// delivery with message ID id, sent at t: "v1," and the base64 of the
// HMAC-SHA256, under secret, of "<id>.<unix seconds>.<body>".
func SignStandard(secret []byte, id string, t time.Time, body []byte) string {
	return "v1," + standardMAC(secret, id, strconv.FormatInt(t.Unix(), 10), body)
}

func standardMAC(secret []byte, id, ts string, body []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(id))
	m.Write([]byte("."))
	m.Write([]byte(ts))
	m.Write([]byte("."))
	m.Write(body)
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// VerifyStandard checks a Standard Webhooks delivery: its webhook-id (id),
// webhook-timestamp (ts, Unix seconds) and webhook-signature (sigs: space
// separated "v1,<base64>" values, of which one must match) headers against
// its body under secret, and that ts is within tolerance of now (0 means
// [DefaultTolerance]). An empty id is refused, as the ID is what a receiver
// tells a replay by. It fails with an error matching [ErrSignature].
func VerifyStandard(secret []byte, id, ts, sigs string, body []byte, now time.Time, tolerance time.Duration) error {
	if tolerance <= 0 {
		tolerance = DefaultTolerance
	}
	if id == "" {
		return fmt.Errorf("%w: no %s", ErrSignature, StandardHeaderID)
	}
	sec, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: %s is not Unix seconds", ErrSignature, StandardHeaderTimestamp)
	}
	if d := now.Sub(time.Unix(sec, 0)); d > tolerance || d < -tolerance {
		return fmt.Errorf("%w: the timestamp is %v away from now", ErrSignature, d.Round(time.Second))
	}
	want := []byte(standardMAC(secret, id, ts, body))
	for s := range strings.FieldsSeq(sigs) {
		if v, ok := strings.CutPrefix(s, "v1,"); ok && hmac.Equal([]byte(v), want) {
			return nil
		}
	}
	return fmt.Errorf("%w: no v1 signature matches the body", ErrSignature)
}

// EncodeStandardSecret is the text form of a secret that Standard Webhooks
// libraries read: "whsec_" and its padded standard base64. (Completion
// webhooks' secrets, package webhook, are in base64url.)
func EncodeStandardSecret(secret []byte) string {
	return secretPrefix + base64.StdEncoding.EncodeToString(secret)
}

// DecodeStandardSecret reads what [EncodeStandardSecret] wrote.
func DecodeStandardSecret(s string) ([]byte, error) {
	b64, ok := strings.CutPrefix(s, secretPrefix)
	if !ok {
		return nil, fmt.Errorf("webhook secret: want %q and base64", secretPrefix)
	}
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(b) == 0 {
		return nil, fmt.Errorf("webhook secret: want %q and base64", secretPrefix)
	}
	return b, nil
}
