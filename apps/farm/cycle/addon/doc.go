// Package addon has what other features add to a plot's crop cycles: the
// inputs plan (what to buy, feeding and sprays, from package inputs) and
// sell timing (package prices), each a [cycle.Addon], and the inputs
// schedule's tasks on the season roadmap, a [work.TaskSource]; crop
// insurance (package schemes) is both.
//
// They are picked in one place, the farm binary (apps/farm/cmd/farm), like
// every other feature; each is optional, and none ever fails the line or
// the roadmap: an addon with nothing to say returns nothing, and one that
// fails becomes a note.
//
// Numbers come from the rules (inputs) and the price history (prices),
// never from a model: the assistant reads them through its tools.
//
// See apps/farm/docs/CYCLES.md.
package addon
