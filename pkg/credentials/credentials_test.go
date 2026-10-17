package credentials_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/credentials"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

const value = "hunter2-secret-value"

func TestSecretNeverPrints(t *testing.T) {
	s := credentials.NewSecret(value)
	if s.Reveal() != value || s.IsZero() || !credentials.NewSecret("").IsZero() {
		t.Fatal("Reveal or IsZero is wrong")
	}
	type holder struct {
		Name  string
		Token credentials.Secret
		Ptr   *credentials.Secret
	}
	h := holder{Name: "x", Token: s, Ptr: &s}
	var outs []string
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%10s", "%-8v"} {
		outs = append(outs, fmt.Sprintf(verb, s), fmt.Sprintf(verb, h), fmt.Sprintf(verb, &h))
	}
	outs = append(outs, fmt.Sprint(s), fmt.Sprintln(h), fmt.Errorf("wrapped: %w and %v", errors.New("e"), s).Error())

	for _, json := range []bool{false, true} {
		var buf bytes.Buffer
		var handler slog.Handler = slog.NewTextHandler(&buf, nil)
		if json {
			handler = slog.NewJSONHandler(&buf, nil)
		}
		log := slog.New(handler)
		log.Info("m", "secret", s, "group", slog.GroupValue(slog.Any("inner", s)), "holder", h)
		outs = append(outs, buf.String())
	}
	for _, out := range outs {
		if strings.Contains(out, value) || strings.Contains(out, fmt.Sprintf("%x", value)) {
			t.Errorf("printed the value: %s", out)
		}
	}

	if _, err := json.Marshal(h); !errors.Is(err, credentials.ErrEncodeSecret) {
		t.Errorf("json.Marshal = %v, want ErrEncodeSecret", err)
	}
	if _, err := json.Marshal(map[credentials.Secret]int{s: 1}); err == nil {
		t.Error("json.Marshal of a map key succeeded")
	}
}

func key(b byte) string {
	return base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{b}, credentials.KeySize))
}

func TestParseKeyRing(t *testing.T) {
	raw := base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{0xfb}, credentials.KeySize))
	r, err := credentials.ParseKeyRing(" new:" + key(2) + ", old:" + raw + ",a:" + key(3))
	if err != nil {
		t.Fatal(err)
	}
	if r.Current() != "new" || fmt.Sprint(r.IDs()) != "[new a old]" {
		t.Errorf("current %q ids %v", r.Current(), r.IDs())
	}

	secretB64 := key(9)
	short := base64.StdEncoding.EncodeToString([]byte("too-short-key-material"))
	for name, in := range map[string]string{
		"empty":        "  ",
		"no colon":     secretB64,
		"bad id":       "a b:" + secretB64,
		"long id":      strings.Repeat("x", 33) + ":" + secretB64,
		"empty id":     ":" + secretB64,
		"not base64":   "k:" + secretB64 + "!!",
		"wrong length": "k:" + short,
		"duplicate":    "k:" + secretB64 + ",k:" + key(1),
		"empty part":   "k:" + secretB64 + ",",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := credentials.ParseKeyRing(in)
			if !errors.Is(err, credentials.ErrKeyConfig) {
				t.Fatalf("err = %v, want ErrKeyConfig", err)
			}
			for _, leak := range []string{secretB64, short, "too-short"} {
				if strings.Contains(err.Error(), leak) {
					t.Errorf("message holds key material: %v", err)
				}
			}
		})
	}
	if _, err := credentials.NewKeyRing("x", map[string][]byte{"y": make([]byte, 32)}); !errors.Is(err, credentials.ErrKeyConfig) {
		t.Errorf("missing current: %v", err)
	}
}

func sealer(t *testing.T, spec string) *credentials.Sealer {
	t.Helper()
	r, err := credentials.ParseKeyRing(spec)
	if err != nil {
		t.Fatal(err)
	}
	return credentials.NewSealer(r)
}

func TestSealer(t *testing.T) {
	ta, _ := tenancy.NewTenant("tenant-a")
	tb, _ := tenancy.NewTenant("tenant-b")
	k := credentials.Key{Tenant: ta, User: "u", Slot: "s"}
	s1 := sealer(t, "k1:"+key(1))
	s21 := sealer(t, "k2:"+key(2)+",k1:"+key(1))

	blob, err := s1.Seal(k, []byte(value))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte(value)) {
		t.Fatal("blob holds the plaintext")
	}
	again, _ := s1.Seal(k, []byte(value))
	if bytes.Equal(blob, again) {
		t.Fatal("two seals are equal: the nonce repeated")
	}

	for _, s := range []*credentials.Sealer{s1, s21} {
		got, id, err := s.Open(k, blob)
		if err != nil || string(got) != value || id != "k1" {
			t.Fatalf("Open = %q %q %v", got, id, err)
		}
	}

	// Another tenant, user or slot does not open it, nor does a near key
	// that would collide without length prefixes.
	for _, other := range []credentials.Key{
		{Tenant: tb, User: "u", Slot: "s"},
		{Tenant: ta, User: "", Slot: "s"},
		{Tenant: ta, User: "u", Slot: "t"},
		{Tenant: ta, User: "us", Slot: ""},
	} {
		if _, _, err := s1.Open(other, blob); !errors.Is(err, credentials.ErrCorrupt) {
			t.Errorf("Open for %+v = %v, want ErrCorrupt", other, err)
		}
	}

	// Rotation: reseal moves it to k2, which a k1-only ring cannot open.
	moved, err := s21.Reseal(k, blob)
	if err != nil {
		t.Fatal(err)
	}
	if _, id, err := s21.Open(k, moved); err != nil || id != "k2" {
		t.Fatalf("after reseal: %q %v", id, err)
	}
	if _, _, err := s1.Open(k, moved); !errors.Is(err, credentials.ErrUnknownKey) {
		t.Errorf("k1-only ring opening k2 blob = %v, want ErrUnknownKey", err)
	}
	// Same id, different key material: corrupt, not a wrong plaintext.
	if _, _, err := sealer(t, "k1:"+key(7)).Open(k, blob); !errors.Is(err, credentials.ErrCorrupt) {
		t.Errorf("wrong key = %v", err)
	}

	for name, b := range map[string][]byte{
		"empty":     nil,
		"version":   append([]byte{2}, blob[1:]...),
		"truncated": blob[:len(blob)-1],
		"header":    blob[:5],
		"id length": append([]byte{1, 0}, blob[2:]...),
	} {
		if _, _, err := s1.Open(k, b); !errors.Is(err, credentials.ErrCorrupt) {
			t.Errorf("%s: %v, want ErrCorrupt", name, err)
		}
	}
}
