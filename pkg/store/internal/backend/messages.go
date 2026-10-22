package backend

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bsamanvith/agentkit/pkg/store"
)

// CheckMessage checks m for [store.MessageLog.SendMessage] against the rules
// on [store.Message], except that a reply's InReplyTo is in its thread,
// which the backend checks. It returns the fields SendMessage keeps, with
// empty Data as nil and sharing no memory with m. Errors match
// [store.ErrInvalid].
func CheckMessage(m store.Message) (store.Message, error) {
	for _, id := range []struct{ what, v string }{{"message", m.ID}, {"thread", m.ThreadID}, {"recipient conversation", m.ToConversation}} {
		if err := CheckID(id.what, id.v); err != nil {
			return store.Message{}, err
		}
	}
	if m.From == "" || m.To == "" {
		return store.Message{}, fmt.Errorf("%w: a message needs a sender and a recipient", store.ErrInvalid)
	}
	for _, f := range []struct{ what, v string }{
		{"sender", m.From},
		{"recipient", m.To},
		{"sender conversation", m.FromConversation},
		{"message text", m.Text},
		{"in_reply_to", m.InReplyTo},
	} {
		if err := CheckText(f.what, f.v); err != nil {
			return store.Message{}, err
		}
	}
	if !m.Kind.Valid() {
		return store.Message{}, fmt.Errorf("%w: message kind %q is none of request, reply, notice", store.ErrInvalid, m.Kind)
	}
	if (m.Kind == store.MessageReply) != (m.InReplyTo != "") {
		return store.Message{}, fmt.Errorf("%w: a reply, and only a reply, names the message it answers", store.ErrInvalid)
	}
	if len(m.Data) > 0 && !json.Valid(m.Data) {
		return store.Message{}, fmt.Errorf("%w: message data is not valid JSON", store.ErrInvalid)
	}
	if err := CheckTextBytes("message data", m.Data); err != nil {
		return store.Message{}, err
	}
	return store.Message{
		ID: m.ID, ThreadID: m.ThreadID, From: m.From, To: m.To,
		FromConversation: m.FromConversation, ToConversation: m.ToConversation,
		Kind: m.Kind, Text: m.Text, Data: clone(m.Data), InReplyTo: m.InReplyTo,
	}, nil
}

// SameMessage reports whether a sent message is the stored one, which makes
// sending it again a no-op: every field a sender sets.
func SameMessage(a, b store.Message) bool {
	return a.ID == b.ID && a.ThreadID == b.ThreadID && a.From == b.From && a.To == b.To &&
		a.FromConversation == b.FromConversation && a.ToConversation == b.ToConversation &&
		a.Kind == b.Kind && a.Text == b.Text && bytes.Equal(a.Data, b.Data) && a.InReplyTo == b.InReplyTo
}

// SentMessage decides a send of m (checked) whose ID the tenant has as
// stored: the stored one, or [store.ErrConflict].
func SentMessage(m, stored store.Message) (store.Message, error) {
	if !SameMessage(m, stored) {
		return store.Message{}, fmt.Errorf("%w: message %q is stored with other content", store.ErrConflict, m.ID)
	}
	return stored, nil
}

// SendToThreadError is why a thread th refuses a message of kind k, or nil.
func SendToThreadError(th store.Thread, k store.MessageKind) error {
	if th.Status == store.ThreadStopped && k != store.MessageNotice {
		return fmt.Errorf("%w: thread %q is stopped (%s); it takes notices only", store.ErrFinal, th.ID, th.StopCode)
	}
	return nil
}

// CheckStop checks the code and reason of [store.MessageLog.StopThread].
func CheckStop(code, reason string) error {
	if code == "" {
		return fmt.Errorf("%w: a stop needs a code", store.ErrInvalid)
	}
	if err := CheckText("stop code", code); err != nil {
		return err
	}
	return CheckText("stop reason", reason)
}

// MessageDue reports whether [store.MessageAdmin.ClaimDueMessage] may take m
// at now; blocked says that a message of its thread to the same
// conversation with a lower Hop is queued.
func MessageDue(m store.Message, now time.Time, blocked bool) bool {
	if m.State == store.MessageDone || Held(m.Lease, now) || m.NextAttemptAt.After(now) {
		return false
	}
	return m.State != store.MessageQueued || !blocked
}

// MessageOrder compares two due messages, each with its tenant id, in the
// order ClaimDueMessage takes them: earliest NextAttemptAt (zero first),
// then CreatedAt, then tenant id and message ID as bytes.
func MessageOrder(at string, a store.Message, bt string, b store.Message) int {
	if c := a.NextAttemptAt.Compare(b.NextAttemptAt); c != 0 {
		return c
	}
	if c := a.CreatedAt.Compare(b.CreatedAt); c != 0 {
		return c
	}
	if c := strings.Compare(at, bt); c != 0 {
		return c
	}
	return strings.Compare(a.ID, b.ID)
}

// ClaimMessageError is why owner cannot claim m at now, or nil.
func ClaimMessageError(m store.Message, owner string, now time.Time) error {
	switch {
	case m.State == store.MessageDone:
		return fmt.Errorf("%w: message %q is done", store.ErrFinal, m.ID)
	case Held(m.Lease, now) && m.Lease.Owner != owner:
		return fmt.Errorf("%w: message %q", store.ErrLeased, m.ID)
	}
	return nil
}

// Claimed is m claimed by owner until expires at now.
func Claimed(m store.Message, owner string, now, expires time.Time) store.Message {
	m.Lease = store.Lease{Owner: owner, Expires: expires}
	m.Attempts++
	if m.DeliveredAt.IsZero() {
		m.DeliveredAt = now
	}
	return m
}

// CheckMessageUpdate checks u for [store.MessageLog.UpdateMessage] on its
// own.
func CheckMessageUpdate(u store.MessageUpdate) error {
	switch {
	case !u.State.Valid():
		return fmt.Errorf("%w: message state %q is none of queued, running, done", store.ErrInvalid, u.State)
	case u.Tokens < 0:
		return fmt.Errorf("%w: negative tokens %d", store.ErrInvalid, u.Tokens)
	}
	if err := CheckTime("next attempt", u.NextAttemptAt); err != nil {
		return err
	}
	return CheckText("run id", u.RunID)
}

// UpdateMessageError is why owner cannot record u on m at now, or nil.
func UpdateMessageError(m store.Message, owner string, u store.MessageUpdate, now time.Time) error {
	switch {
	case m.State == store.MessageDone:
		return fmt.Errorf("%w: message %q is done", store.ErrFinal, m.ID)
	case Held(m.Lease, now) && m.Lease.Owner != owner:
		return fmt.Errorf("%w: message %q", store.ErrLeased, m.ID)
	case !Held(m.Lease, now) || m.Lease.Owner != owner:
		return fmt.Errorf("%w: message %q", store.ErrLeaseLost, m.ID)
	case m.State != store.MessageQueued && u.State == store.MessageQueued:
		return fmt.Errorf("%w: message %q was processed; it never goes back to queued", store.ErrInvalid, m.ID)
	case u.RunID != "" && m.RunID != "" && u.RunID != m.RunID:
		return fmt.Errorf("%w: message %q was processed by run %q", store.ErrConflict, m.ID, m.RunID)
	}
	return nil
}

// Updated is m with u recorded at now, its lease released.
func Updated(m store.Message, u store.MessageUpdate, now time.Time) store.Message {
	if m.State == store.MessageQueued && u.State != store.MessageQueued {
		m.ProcessedAt = now
	}
	m.State = u.State
	if u.RunID != "" {
		m.RunID = u.RunID
	}
	m.NextAttemptAt = u.NextAttemptAt.UTC().Round(0)
	if u.State == store.MessageDone {
		m.NextAttemptAt = time.Time{}
	}
	m.Lease = store.Lease{}
	return m
}

// ParseThreadPage checks p for ListThreads, which orders threads as List
// orders runs, and returns its limit and start.
func ParseThreadPage(p store.Page) (int, RunStart, error) { return parseKeyPage(p, "t", "threads") }

// ThreadCursor returns the cursor of the ListThreads page after one that
// ended at k.
func ThreadCursor(o store.Order, k RunKey) string { return keyCursor("t", o, k) }

// CheckThreadMessages checks the arguments of ThreadMessages and returns its
// limit.
func CheckThreadMessages(after, limit int) (int, error) {
	if after < 0 {
		return 0, fmt.Errorf("%w: negative hop %d", store.ErrInvalid, after)
	}
	return Limit(store.Page{Limit: limit})
}
