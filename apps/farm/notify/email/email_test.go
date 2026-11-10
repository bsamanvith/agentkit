package email_test

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net"
	"net/mail"
	"strings"
	"sync"
	"testing"

	"github.com/bsamanvith/agentkit/apps/farm/notify"
	"github.com/bsamanvith/agentkit/apps/farm/notify/email"
	"github.com/bsamanvith/agentkit/apps/farm/notify/notifytest"
)

// server is a fake SMTP server: it takes mail, or answers RCPT with a
// failure code when told.
type server struct {
	ln   net.Listener
	mu   sync.Mutex
	got  []string
	fail []int
	auth []string
}

func newServer(t *testing.T) *server {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &server{ln: ln}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

func (s *server) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	say := func(line string) { _, _ = io.WriteString(c, line+"\r\n") }
	say("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"):
			say("250-fake")
			say("250 AUTH PLAIN")
		case strings.HasPrefix(cmd, "AUTH PLAIN"):
			s.mu.Lock()
			s.auth = append(s.auth, strings.TrimSpace(line[len("AUTH PLAIN"):]))
			s.mu.Unlock()
			say("235 ok")
		case strings.HasPrefix(cmd, "MAIL"):
			say("250 ok")
		case strings.HasPrefix(cmd, "RCPT"):
			s.mu.Lock()
			code := 0
			if len(s.fail) > 0 {
				code, s.fail = s.fail[0], s.fail[1:]
			}
			s.mu.Unlock()
			if code != 0 {
				say(fmt.Sprintf("%d no", code))
				continue
			}
			say("250 ok")
		case cmd == "DATA":
			say("354 go on")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			s.mu.Lock()
			s.got = append(s.got, b.String())
			s.mu.Unlock()
			say("250 queued")
		case cmd == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

// readable decodes a raw message into its headers and its text.
func readable(raw string) string {
	m, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		return "unreadable: " + err.Error()
	}
	subj, _ := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	body, _ := io.ReadAll(base64.NewDecoder(base64.StdEncoding, m.Body))
	return fmt.Sprintf("To: %s\nSubject: %s\nList-Unsubscribe: %s\n\n%s", m.Header.Get("To"), subj, m.Header.Get("List-Unsubscribe"), body)
}

func (s *server) messages() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.got))
	for i, g := range s.got {
		out[i] = readable(g)
	}
	return out
}

func lane(t *testing.T, s *server) *email.Lane {
	t.Helper()
	l, err := email.New(email.Config{
		URL: "smtp://farm:secret@" + s.ln.Addr().String(), From: "Farm app <alerts@farm.example>",
		Unsubscribe: func(m notify.Message) string { return "https://farm.example/unsubscribe?m=" + m.ID },
	})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestLane(t *testing.T) {
	notifytest.RunLane(t, func(t *testing.T) notifytest.LaneHarness {
		s := newServer(t)
		return notifytest.LaneHarness{
			Lane: lane(t, s),
			To:   notify.Recipient{ID: "r1", Name: "Ravi", Email: "ravi@example.com"},
			Fail: func(kind string) bool {
				code := map[string]int{notifytest.FailTemporary: 451, notifytest.FailRejected: 550}[kind]
				if code == 0 {
					return false
				}
				s.mu.Lock()
				s.fail = append(s.fail, code)
				s.mu.Unlock()
				return true
			},
			Got: s.messages,
		}
	})
}

func TestTeluguAndUnsubscribe(t *testing.T) {
	s := newServer(t)
	l := lane(t, s)
	m := notify.Message{
		ID: "m1", Severity: notify.Warning, Title: "గురువారం భారీ వర్షం", Body: "కాలువలు శుభ్రం చేయండి.",
		Recipient: notify.Recipient{ID: "r1", Name: "రవి", Email: "ravi@example.com"},
	}
	rc, err := l.Deliver(t.Context(), m)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rc.ProviderID, "<") || !strings.HasSuffix(rc.ProviderID, "@farm.example>") {
		t.Errorf("ProviderID %q, want a Message-ID", rc.ProviderID)
	}
	got := s.messages()[0]
	for _, want := range []string{"Subject: గురువారం భారీ వర్షం", "కాలువలు శుభ్రం చేయండి.", "List-Unsubscribe: <https://farm.example/unsubscribe?m=m1>", "To stop these emails"} {
		if !strings.Contains(got, want) {
			t.Errorf("the mail lacks %q:\n%s", want, got)
		}
	}
	s.mu.Lock()
	auth, _ := base64.StdEncoding.DecodeString(s.auth[0])
	s.mu.Unlock()
	if string(auth) != "\x00farm\x00secret" {
		t.Errorf("AUTH PLAIN %q", auth)
	}
}

func TestConfig(t *testing.T) {
	for _, c := range []email.Config{
		{URL: "http://x", From: "a@b.c"},
		{URL: "smtp://", From: "a@b.c"},
		{URL: "smtp://host", From: "not an address"},
	} {
		if _, err := email.New(c); err == nil {
			t.Errorf("New(%+v) took it", c)
		}
	}
}
