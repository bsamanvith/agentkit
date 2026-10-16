package gmail

import (
	"bytes"
	"encoding/base64"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/connectors/google/common"
)

const (
	maxRecipients  = 100
	maxSubjectLen  = 998 // RFC 5322's line limit; longer is refused, not folded
	maxComposeBody = 100_000
)

// composeIn is the input of the tools that write a message.
type composeIn struct {
	To               []string `json:"to,omitempty" jsonschema:"recipient addresses, such as ada@example.com or Ada <ada@example.com>; may be left out when replying"`
	Cc               []string `json:"cc,omitempty" jsonschema:"cc addresses"`
	Bcc              []string `json:"bcc,omitempty" jsonschema:"bcc addresses"`
	Subject          string   `json:"subject,omitempty" jsonschema:"subject line; when replying, defaults to Re: and the original subject"`
	Body             string   `json:"body" jsonschema:"the message as plain text"`
	ReplyToMessageID string   `json:"reply_to_message_id,omitempty" jsonschema:"id of the message to reply to: keeps the conversation together and defaults the recipient and subject"`
}

// original is what a reply needs from the message it answers.
type original struct {
	threadID   string
	messageID  string // its Message-ID header
	references string
	subject    string
	replyTo    string // its Reply-To, else From
}

// message is a validated message ready to encode.
type message struct {
	to, cc, bcc []string
	subject     string
	body        string
	inReplyTo   string
	references  string
	threadID    string
}

// compose validates in (and the message it replies to, if any) and returns the
// message. Every problem is an input error, found before any request is made
// for the message itself.
func compose(in composeIn, orig *original) (message, error) {
	var m message
	var err error
	for _, f := range []struct {
		name string
		list []string
		dst  *[]string
	}{{"to", in.To, &m.to}, {"cc", in.Cc, &m.cc}, {"bcc", in.Bcc, &m.bcc}} {
		if *f.dst, err = addresses(f.name, f.list); err != nil {
			return m, err
		}
	}
	m.subject = strings.TrimSpace(in.Subject)
	m.body = in.Body
	if orig != nil {
		m.threadID, m.inReplyTo, m.references = orig.threadID, orig.messageID, orig.references
		if m.inReplyTo != "" && !strings.Contains(m.references, m.inReplyTo) {
			m.references = strings.TrimSpace(m.references + " " + m.inReplyTo)
		}
		if len(m.to) == 0 && orig.replyTo != "" {
			if m.to, err = addresses("to", []string{orig.replyTo}); err != nil {
				return m, common.Invalid("the message to reply to has no usable reply address; give to")
			}
		}
		if m.subject == "" && orig.subject != "" {
			m.subject = orig.subject
			if !strings.HasPrefix(strings.ToLower(m.subject), "re:") {
				m.subject = "Re: " + m.subject
			}
		}
	}
	switch {
	case len(m.to)+len(m.cc)+len(m.bcc) == 0:
		return m, common.Invalid("no recipients: give to, cc or bcc")
	case len(m.to)+len(m.cc)+len(m.bcc) > maxRecipients:
		return m, common.Invalid("more than %d recipients", maxRecipients)
	case strings.TrimSpace(m.body) == "":
		return m, common.Invalid("body is empty")
	case utf8.RuneCountInString(m.body) > maxComposeBody:
		return m, common.Invalid("body is over %d characters", maxComposeBody)
	case len(m.subject) > maxSubjectLen:
		return m, common.Invalid("subject is over %d bytes", maxSubjectLen)
	case hasLineBreak(m.subject):
		return m, common.Invalid("subject must be one line")
	}
	return m, nil
}

func hasLineBreak(s string) bool { return strings.ContainsAny(s, "\r\n") }

// addresses parses each address with net/mail and returns them in a form
// that is safe to put in a header. A line break anywhere is refused: it would
// start a new header (header injection).
func addresses(field string, list []string) ([]string, error) {
	out := make([]string, 0, len(list))
	for _, raw := range list {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if hasLineBreak(raw) {
			return nil, common.Invalid("%s: an address contains a line break", field)
		}
		a, err := mail.ParseAddress(raw)
		if err != nil {
			return nil, common.Invalid("%s: %q is not an email address", field, raw)
		}
		out = append(out, a.String())
	}
	return out, nil
}

// raw encodes the message as RFC 5322 text, base64url as the API wants.
func (m message) raw() string {
	var b bytes.Buffer
	line := func(name, value string) {
		if value != "" {
			b.WriteString(name + ": " + value + "\r\n")
		}
	}
	line("To", strings.Join(m.to, ", "))
	line("Cc", strings.Join(m.cc, ", "))
	line("Bcc", strings.Join(m.bcc, ", "))
	line("Subject", mime.QEncoding.Encode("utf-8", m.subject))
	line("In-Reply-To", m.inReplyTo)
	line("References", m.references)
	line("MIME-Version", "1.0")
	line("Content-Type", `text/plain; charset="UTF-8"`)
	line("Content-Transfer-Encoding", "quoted-printable")
	b.WriteString("\r\n")
	w := quotedprintable.NewWriter(&b)
	_, _ = w.Write([]byte(strings.ReplaceAll(normalizeNewlines(m.body), "\n", "\r\n")))
	_ = w.Close()
	return base64.URLEncoding.EncodeToString(b.Bytes())
}
