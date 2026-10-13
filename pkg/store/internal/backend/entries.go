package backend

import (
	"fmt"

	"github.com/bsamanvith/agentkit/pkg/store"
)

// CheckEntries checks a batch for [store.ConversationStore.AppendBatch]:
// each entry by [CheckEntry], and their Seqs consecutive. It returns the
// encoded entries and the first Seq (0 for no entries). The result shares no
// memory with entries. Errors match [store.ErrInvalid].
func CheckEntries(entries []store.Entry) ([]Stored, int64, error) {
	out := make([]Stored, len(entries))
	for i, e := range entries {
		if i > 0 && e.Seq != entries[i-1].Seq+1 {
			return nil, 0, fmt.Errorf("%w: entry %d follows entry %d; a batch's seqs are consecutive", store.ErrInvalid, e.Seq, entries[i-1].Seq)
		}
		st, err := CheckEntry(e)
		if err != nil {
			return nil, 0, err
		}
		out[i] = st
	}
	if len(entries) == 0 {
		return out, 0, nil
	}
	return out, entries[0].Seq, nil
}

// NewEntries decides a batch that [CheckEntries] accepted, starting at
// first, against its conversation's log. last is the log's last Seq, and
// taken holds the stored entries at the batch's positions up to last, in
// order (none if the batch starts after last). It returns the entries to
// write, which start at last+1, or [store.ErrGap] or [store.ErrConflict].
func NewEntries(conv string, first int64, batch []Stored, last int64, taken []Stored) ([]Stored, error) {
	if len(batch) == 0 {
		return nil, nil
	}
	if first > last+1 {
		return nil, fmt.Errorf("%w: conversation %q has %d entries, got entry %d", store.ErrGap, conv, last, first)
	}
	n := int(max(min(last-first+1, int64(len(batch))), 0))
	if len(taken) != n {
		return nil, fmt.Errorf("conversation %q: read %d stored entries at %d, want %d", conv, len(taken), first, n)
	}
	for i, st := range batch[:n] {
		if !st.Same(taken[i]) {
			return nil, fmt.Errorf("%w: conversation %q already has a different entry %d", store.ErrConflict, conv, first+int64(i))
		}
	}
	return batch[n:], nil
}
