// Package email is the email lane: plain text mail over SMTP, so any
// provider works (Brevo, Amazon SES, Gmail, a company's own server). Free
// tiers of a few hundred messages a day exist; which one is a setting.
package email

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/apps/farm/notify"
)

// Config configures the lane.
type Config struct {
	// URL is the server: smtp://user:password@host:587 (STARTTLS when the
	// server offers it, which a server that is not this machine must) or
	// smtps://user:password@host:465 (TLS from the start).
	URL string
	// From is the sender, "Farm app <alerts@example.com>".
	From string
	// Unsubscribe returns the one-click unsubscribe link of a message
	// (List-Unsubscribe), or "" for none.
	Unsubscribe func(m notify.Message) string
	// Timeout bounds one send (default 30s).
	Timeout time.Duration
	// TLS is the TLS configuration (default the system's roots).
	TLS *tls.Config
}

// Lane sends email.
type Lane struct {
	cfg              Config
	host, addr, user string
	pass             string
	implicitTLS      bool
	from             *mail.Address
}

// New returns the lane.
func New(c Config) (*Lane, error) {
	u, err := url.Parse(c.URL)
	if err != nil || (u.Scheme != "smtp" && u.Scheme != "smtps") || u.Hostname() == "" {
		return nil, fmt.Errorf("%w: email: want smtp://user:password@host:587 or smtps://...:465", notify.ErrInvalid)
	}
	from, err := mail.ParseAddress(c.From)
	if err != nil {
		return nil, fmt.Errorf("%w: email: from %q: %v", notify.ErrInvalid, c.From, err)
	}
	l := &Lane{cfg: c, host: u.Hostname(), implicitTLS: u.Scheme == "smtps", from: from}
	port := u.Port()
	if port == "" {
		port = "587"
		if l.implicitTLS {
			port = "465"
		}
	}
	l.addr = net.JoinHostPort(l.host, port)
	if u.User != nil {
		l.user = u.User.Username()
		l.pass, _ = u.User.Password()
	}
	if l.cfg.Timeout == 0 {
		l.cfg.Timeout = 30 * time.Second
	}
	return l, nil
}

// Info implements notify.Lane.
func (l *Lane) Info() notify.LaneInfo {
	return notify.LaneInfo{Name: "email", Address: notify.AddrEmail, Note: "Email over SMTP from " + l.from.Address + " via " + l.host + ". Free tiers send a few hundred a day."}
}

// Deliver implements notify.Lane.
func (l *Lane) Deliver(ctx context.Context, m notify.Message) (notify.Receipt, error) {
	to := m.Recipient.Email
	if to == "" {
		return notify.Receipt{}, notify.Rejected(errors.New("email: no address"))
	}
	if _, err := mail.ParseAddress(to); err != nil {
		return notify.Receipt{}, notify.Rejected(fmt.Errorf("email: %q: %w", to, err))
	}
	id := messageID(l.from.Address)
	body := l.compose(m, to, id)
	ctx, cancel := context.WithTimeout(ctx, l.cfg.Timeout)
	defer cancel()
	if err := l.send(ctx, to, body); err != nil {
		return notify.Receipt{}, classify(ctx, err)
	}
	return notify.Receipt{Status: notify.StatusSent, ProviderID: id}, nil
}

func messageID(from string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	domain := from[strings.LastIndexByte(from, '@')+1:]
	return "<" + hex.EncodeToString(b) + "@" + domain + ">"
}

// compose writes the message: UTF-8 plain text in base64, so Telugu and
// Hindi arrive whole.
func (l *Lane) compose(m notify.Message, to, id string) []byte {
	var b bytes.Buffer
	h := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	h("From", l.from.String())
	h("To", (&mail.Address{Name: m.Recipient.Name, Address: to}).String())
	subject := m.Title
	if subject == "" {
		subject = "A message from your farm app"
	}
	h("Subject", mime.QEncoding.Encode("utf-8", subject))
	h("Date", time.Now().UTC().Format(time.RFC1123Z))
	h("Message-ID", id)
	h("MIME-Version", "1.0")
	h("Content-Type", "text/plain; charset=utf-8")
	h("Content-Transfer-Encoding", "base64")
	if m.Severity == notify.Urgent {
		h("X-Priority", "1")
	}
	text := m.Body
	if m.Link != "" {
		text += "\n\n" + m.Link
	}
	if l.cfg.Unsubscribe != nil {
		if u := l.cfg.Unsubscribe(m); u != "" {
			h("List-Unsubscribe", "<"+u+">")
			h("List-Unsubscribe-Post", "List-Unsubscribe=One-Click")
			text += "\n\n--\nTo stop these emails: " + u
		}
	}
	b.WriteString("\r\n")
	enc := base64.StdEncoding.EncodeToString([]byte(strings.ReplaceAll(text, "\n", "\r\n")))
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
	return b.Bytes()
}

func (l *Lane) send(ctx context.Context, to string, body []byte) error {
	tlsCfg := l.cfg.TLS
	if tlsCfg == nil {
		tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	tlsCfg = tlsCfg.Clone()
	tlsCfg.ServerName = l.host
	d := net.Dialer{}
	var conn net.Conn
	var err error
	if l.implicitTLS {
		conn, err = (&tls.Dialer{NetDialer: &d, Config: tlsCfg}).DialContext(ctx, "tcp", l.addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", l.addr)
	}
	if err != nil {
		return err
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()
	c, err := smtp.NewClient(conn, l.host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer c.Close()
	if !l.implicitTLS {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsCfg); err != nil {
				return err
			}
		}
	}
	if l.user != "" {
		if err := c.Auth(smtp.PlainAuth("", l.user, l.pass, l.host)); err != nil {
			return err
		}
	}
	if err := c.Mail(l.from.Address); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(body); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// classify: a 4xx reply is temporary, a 5xx final (550 on the recipient
// is a mailbox that does not exist), the network temporary.
func classify(ctx context.Context, err error) error {
	var te *textproto.Error
	switch {
	case errors.As(err, &te) && te.Code >= 400 && te.Code < 500:
		return notify.Temporary(fmt.Errorf("email: %w", err))
	case errors.As(err, &te):
		return notify.Rejected(fmt.Errorf("email: %w", err))
	case ctx.Err() != nil:
		return notify.Temporary(fmt.Errorf("email: %w", ctx.Err()))
	}
	return notify.Temporary(fmt.Errorf("email: %w", err))
}
