package webhook_test

import (
	"fmt"
	"time"

	"github.com/bsamanvith/agentkit/pkg/webhook"
)

// A receiver checks a delivery with its tenant's secret, which it reads as
// text from the API (GET /v1/webhooks/secret).
func Example() {
	secret := webhook.DeriveSecret([]byte("the operator's master secret, 32+ bytes"), "acme")
	text := webhook.EncodeSecret(secret) // what the tenant's admin is shown

	// The sender signs the body it posts.
	body := []byte(`{"v":1,"type":"run.ended","run":{"id":"run_1","status":"done"}}`)
	sent := time.Unix(1_800_000_000, 0)
	header := webhook.Sign(secret, sent, body)

	// The receiver, with the secret's text form.
	key, err := webhook.DecodeSecret(text)
	if err != nil {
		panic(err)
	}
	fmt.Println(webhook.Verify(key, header, body, sent.Add(time.Second), 0))
	fmt.Println(webhook.Verify(key, header, []byte(`{"tampered":true}`), sent, 0) != nil)
	fmt.Println(webhook.Verify(key, header, body, sent.Add(time.Hour), 0) != nil)
	// Output:
	// <nil>
	// true
	// true
}
