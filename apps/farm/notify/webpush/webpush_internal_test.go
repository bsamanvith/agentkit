package webpush

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/notify"
	"github.com/bsamanvith/agentkit/apps/farm/notify/notifytest"
)

// Decrypt is the browser's side of RFC 8291, for tests: it opens a body
// made by Encrypt with the subscription's private key and auth secret.
func Decrypt(ua *ecdh.PrivateKey, auth, body []byte) ([]byte, error) {
	if len(body) < 21 {
		return nil, errors.New("short")
	}
	salt, rs, idlen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	if rs < 18 || len(body) < 21+idlen {
		return nil, errors.New("bad header")
	}
	asPub, ct := body[21:21+idlen], body[21+idlen:]
	as, err := ecdh.P256().NewPublicKey(asPub)
	if err != nil {
		return nil, err
	}
	shared, err := ua.ECDH(as)
	if err != nil {
		return nil, err
	}
	ikm, _ := hkdf.Key(sha256.New, shared, auth, "WebPush: info\x00"+string(ua.PublicKey().Bytes())+string(asPub), 32)
	cek, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, err
	}
	pt = bytes.TrimRight(pt, "\x00")
	if len(pt) == 0 || pt[len(pt)-1] != 2 {
		return nil, errors.New("no last-record delimiter")
	}
	return pt[:len(pt)-1], nil
}

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := decode(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestRFC8291Example checks Encrypt against the example in RFC 8291's
// appendix A, byte for byte.
func TestRFC8291Example(t *testing.T) {
	as, err := ecdh.P256().NewPrivateKey(mustB64(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	if got := b64.EncodeToString(as.PublicKey().Bytes()); got != "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8" {
		t.Fatalf("the example's sender public key: %s", got)
	}
	body, err := encrypt(
		"BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4",
		"BTBZMqHH6r4Tts7J_aSIgg",
		[]byte("When I grow up, I want to be a watermelon"),
		as, mustB64(t, "DGv6ra1nlYgDCS1FRnbzlw"))
	if err != nil {
		t.Fatal(err)
	}
	const want = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if got := b64.EncodeToString(body); got != want {
		t.Errorf("body\n got %s\nwant %s", got, want)
	}
	ua, err := ecdh.P256().NewPrivateKey(mustB64(t, "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"))
	if err != nil {
		t.Fatal(err)
	}
	pt, err := Decrypt(ua, mustB64(t, "BTBZMqHH6r4Tts7J_aSIgg"), body)
	if err != nil || string(pt) != "When I grow up, I want to be a watermelon" {
		t.Errorf("Decrypt = %q, %v", pt, err)
	}
}

func TestKeysPersistAndSign(t *testing.T) {
	path := filepath.Join(t.TempDir(), "push.json")
	k1, err := LoadOrCreateKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := LoadOrCreateKeys(path)
	if err != nil {
		t.Fatal(err)
	}
	if k1.Public() != k2.Public() || len(mustB64(t, k1.Public())) != 65 {
		t.Errorf("keys did not persist: %s, %s", k1.Public(), k2.Public())
	}
	jwt, err := k1.token("https://fcm.googleapis.com", "mailto:a@b.c", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !verify(mustB64(t, k2.Public()), jwt) {
		t.Error("the VAPID token does not verify with the public key")
	}
	other, _ := GenerateKeys()
	if verify(mustB64(t, other.Public()), jwt) {
		t.Error("the VAPID token verifies with another key")
	}
}

// service is a fake push service: it checks the VAPID header and decrypts
// what it gets with the subscription's private key.
type service struct {
	ua    *ecdh.PrivateKey
	auth  []byte
	vapid []byte
	mu    sync.Mutex
	got   []string
	codes []int
}

func (s *service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.codes) > 0 {
		code := s.codes[0]
		s.codes = s.codes[1:]
		w.WriteHeader(code)
		return
	}
	authz := r.Header.Get("Authorization")
	t, k, ok := strings.Cut(strings.TrimPrefix(authz, "vapid t="), ", k=")
	if !ok || k != b64.EncodeToString(s.vapid) || !verify(s.vapid, t) || r.Header.Get("Content-Encoding") != "aes128gcm" || r.Header.Get("TTL") == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	body, _ := io.ReadAll(r.Body)
	pt, err := Decrypt(s.ua, s.auth, body)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	s.got = append(s.got, r.Header.Get("Urgency")+" "+string(pt))
	w.WriteHeader(http.StatusCreated)
}

func newService(t *testing.T) (*service, *httptest.Server, notify.PushSubscription) {
	t.Helper()
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	s := &service{ua: ua, auth: []byte("0123456789abcdef")}
	srv := httptest.NewTLSServer(s)
	t.Cleanup(srv.Close)
	sub := notify.PushSubscription{Endpoint: srv.URL + "/push/abc"}
	sub.Keys.P256dh, sub.Keys.Auth = b64.EncodeToString(ua.PublicKey().Bytes()), b64.EncodeToString(s.auth)
	return s, srv, sub
}

func newLane(t *testing.T, s *service, srv *httptest.Server, drop func(context.Context, string, string)) *Lane {
	t.Helper()
	keys, _ := GenerateKeys()
	s.vapid = mustB64(t, keys.Public())
	l, err := New(Config{Keys: keys, Subject: "mailto:alerts@farm.example", Client: srv.Client(), Drop: drop})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestLane(t *testing.T) {
	notifytest.RunLane(t, func(t *testing.T) notifytest.LaneHarness {
		s, srv, sub := newService(t)
		return notifytest.LaneHarness{
			Lane: newLane(t, s, srv, nil),
			To:   notify.Recipient{ID: "r1", Push: []notify.PushSubscription{sub}},
			Fail: func(kind string) bool {
				code := map[string]int{notifytest.FailTemporary: 503, notifytest.FailRejected: 413}[kind]
				if code == 0 {
					return false // a gone subscription is dropped, not a STOP: TestGoneSubscriptions
				}
				s.mu.Lock()
				s.codes = append(s.codes, code)
				s.mu.Unlock()
				return true
			},
			Got: func() []string { s.mu.Lock(); defer s.mu.Unlock(); return append([]string(nil), s.got...) },
		}
	})
}

func TestGoneSubscriptions(t *testing.T) {
	s, srv, sub := newService(t)
	var dropped []string
	l := newLane(t, s, srv, func(_ context.Context, r, endpoint string) { dropped = append(dropped, r+" "+endpoint) })
	old := sub
	old.Endpoint += "-old"
	m := notify.Message{
		ID: "m1", Severity: notify.Urgent, Title: "Rain", Body: "Cover the harvest",
		Recipient: notify.Recipient{ID: "r1", Push: []notify.PushSubscription{old, sub}},
	}
	s.codes = []int{http.StatusGone}
	if _, err := l.Deliver(t.Context(), m); err != nil {
		t.Fatalf("one gone, one live: %v", err)
	}
	if len(dropped) != 1 || dropped[0] != "r1 "+old.Endpoint {
		t.Errorf("dropped %v, want the old one", dropped)
	}
	if len(s.got) != 1 || !strings.HasPrefix(s.got[0], "high ") || !strings.Contains(s.got[0], `"title":"Rain"`) {
		t.Errorf("got %v", s.got)
	}
	s.codes = []int{http.StatusNotFound}
	m.Recipient.Push = []notify.PushSubscription{sub}
	if _, err := l.Deliver(t.Context(), m); !errors.Is(err, notify.ErrRejected) || errors.Is(err, notify.ErrGone) {
		t.Errorf("all gone: %v, want a rejection (dropping them is enough; push is not stopped)", err)
	}
}
