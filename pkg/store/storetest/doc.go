// Package storetest provides the conformance suites every store backend runs,
// so the in-memory store, SQLite and any later backend behave the same:
//
//	func TestConformance(t *testing.T) {
//		newStore := func(t *testing.T, clk clock.Clock) *mybackend.Store {
//			return mybackend.Open(t.TempDir(), mybackend.WithClock(clk))
//		}
//		storetest.RunConversationStore(t, func(t *testing.T, clk clock.Clock) store.ConversationStore { return newStore(t, clk) })
//		storetest.RunRunStore(t, func(t *testing.T, clk clock.Clock) store.RunStore { return newStore(t, clk) })
//		storetest.RunCheckpointer(t, func(t *testing.T) flow.Checkpointer { return newStore(t, clock.Real()) })
//	}
//
// The factories get a fake clock and must make the store read every time it
// records (entry and run times, lease expiry) from it.
//
// Every test goes through one helper per interface that, when the test ends,
// reads back everything the test touched, through every tenant it used plus
// one that never wrote, and checks the invariants: for conversations, entries
// numbered densely from 1, every entry reading back byte for byte as it was
// first accepted, times that never go backwards, History the same in both
// orders, and Context a suffix of History starting at the latest compaction;
// for runs, each tenant seeing exactly its own runs, records matching the
// transitions the store accepted, List covering each conversation's runs once
// in order, at most one unfinished run per conversation, and ListSuspended and
// ListResumable returning exactly the runs the rules say. Seeded simulations
// (SimulateConversations, SimulateRuns) check every result against a model; a
// backend can also run them from a
// fuzz target.
package storetest
