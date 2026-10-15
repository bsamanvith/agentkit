// Package stream carries a run's events, a user's notifications and the
// commands to a running run across replicas (E18, T090). It joins the store,
// which is the source of truth, and the bus, which is the fast and lossy way
// between processes.
//
// # A run's events
//
// The process running a run writes its events through a [RunStream] (from
// [Publisher.Open]). Each event has an ID ([bus.ID]) that orders all of a
// run's events:
//
//   - a durable event (run_start, tool_call, tool_result, turn_done, an
//     interrupt, the run's end) is the run's next [store.Event] at Seq S and
//     has the ID "S". It is appended to the store's event log first, then
//     published on the run's topic;
//   - a live event (a text delta) after durable event S has the ID "S.n",
//     n counting from 1 until the next durable event. It is only published.
//
// A failed publish is logged and left to the readers: the bus reports the
// hole as [bus.ErrGap], and a reader fills it from the log. A process that
// takes a run over republishes the stored events the bus lacks, so a hole
// left by a crash between the store and the publish is filled too.
//
// [Watch] reads a run's events from any replica: it replays the stored events
// after the ID a client last saw (its Last-Event-ID), then follows the bus
// from where the replay ended. Nothing is delivered twice and nothing durable
// is skipped; on a gap (history trimmed, Redis restarted) it backfills from
// the log. Live events lost in a gap stay lost: the durable events carry
// everything (turn_done has the turn's full text). It ends after the event
// its terminal function accepts (run_done, run_error).
//
// # Notifications
//
// A [Notifier] publishes on a user's topic (tenant and user scoped, so a
// notification reaches nobody else): a run or task changed status, an
// interrupt waits, a conversation was updated. [WatchUser] follows them, and
// resumes after an ID as long as the bus still has it. Notifications are not
// stored: after a gap a watcher gets a [TypeResync] event, and the client
// refetches its lists.
//
// # Commands
//
// [Commands.Send] stores a command (steering, an interrupt's answer, a
// cancel) in the store's [store.CommandLog], then signals the run's command
// topic. The process holding the run's lease reads them through an [Inbox],
// which wakes on the signal and reads the store at turn boundaries; a process
// taking a run over finds the commands still pending in the store.
package stream
