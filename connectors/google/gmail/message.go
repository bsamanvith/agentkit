package gmail

import (
	"encoding/base64"
	"html"
	"regexp"
	"strings"
	"unicode/utf8"
)

// The Gmail API's message resource, the parts the tools read.
type (
	apiMessage struct {
		ID       string     `json:"id"`
		ThreadID string     `json:"threadId"`
		LabelIDs []string   `json:"labelIds"`
		Snippet  string     `json:"snippet"`
		Payload  apiPayload `json:"payload"`
	}
	apiPayload struct {
		MIMEType string       `json:"mimeType"`
		Filename string       `json:"filename"`
		Headers  []apiHeader  `json:"headers"`
		Body     apiBody      `json:"body"`
		Parts    []apiPayload `json:"parts"`
	}
	apiHeader struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	apiBody struct {
		Size         int    `json:"size"`
		Data         string `json:"data"`
		AttachmentID string `json:"attachmentId"`
	}
)

// header returns the first header called name (case-insensitive), cleaned.
func (p apiPayload) header(name string) string {
	for _, h := range p.Headers {
		if strings.EqualFold(h.Name, name) {
			return cleanHeader(h.Value)
		}
	}
	return ""
}

const maxHeaderChars = 500

// cleanHeader makes a header value fit for a model: control characters out,
// length capped. (Mail headers are third-party text; they are data.)
func cleanHeader(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > maxHeaderChars {
		s = string([]rune(s)[:maxHeaderChars]) + "…"
	}
	return s
}

// maxWalkDepth bounds the MIME tree the reader follows, so a hostile message
// cannot recurse without end.
const maxWalkDepth = 20

type attachment struct {
	Filename     string `json:"filename"`
	MIMEType     string `json:"mime_type"`
	Size         int    `json:"size"`
	AttachmentID string `json:"attachment_id,omitempty"`
}

// content is a message's readable text and its attachments.
type content struct {
	text        string
	attachments []attachment
}

// extract finds a message's text and lists its attachments. It prefers a
// text/plain part; failing that it reduces text/html to text.
func extract(p apiPayload) content {
	var plain, htm []string
	var atts []attachment
	var walk func(p apiPayload, depth int)
	walk = func(p apiPayload, depth int) {
		if depth > maxWalkDepth {
			return
		}
		mt := strings.ToLower(p.MIMEType)
		switch {
		case p.Filename != "":
			atts = append(atts, attachment{Filename: cleanHeader(p.Filename), MIMEType: mt, Size: p.Body.Size, AttachmentID: p.Body.AttachmentID})
		case len(p.Parts) > 0:
			for _, c := range p.Parts {
				walk(c, depth+1)
			}
		case mt == "text/plain":
			plain = append(plain, decodeBody(p.Body.Data))
		case mt == "text/html":
			htm = append(htm, decodeBody(p.Body.Data))
		}
	}
	walk(p, 0)
	var text string
	switch {
	case len(plain) > 0:
		text = strings.Join(plain, "\n\n")
	case len(htm) > 0:
		text = htmlToText(strings.Join(htm, "\n"))
	}
	return content{text: strings.TrimSpace(normalizeNewlines(text)), attachments: atts}
}

// decodeBody decodes the API's base64url body data; bad data is empty text.
func decodeBody(data string) string {
	if data == "" {
		return ""
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(data, "="))
	if err != nil {
		return ""
	}
	if !utf8.Valid(b) {
		return strings.ToValidUTF8(string(b), "�")
	}
	return string(b)
}

func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

var (
	reHidden   = regexp.MustCompile(`(?is)<(script|style|head)\b.*?</(script|style|head)\s*>`)
	reComment  = regexp.MustCompile(`(?s)<!--.*?-->`)
	reBreak    = regexp.MustCompile(`(?i)<\s*(br|/p|/div|/li|/tr|/h[1-6]|/table|/blockquote)\b[^>]*>`)
	reTag      = regexp.MustCompile(`(?s)<[^>]*>`)
	reSpaces   = regexp.MustCompile(`[ \t\f\v\x{a0}]+`)
	reBlankRun = regexp.MustCompile(`\n[ ]*(\n[ ]*)+`)
)

// htmlToText reduces HTML mail to readable text: scripts, styles and
// comments dropped, block ends as line breaks, tags removed, entities decoded,
// whitespace collapsed. It is for reading, not for rendering.
func htmlToText(s string) string {
	s = reHidden.ReplaceAllString(s, "")
	s = reComment.ReplaceAllString(s, "")
	s = reBreak.ReplaceAllString(s, "\n")
	s = reTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = reSpaces.ReplaceAllString(s, " ")
	lines := strings.Split(normalizeNewlines(s), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSpace(l)
	}
	s = strings.Join(lines, "\n")
	s = reBlankRun.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
