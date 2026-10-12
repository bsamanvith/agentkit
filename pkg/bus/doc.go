// Package bus carries live messages between processes: a run's events to the
// replicas serving its watchers, commands to the replica running a run, and a
// user's notifications.
//
// A [Bus] keeps a short, trimmed history per [Topic] and delivers it in order
// from any point a subscriber names, then live. It is never the only copy of
// anything: the store is the source of truth, and a subscriber whose history
// was trimmed or lost gets [ErrGap] and reloads from the store instead of
// silently missing messages (E18).
//
// Message IDs are chosen by the publisher (a run's sequence numbers), so a
// reader that replays from the store and then tails the bus can dedupe by ID.
// Each stored message remembers the ID of the one before it, which is how a
// gap is told from a quiet topic.
//
// Backends: [github.com/bsamanvith/agentkit/pkg/bus/memory] for one process
// and tests, and [github.com/bsamanvith/agentkit/pkg/bus/redis] (Redis
// Streams) across replicas. Both pass [github.com/bsamanvith/agentkit/pkg/bus/bustest.Run].
package bus
