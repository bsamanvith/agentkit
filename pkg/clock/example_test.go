package clock_test

import (
	"fmt"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock"
	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

// lease is held until a deadline that is measured on a Clock.
type lease struct {
	clock   clock.Clock
	expires time.Time
}

func (l lease) held() bool { return l.clock.Now().Before(l.expires) }

// Code that takes a Clock can be tested without waiting: the fake clock moves
// only when told to. Production code passes clock.Real() instead.
func Example() {
	fake := clocktest.NewFake(time.Time{})
	l := lease{clock: fake, expires: fake.Now().Add(30 * time.Second)}

	fmt.Println("held at start:", l.held())
	fake.Advance(29 * time.Second)
	fmt.Println("held after 29s:", l.held())
	fake.Advance(time.Second)
	fmt.Println("held after 30s:", l.held())
	// Output:
	// held at start: true
	// held after 29s: true
	// held after 30s: false
}
