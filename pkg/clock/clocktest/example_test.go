package clocktest_test

import (
	"fmt"
	"time"

	"github.com/bsamanvith/agentkit/pkg/clock/clocktest"
)

// A ticker fires once for every period that passes, and a timer fires once, at
// its deadline.
func ExampleFake() {
	fake := clocktest.NewFake(time.Time{})
	begin := fake.Now()
	ticker := fake.NewTicker(10 * time.Second)
	defer ticker.Stop()
	timer := fake.NewTimer(25 * time.Second)

	for range 6 {
		fake.Advance(5 * time.Second)
		select {
		case at := <-ticker.C():
			fmt.Println("tick at", at.Sub(begin))
		default:
		}
		select {
		case at := <-timer.C():
			fmt.Println("timer at", at.Sub(begin))
		default:
		}
	}
	// Output:
	// tick at 10s
	// tick at 20s
	// timer at 25s
	// tick at 30s
}
