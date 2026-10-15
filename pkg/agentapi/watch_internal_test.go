package agentapi

import (
	"slices"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/bus"
)

func toolCall(seq uint64) Event {
	return withID(Event{V: WireVersion, Type: TypeToolCall, RunID: "run_1"}, bus.ID{Seq: seq})
}

// TestSharedWatchSlowViewer: a viewer that stops reading is dropped when its
// buffer is full, the others get every event, in order, and the dropped one
// joins again from the watch's memory, missing nothing.
func TestSharedWatchSlowViewer(t *testing.T) {
	t.Parallel()
	const n, buffer = 10, 2
	w := newSharedWatch(hubKey{"t", "run_1"}, buffer, bus.ID{})
	fast, _, _, _ := w.join()
	slow, _, _, _ := w.join()
	got := make(chan string)
	go func() {
		for e := range fast.ch {
			got <- e.ID
		}
		close(got)
	}()
	var want, ids []string
	for i := range uint64(n) {
		e := toolCall(i + 1)
		if i == n-1 {
			e.Type = TypeRunDone
		}
		w.publish(e)
		want = append(want, e.ID)
		ids = append(ids, <-got) // the fast viewer has it before the next
	}
	w.end()
	if _, open := <-got; open || !slices.Equal(ids, want) {
		t.Errorf("the viewer that reads got %v, want %v, then its end", ids, want)
	}
	if !w.wasDropped(slow) {
		t.Fatal("the viewer that stopped reading was not dropped")
	}
	var buffered []string
	for e := range slow.ch {
		buffered = append(buffered, e.ID)
	}
	if len(buffered) != buffer {
		t.Errorf("the dropped viewer's channel held %v, want the first %d events", buffered, buffer)
	}

	// Back, after the run's end: the ring has it all.
	back, start, ring, ok := w.join()
	if !ok {
		t.Fatal("joining the watch again failed")
	}
	if _, open := <-back.ch; open || start != (bus.ID{}) || len(ring) != n {
		t.Errorf("joined again: ring from %v with %d events (channel open %v), want the %d events from the start, channel closed", start, len(ring), open, n)
	}
}

// TestSharedWatchStopsWithLastViewer: a viewer dropped for falling behind is
// still one until it leaves, so the watch is not stopped while it joins
// again; it is stopped once, by the last one to leave.
func TestSharedWatchStopsWithLastViewer(t *testing.T) {
	t.Parallel()
	w := newSharedWatch(hubKey{"t", "run_1"}, 0, bus.ID{})
	a, _, _, _ := w.join()
	b, _, _, _ := w.join()
	w.publish(toolCall(1)) // nobody waits: both dropped
	if !w.wasDropped(a) || !w.wasDropped(b) {
		t.Fatal("viewers not waiting were not dropped from an unbuffered watch")
	}
	a2, _, _, ok := w.join()
	if !ok || w.leave(a) {
		t.Fatal("a viewer joining again after it was dropped stopped the watch")
	}
	if w.leave(b) {
		t.Fatal("the watch stopped with a viewer left")
	}
	if !w.leave(a2) {
		t.Fatal("the last viewer leaving did not stop the watch")
	}
	if w.leave(a2) {
		t.Error("the watch was stopped twice")
	}
	if _, _, _, ok := w.join(); ok {
		t.Error("a viewer joined a watch that was stopped before the run's end")
	}
}
