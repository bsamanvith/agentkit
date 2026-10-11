// Package clock is the time that agentkit code reads. Code that depends on
// the time (lease expiry, renewal, retry backoff, rate limits, timestamps)
// takes a [Clock] instead of calling the time package, so a test can control
// time instead of waiting for it.
//
// Production code passes [Real], which forwards to the time package and adds
// no behavior. Tests pass [clocktest.Fake], whose time moves only when the
// test calls Advance.
//
// [clocktest.Fake]: https://pkg.go.dev/github.com/bsamanvith/agentkit/pkg/clock/clocktest#Fake
package clock
