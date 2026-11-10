// Package webpush is the web push lane: notifications to the farm app
// installed on a phone or open in a browser, through the browser's own
// push service (Google's for Chrome on Android, Mozilla's, Apple's).
// FREE: no account and no charge; the app signs its requests with its own
// VAPID key (RFC 8292) and encrypts each message for the browser (RFC
// 8291), so the push service cannot read it.
package webpush

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/notify"
)

var b64 = base64.RawURLEncoding

// decode reads base64url with or without padding, or standard base64.
func decode(s string) ([]byte, error) {
	s = strings.TrimRight(s, "=")
	if b, err := b64.DecodeString(s); err == nil {
		return b, nil
	}
	return base64.RawStdEncoding.DecodeString(s)
}

// Keys is the app's VAPID key pair.
type Keys struct{ priv *ecdsa.PrivateKey }

// GenerateKeys makes a new key pair.
func GenerateKeys() (*Keys, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return &Keys{priv: k}, nil
}

// LoadOrCreateKeys reads the key pair from path (a JSON file only the
// app's user can read), making and saving one the first time. Keep it:
// browsers subscribed with the old public key stop getting messages when
// it changes.
func LoadOrCreateKeys(path string) (*Keys, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		k, err := GenerateKeys()
		if err != nil {
			return nil, err
		}
		d, _ := k.priv.Bytes()
		data, _ := json.Marshal(map[string]string{"vapid_private_key": b64.EncodeToString(d)})
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return nil, fmt.Errorf("webpush: save keys: %w", err)
		}
		return k, nil
	}
	if err != nil {
		return nil, fmt.Errorf("webpush: read keys: %w", err)
	}
	var f struct {
		D string `json:"vapid_private_key"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("webpush: read keys %s: %w", path, err)
	}
	d, err := decode(f.D)
	if err != nil {
		return nil, fmt.Errorf("webpush: read keys %s: %w", path, err)
	}
	priv, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), d)
	if err != nil {
		return nil, fmt.Errorf("webpush: read keys %s: %w", path, err)
	}
	return &Keys{priv: priv}, nil
}

// Public is the public key, base64url of the uncompressed point: the
// applicationServerKey the browser subscribes with.
func (k *Keys) Public() string {
	b, _ := k.priv.PublicKey.Bytes()
	return b64.EncodeToString(b)
}

// token is the VAPID JWT (ES256) for the push service at aud.
func (k *Keys) token(aud, subject string, exp time.Time) (string, error) {
	head := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, _ := json.Marshal(map[string]any{"aud": aud, "exp": exp.Unix(), "sub": subject})
	signing := head + "." + b64.EncodeToString(claims)
	h := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, k.priv, h[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + b64.EncodeToString(sig), nil
}

// verify checks a VAPID JWT made by token (for tests and the conformance
// fake).
func verify(pub []byte, jwt string) bool {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return false
	}
	sig, err := b64.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return false
	}
	key, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), pub)
	if err != nil {
		return false
	}
	h := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	return ecdsa.Verify(key, h[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:]))
}

// Encrypt encrypts plaintext for a subscription (RFC 8291, aes128gcm in
// one record), with a fresh key pair and salt.
func Encrypt(p256dh, auth string, plaintext []byte) ([]byte, error) {
	as, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return encrypt(p256dh, auth, plaintext, as, salt)
}

func encrypt(p256dh, auth string, plaintext []byte, as *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	uaPub, err := decode(p256dh)
	if err != nil {
		return nil, fmt.Errorf("webpush: p256dh: %w", err)
	}
	secret, err := decode(auth)
	if err != nil || len(secret) != 16 {
		return nil, fmt.Errorf("webpush: auth secret: want 16 bytes")
	}
	ua, err := ecdh.P256().NewPublicKey(uaPub)
	if err != nil {
		return nil, fmt.Errorf("webpush: p256dh: %w", err)
	}
	shared, err := as.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPub := as.PublicKey().Bytes()
	keyInfo := "WebPush: info\x00" + string(uaPub) + string(asPub)
	ikm, err := hkdf.Key(sha256.New, shared, secret, keyInfo, 32)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	const rs = 4096
	if len(plaintext)+1+16 > rs {
		return nil, fmt.Errorf("webpush: a message of %d bytes is over one record", len(plaintext))
	}
	var out bytes.Buffer
	out.Write(salt)
	_ = binary.Write(&out, binary.BigEndian, uint32(rs))
	out.WriteByte(byte(len(asPub)))
	out.Write(asPub)
	out.Write(gcm.Seal(nil, nonce, append(append([]byte{}, plaintext...), 2), nil))
	return out.Bytes(), nil
}

// Config configures the lane.
type Config struct {
	Keys *Keys
	// Subject is a contact for the push services: mailto: or https: (RFC
	// 8292 asks for one).
	Subject string
	// Drop is called with a subscription the push service says is gone
	// (404, 410), to remove it from the person.
	Drop func(ctx context.Context, recipient, endpoint string)
	// Client calls the push services; give one that keeps requests off
	// the internal network (pkg/netguard), since endpoints come from
	// browsers.
	Client *http.Client
	Log    *slog.Logger
}

// Lane sends web push notifications.
type Lane struct{ cfg Config }

// New returns the lane.
func New(c Config) (*Lane, error) {
	if c.Keys == nil {
		return nil, fmt.Errorf("%w: webpush: needs the VAPID keys", notify.ErrInvalid)
	}
	if !strings.HasPrefix(c.Subject, "mailto:") && !strings.HasPrefix(c.Subject, "https://") {
		return nil, fmt.Errorf("%w: webpush: the subject is a mailto: or https: URL", notify.ErrInvalid)
	}
	if c.Client == nil {
		c.Client = &http.Client{Timeout: 30 * time.Second}
	}
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
	return &Lane{cfg: c}, nil
}

// Info implements notify.Lane.
func (l *Lane) Info() notify.LaneInfo {
	return notify.LaneInfo{Name: "push", Address: notify.AddrPush, Note: "Notifications to the installed app or the browser (web push). Free."}
}

// Payload is what the app's service worker gets.
type Payload struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	Severity string `json:"severity"`
	Link     string `json:"link,omitempty"`
	Farm     string `json:"farm,omitempty"`
}

// Deliver implements notify.Lane: the message goes to each of the
// person's browsers. It succeeds when one took it; subscriptions that are
// gone are dropped.
func (l *Lane) Deliver(ctx context.Context, m notify.Message) (notify.Receipt, error) {
	subs := m.Recipient.Push
	if len(subs) == 0 {
		return notify.Receipt{}, notify.Rejected(errors.New("webpush: no subscription"))
	}
	body := m.Body
	if len(body) > 2500 {
		body = body[:2500] + "…"
	}
	payload, _ := json.Marshal(Payload{ID: m.ID, Title: m.Title, Body: body, Severity: m.Severity, Link: m.Link, Farm: m.Farm})
	ttl, urgency := 24*time.Hour, "normal"
	switch m.Severity {
	case notify.Urgent:
		ttl, urgency = 12*time.Hour, "high"
	case notify.Advice:
		urgency = "low"
	}
	var errs []error
	sent, gone := 0, 0
	for _, s := range subs {
		err := l.push(ctx, s, payload, ttl, urgency)
		switch {
		case err == nil:
			sent++
		case errors.Is(err, notify.ErrGone):
			gone++
			if l.cfg.Drop != nil {
				l.cfg.Drop(context.WithoutCancel(ctx), m.Recipient.ID, s.Endpoint)
			}
		default:
			errs = append(errs, err)
		}
	}
	switch {
	case sent > 0:
		return notify.Receipt{Status: notify.StatusSent}, nil
	case len(errs) == 0:
		return notify.Receipt{}, notify.Rejected(fmt.Errorf("webpush: all %d subscriptions are gone", gone))
	}
	for _, err := range errs {
		if errors.Is(err, notify.ErrTemporary) {
			return notify.Receipt{}, err
		}
	}
	return notify.Receipt{}, errs[0]
}

func (l *Lane) push(ctx context.Context, s notify.PushSubscription, payload []byte, ttl time.Duration, urgency string) error {
	u, err := url.Parse(s.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return notify.Gone(fmt.Errorf("webpush: bad endpoint"))
	}
	body, err := Encrypt(s.Keys.P256dh, s.Keys.Auth, payload)
	if err != nil {
		return notify.Gone(err) // keys the browser gave that do not work: the subscription is useless
	}
	jwt, err := l.cfg.Keys.token(u.Scheme+"://"+u.Host, l.cfg.Subject, time.Now().Add(12*time.Hour))
	if err != nil {
		return notify.Temporary(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Endpoint, bytes.NewReader(body))
	if err != nil {
		return notify.Rejected(err)
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", strconv.Itoa(int(ttl.Seconds())))
	req.Header.Set("Urgency", urgency)
	req.Header.Set("Authorization", "vapid t="+jwt+", k="+l.cfg.Keys.Public())
	resp, err := l.cfg.Client.Do(req)
	if err != nil {
		return notify.Temporary(fmt.Errorf("webpush %s: %w", u.Host, err))
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	why := fmt.Errorf("webpush %s: HTTP %d", u.Host, resp.StatusCode)
	switch {
	case resp.StatusCode/100 == 2:
		return nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return notify.Gone(why)
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return notify.Temporary(why)
	}
	return notify.Rejected(why)
}
