// Package backend holds what every store backend does the same way: checking
// arguments against the rules in package store, encoding messages, and the
// cursor format. Backends use it so that they cannot drift apart on rules
// the conformance suite would only catch one case of.
package backend

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bsamanvith/agentkit/pkg/llm"
	"github.com/bsamanvith/agentkit/pkg/store"
)

// Stored is an entry as a backend keeps it: everything but Seq and CreatedAt
// in a form that is compared byte for byte.
type Stored struct {
	Kind store.Kind
	// Messages is the JSON array the entry's messages marshal to.
	Messages []byte
	// Record is the entry's Record, nil if empty.
	Record []byte
	RunID  string
	Covers int64
}

// Same reports whether two stored entries are identical, which makes an
// Append at a taken position a no-op.
func (s Stored) Same(o Stored) bool {
	return s.Kind == o.Kind && s.RunID == o.RunID && s.Covers == o.Covers &&
		bytes.Equal(s.Messages, o.Messages) && bytes.Equal(s.Record, o.Record)
}

// MessageCount returns how many messages s adds to its conversation's count
// ([store.Conversation.Messages]): those it holds, none for a compaction or
// an interrupt.
func (s Stored) MessageCount() int64 {
	if s.Kind == store.KindCompaction || s.Kind == store.KindInterrupt {
		return 0
	}
	var msgs []json.RawMessage
	if err := json.Unmarshal(s.Messages, &msgs); err != nil {
		return 0 // cannot happen: Messages is what CheckEntry marshaled
	}
	return int64(len(msgs))
}

// CheckConversation checks a conversation record against the rules on
// [store.Conversation]. Errors match [store.ErrInvalid].
func CheckConversation(c store.Conversation) error {
	if err := CheckID("conversation", c.ID); err != nil {
		return err
	}
	if err := CheckText("conversation agent", c.Agent); err != nil {
		return err
	}
	return CheckText("conversation title", c.Title)
}

// Entry decodes s into an entry at seq. The result shares no memory with s.
func (s Stored) Entry(seq int64, created time.Time) (store.Entry, error) {
	var msgs []llm.Message
	if err := json.Unmarshal(s.Messages, &msgs); err != nil {
		return store.Entry{}, fmt.Errorf("stored entry %d: %w", seq, err)
	}
	return store.Entry{
		Seq:       seq,
		Kind:      s.Kind,
		Messages:  msgs,
		Record:    clone(s.Record),
		RunID:     s.RunID,
		Covers:    s.Covers,
		CreatedAt: created,
	}, nil
}

// CheckEntry checks e against the rules on [store.Entry] and encodes it. The
// result shares no memory with e. Errors match [store.ErrInvalid].
func CheckEntry(e store.Entry) (Stored, error) {
	if e.Seq < 1 {
		return Stored{}, fmt.Errorf("%w: entry seq %d is not positive", store.ErrInvalid, e.Seq)
	}
	switch e.Kind {
	case store.KindPrompt, store.KindTurn, store.KindInterrupt:
		if e.Covers != 0 {
			return Stored{}, fmt.Errorf("%w: a %s entry covers nothing, got covers %d", store.ErrInvalid, e.Kind, e.Covers)
		}
	case store.KindCompaction:
		if e.Covers != e.Seq-1 || e.Covers < 1 {
			return Stored{}, fmt.Errorf("%w: compaction at %d must cover %d, got %d", store.ErrInvalid, e.Seq, e.Seq-1, e.Covers)
		}
	default:
		return Stored{}, fmt.Errorf("%w: unknown entry kind %q", store.ErrInvalid, e.Kind)
	}
	if len(e.Messages) == 0 {
		return Stored{}, fmt.Errorf("%w: entry %d has no messages", store.ErrInvalid, e.Seq)
	}
	for i, m := range e.Messages {
		if m.HasUnknown() { // it would marshal, but no read could decode it
			return Stored{}, fmt.Errorf("%w: entry %d: message %d has a block of a newer version", store.ErrInvalid, e.Seq, i)
		}
	}
	msgs, err := json.Marshal(e.Messages)
	if err != nil {
		return Stored{}, fmt.Errorf("%w: entry %d: %w", store.ErrInvalid, e.Seq, err)
	}
	if len(e.Record) > 0 && !json.Valid(e.Record) {
		return Stored{}, fmt.Errorf("%w: entry %d: record is not valid JSON", store.ErrInvalid, e.Seq)
	}
	if err := CheckTextBytes(fmt.Sprintf("entry %d's record", e.Seq), e.Record); err != nil {
		return Stored{}, err
	}
	if err := CheckText(fmt.Sprintf("entry %d's run id", e.Seq), e.RunID); err != nil {
		return Stored{}, err
	}
	return Stored{Kind: e.Kind, Messages: msgs, Record: clone(e.Record), RunID: e.RunID, Covers: e.Covers}, nil
}

// CheckID refuses an empty conversation or run id, and one that is not text
// (see [CheckText]).
func CheckID(what, id string) error {
	if id == "" {
		return fmt.Errorf("%w: empty %s id", store.ErrInvalid, what)
	}
	return CheckText(what+" id", id)
}

// CheckText refuses what a store keeps as text (an id, an owner, an error, a
// pending path, an event type, the JSON of records and event data) if it is
// not UTF-8 or holds a NUL byte, which a Postgres text column cannot hold.
// JSON that [json.Valid] accepts can still be invalid UTF-8. Messages need no
// check: they marshal to UTF-8 or not at all.
func CheckText(what, s string) error {
	return textError(what, utf8.ValidString(s), strings.IndexByte(s, 0) >= 0)
}

// CheckTextBytes is [CheckText] for bytes.
func CheckTextBytes(what string, b []byte) error {
	return textError(what, utf8.Valid(b), bytes.IndexByte(b, 0) >= 0)
}

func textError(what string, valid, nul bool) error {
	switch {
	case !valid:
		return fmt.Errorf("%w: %s is not UTF-8", store.ErrInvalid, what)
	case nul:
		return fmt.Errorf("%w: %s holds a NUL byte", store.ErrInvalid, what)
	}
	return nil
}

// TextOK reports whether s passes [CheckText]. A run looked up by an id that
// does not is not found: it could never have been created.
func TextOK(s string) bool { return CheckText("", s) == nil }

// The range of times a store keeps: what int64 Unix nanoseconds hold, less
// the value at each end, so that a backend can clamp a time a query only
// compares with to just past the range and keep every comparison exact.
var (
	MinTime = time.Unix(0, math.MinInt64+1).UTC()
	MaxTime = time.Unix(0, math.MaxInt64-1).UTC()
)

// CheckTime refuses a time a caller asks the store to keep outside [MinTime,
// MaxTime]. The zero time means unset and is allowed.
func CheckTime(what string, t time.Time) error {
	if t.IsZero() || (!t.Before(MinTime) && !t.After(MaxTime)) {
		return nil
	}
	return fmt.Errorf("%w: %s %v is outside what the store can hold (%v to %v)", store.ErrInvalid, what, t, MinTime, MaxTime)
}

// CheckLease refuses an empty owner or a time to live that is not positive.
func CheckLease(owner string, ttl time.Duration) error {
	if err := CheckOwner(owner); err != nil {
		return err
	}
	if ttl <= 0 {
		return fmt.Errorf("%w: lease ttl %v is not positive", store.ErrInvalid, ttl)
	}
	return nil
}

// CheckOwner refuses an empty lease owner or one that is not text.
func CheckOwner(owner string) error {
	if owner == "" {
		return fmt.Errorf("%w: empty lease owner", store.ErrInvalid)
	}
	return CheckText("lease owner", owner)
}

// CheckTransition refuses a move [store.Status.CanMoveTo] does not allow and
// details that do not fit the new status. It returns the pending paths to
// store: sorted, sharing no memory with d.
func CheckTransition(from, to store.Status, d store.Details) ([]string, error) {
	if !from.CanMoveTo(to) {
		return nil, fmt.Errorf("%w: a run cannot move from %q to %q", store.ErrInvalid, from, to)
	}
	bad := func(field string) error {
		return fmt.Errorf("%w: %s does not apply to a %s run", store.ErrInvalid, field, to)
	}
	if d.Error != "" && to != store.StatusFailed && to != store.StatusRejected && to != store.StatusCanceled {
		return nil, bad("an error")
	}
	if err := CheckText("the error", d.Error); err != nil {
		return nil, err
	}
	if to != store.StatusFailed && (d.Retryable || !d.NextAttemptAt.IsZero()) {
		return nil, bad("a retry")
	}
	if !d.Retryable && !d.NextAttemptAt.IsZero() {
		return nil, fmt.Errorf("%w: a next attempt time needs Retryable", store.ErrInvalid)
	}
	if to != store.StatusSuspended {
		if len(d.Pending) > 0 {
			return nil, bad("pending paths")
		}
		return nil, nil
	}
	if len(d.Pending) == 0 {
		return nil, fmt.Errorf("%w: a suspended run needs its pending paths", store.ErrInvalid)
	}
	pending := slices.Clone(d.Pending)
	slices.Sort(pending)
	if pending[0] == "" {
		return nil, fmt.Errorf("%w: empty pending path", store.ErrInvalid)
	}
	for _, p := range pending {
		if err := CheckText("a pending path", p); err != nil {
			return nil, err
		}
	}
	return pending, nil
}

// Held reports whether l is held at now: it has an owner and has not expired.
func Held(l store.Lease, now time.Time) bool {
	return l.Owner != "" && now.Before(l.Expires)
}

// Resumable reports whether r should continue at now: a background run
// whose start time has come, running with no lease held, or failed,
// retryable and due. An interactive run never is.
func Resumable(r store.Run, now time.Time) bool {
	if r.Interactive || r.StartAt.After(now) {
		return false
	}
	switch r.Status {
	case store.StatusRunning:
		return !Held(r.Lease, now)
	case store.StatusFailed:
		return r.Retryable && !r.NextAttemptAt.After(now)
	}
	return false
}

// Queued reports whether r is in the queue [store.Admin.ClaimNext] takes
// from, due or not: a background run that is running, or failed and
// retryable. It is the predicate of the backends' queue index.
func Queued(r store.Run) bool {
	return !r.Interactive && (r.Status == store.StatusRunning || (r.Status == store.StatusFailed && r.Retryable))
}

// Claimable reports whether ClaimNext may take r at now: queued and due
// ([store.Run.Due] at or before now).
func Claimable(r store.Run, now time.Time) bool {
	return Queued(r) && !r.Due().After(now)
}

// ClaimOrder compares two claimable runs, each with its tenant id, in the
// order ClaimNext takes them: highest Priority, then earliest Due, then
// tenant id and run ID as bytes.
func ClaimOrder(at string, a store.Run, bt string, b store.Run) int {
	if a.Priority != b.Priority {
		if a.Priority > b.Priority {
			return -1
		}
		return 1
	}
	if c := a.Due().Compare(b.Due()); c != 0 {
		return c
	}
	if c := strings.Compare(at, bt); c != 0 {
		return c
	}
	return strings.Compare(a.ID, b.ID)
}

// Created is what a Create stores beside the run's ids and status, from its
// options and the time it is created.
type Created struct {
	Start time.Time
	Lease store.Lease
}

// CheckCreate checks the options of a Create made at created against the
// rules on [store.CreateOptions] and [store.RunStore.Create], and returns
// the start time to store (o.StartAt, or created when that is zero) and the
// lease (none without [store.WithLease]).
func CheckCreate(o store.CreateOptions, created time.Time) (Created, error) {
	if err := CheckText("lane", o.Lane); err != nil {
		return Created{}, err
	}
	if err := CheckText("pool", o.Pool); err != nil {
		return Created{}, err
	}
	if o.Priority < store.MinPriority || o.Priority > store.MaxPriority {
		return Created{}, fmt.Errorf("%w: priority %d is outside %d to %d", store.ErrInvalid, o.Priority, store.MinPriority, store.MaxPriority)
	}
	c := Created{Start: created}
	if !o.StartAt.IsZero() {
		if err := CheckTime("start time", o.StartAt); err != nil {
			return Created{}, err
		}
		c.Start = o.StartAt.Round(0).UTC()
	}
	if o.LeaseOwner != "" || o.LeaseTTL != 0 {
		if err := CheckLease(o.LeaseOwner, o.LeaseTTL); err != nil {
			return Created{}, err
		}
		c.Lease = store.Lease{Owner: o.LeaseOwner, Expires: created.Add(o.LeaseTTL)}
		if err := CheckTime("lease expiry", c.Lease.Expires); err != nil {
			return Created{}, err
		}
	}
	return c, nil
}

// CheckClaimNext checks the arguments of [store.Admin.ClaimNext].
func CheckClaimNext(lanes []string, owner string, ttl time.Duration) error {
	if len(lanes) == 0 {
		return fmt.Errorf("%w: no lanes to claim from", store.ErrInvalid)
	}
	for _, l := range lanes {
		if err := CheckText("lane", l); err != nil {
			return err
		}
	}
	return CheckLease(owner, ttl)
}

// Limit returns the page size p asks for.
func Limit(p store.Page) (int, error) {
	switch {
	case p.Limit < 0:
		return 0, fmt.Errorf("%w: negative page limit %d", store.ErrInvalid, p.Limit)
	case p.Limit == 0:
		return store.DefaultPageLimit, nil
	case p.Limit > store.MaxPageLimit:
		return store.MaxPageLimit, nil
	}
	return p.Limit, nil
}

// HistoryStart is where a History page starts.
type HistoryStart struct {
	// After is set when the page continues from a cursor: it starts just
	// past this sequence number in the page's order.
	After    int64
	HasAfter bool
	// FromSeq and FromTime are the page's seek, when it has no cursor.
	FromSeq  int64
	FromTime time.Time
}

// ParseHistoryPage checks p for History and returns its limit and start.
func ParseHistoryPage(p store.Page) (int, HistoryStart, error) {
	limit, err := Limit(p)
	if err != nil {
		return 0, HistoryStart{}, err
	}
	if err := checkOrder(p.Order); err != nil {
		return 0, HistoryStart{}, err
	}
	if p.FromSeq < 0 {
		return 0, HistoryStart{}, fmt.Errorf("%w: negative seek %d", store.ErrInvalid, p.FromSeq)
	}
	if p.FromSeq != 0 && !p.FromTime.IsZero() {
		return 0, HistoryStart{}, fmt.Errorf("%w: seek by sequence number or by time, not both", store.ErrInvalid)
	}
	if p.Cursor == "" {
		return limit, HistoryStart{FromSeq: p.FromSeq, FromTime: p.FromTime}, nil
	}
	if p.FromSeq != 0 || !p.FromTime.IsZero() {
		return 0, HistoryStart{}, fmt.Errorf("%w: a page continues from a cursor or seeks, not both", store.ErrInvalid)
	}
	fields, err := decodeCursor(p.Cursor, "h", p.Order, 1)
	if err != nil {
		return 0, HistoryStart{}, err
	}
	seq, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || seq < 1 {
		return 0, HistoryStart{}, fmt.Errorf("%w: bad cursor", store.ErrInvalid)
	}
	return limit, HistoryStart{After: seq, HasAfter: true}, nil
}

// HistoryCursor returns the cursor of the page after one that ended at seq.
func HistoryCursor(o store.Order, seq int64) string {
	return encodeCursor("h", o, strconv.FormatInt(seq, 10))
}

// RunKey is a run's position in [store.RunStore.List]: runs are ordered by
// CreatedAt and then ID. Conversation records are ordered the same way.
type RunKey struct {
	Created time.Time
	ID      string
}

// Less reports whether k comes before o in Ascending order.
func (k RunKey) Less(o RunKey) bool {
	if !k.Created.Equal(o.Created) {
		return k.Created.Before(o.Created)
	}
	return k.ID < o.ID
}

// RunStart is where a List page starts.
type RunStart struct {
	// After is set when the page continues from a cursor: it starts just
	// past this key in the page's order.
	After    RunKey
	HasAfter bool
	FromTime time.Time
}

// ParseRunPage checks p for List and returns its limit and start.
func ParseRunPage(p store.Page) (int, RunStart, error) { return parseKeyPage(p, "r", "runs") }

// ParseConversationPage checks p for ListConversations, which orders records
// as List orders runs, and returns its limit and start.
func ParseConversationPage(p store.Page) (int, RunStart, error) {
	return parseKeyPage(p, "c", "conversations")
}

func parseKeyPage(p store.Page, list, what string) (int, RunStart, error) {
	limit, err := Limit(p)
	if err != nil {
		return 0, RunStart{}, err
	}
	if err := checkOrder(p.Order); err != nil {
		return 0, RunStart{}, err
	}
	if p.FromSeq != 0 {
		return 0, RunStart{}, fmt.Errorf("%w: %s have no sequence numbers to seek", store.ErrInvalid, what)
	}
	if p.Cursor == "" {
		return limit, RunStart{FromTime: p.FromTime}, nil
	}
	if !p.FromTime.IsZero() {
		return 0, RunStart{}, fmt.Errorf("%w: a page continues from a cursor or seeks, not both", store.ErrInvalid)
	}
	fields, err := decodeCursor(p.Cursor, list, p.Order, 2)
	if err != nil {
		return 0, RunStart{}, err
	}
	nanos, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, RunStart{}, fmt.Errorf("%w: bad cursor", store.ErrInvalid)
	}
	return limit, RunStart{After: RunKey{Created: time.Unix(0, nanos).UTC(), ID: fields[1]}, HasAfter: true}, nil
}

// RunCursor returns the cursor of the page after one that ended at k.
func RunCursor(o store.Order, k RunKey) string { return keyCursor("r", o, k) }

// ConversationCursor returns the cursor of the ListConversations page after
// one that ended at k.
func ConversationCursor(o store.Order, k RunKey) string { return keyCursor("c", o, k) }

func keyCursor(list string, o store.Order, k RunKey) string {
	return encodeCursor(list, o, strconv.FormatInt(k.Created.UnixNano(), 10), k.ID)
}

func checkOrder(o store.Order) error {
	if o != store.Ascending && o != store.Descending {
		return fmt.Errorf("%w: unknown order %d", store.ErrInvalid, o)
	}
	return nil
}

// A cursor is "<list>.<order>.<field>..." in unpadded URL-safe base64. Fields
// are base64 too, so ids may hold any byte.
func encodeCursor(list string, o store.Order, fields ...string) string {
	parts := []string{list, strconv.Itoa(int(o))}
	for _, f := range fields {
		parts = append(parts, base64.RawURLEncoding.EncodeToString([]byte(f)))
	}
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Join(parts, ".")))
}

func decodeCursor(c, list string, o store.Order, n int) ([]string, error) {
	bad := fmt.Errorf("%w: bad cursor", store.ErrInvalid)
	raw, err := base64.RawURLEncoding.DecodeString(c)
	if err != nil {
		return nil, bad
	}
	parts := strings.Split(string(raw), ".")
	if len(parts) != n+2 || parts[0] != list {
		return nil, bad
	}
	if parts[1] != strconv.Itoa(int(o)) {
		return nil, fmt.Errorf("%w: cursor is for the other order", store.ErrInvalid)
	}
	fields := slices.Clone(parts[2:])
	for i, f := range fields {
		b, err := base64.RawURLEncoding.DecodeString(f)
		if err != nil {
			return nil, bad
		}
		fields[i] = string(b)
	}
	return fields, nil
}

func clone(b []byte) []byte {
	if len(b) == 0 {
		return nil
	}
	return slices.Clone(b)
}
