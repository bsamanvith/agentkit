package trigger_test

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/pkg/trigger"
)

// TestStandardVector checks the signing against the Standard Webhooks
// reference libraries' test vector, so any of them can sign for a trigger.
func TestStandardVector(t *testing.T) {
	t.Parallel()
	secret, err := trigger.DecodeStandardSecret("whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw")
	if err != nil {
		t.Fatal(err)
	}
	const id, want = "msg_p5jXN8AQM9LWM0D4loKWxJek", "v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE="
	at := time.Unix(1614265330, 0)
	body := []byte(`{"test": 2432232314}`)
	if got := trigger.SignStandard(secret, id, at, body); got != want {
		t.Errorf("SignStandard = %q, want %q", got, want)
	}
	if err := trigger.VerifyStandard(secret, id, "1614265330", "v1,bm9wZQ== "+want, body, at, 0); err != nil {
		t.Errorf("VerifyStandard of the vector among others: %v", err)
	}
	if got := trigger.EncodeStandardSecret(secret); got != "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw" {
		t.Errorf("EncodeStandardSecret = %q", got)
	}
}

func TestVerifyStandardRefuses(t *testing.T) {
	t.Parallel()
	secret := []byte("s3cret")
	now := time.Unix(1_800_000_000, 0)
	ts := strconv.FormatInt(now.Unix(), 10)
	body := []byte(`{"a":1}`)
	sig := trigger.SignStandard(secret, "msg_1", now, body)
	if err := trigger.VerifyStandard(secret, "msg_1", ts, sig, body, now.Add(time.Minute), 0); err != nil {
		t.Fatalf("a fresh signature: %v", err)
	}
	for _, c := range []struct {
		name, id, ts, sig string
		body              []byte
		secret            []byte
		now               time.Time
	}{
		{"another body", "msg_1", ts, sig, []byte(`{"a":2}`), secret, now},
		{"another id", "msg_2", ts, sig, body, secret, now},
		{"another secret", "msg_1", ts, sig, body, []byte("other"), now},
		{"another timestamp", "msg_1", strconv.FormatInt(now.Unix()+1, 10), sig, body, secret, now},
		{"too old", "msg_1", ts, sig, body, secret, now.Add(trigger.DefaultTolerance + time.Second)},
		{"from the future", "msg_1", ts, sig, body, secret, now.Add(-trigger.DefaultTolerance - time.Second)},
		{"no id", "", ts, sig, body, secret, now},
		{"no timestamp", "msg_1", "", sig, body, secret, now},
		{"no signature", "msg_1", ts, "", body, secret, now},
		{"another version", "msg_1", ts, "v2," + sig[len("v1,"):], body, secret, now},
	} {
		if err := trigger.VerifyStandard(c.secret, c.id, c.ts, c.sig, c.body, c.now, 0); !errors.Is(err, trigger.ErrSignature) {
			t.Errorf("%s: VerifyStandard = %v, want ErrSignature", c.name, err)
		}
	}
	for _, s := range []string{"", "whsec_", "whsec_!!", "MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw"} {
		if _, err := trigger.DecodeStandardSecret(s); err == nil {
			t.Errorf("DecodeStandardSecret(%q) succeeded", s)
		}
	}
}
