// Package clocktest provides a [Fake] clock for tests. Its time moves only
// when the test calls [Fake.Advance], so code that waits (a lease that
// expires, a retry backoff, a renewal ticker) runs instantly and the same way
// every time.
//
// A test of code that waits has three steps: build the fake and hand it to the
// code, wait until the code has armed its timer ([Fake.WaitForTimers]), then
// advance. None of them sleeps.
package clocktest
