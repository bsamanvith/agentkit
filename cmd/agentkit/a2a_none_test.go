//go:build noa2a

package main

import "testing"

// a2aSays is what serve says of any a2a setup in a build without the
// component: that it is not built, naming the tag.
func a2aSays(string) string {
	return "a2a: " + envComponents + ": this agentkit is built without the A2A server (-tags noa2a)"
}

// TestA2ANotBuilt: an image without A2A (runner, toolsets) refuses the a2a
// component at startup and names the tag. Run with -tags noa2a.
func TestA2ANotBuilt(t *testing.T) {
	t.Parallel()
	_, err := cli{}.newA2A(nil, serveConfig{}, nil, nil)
	const want = envComponents + ": this agentkit is built without the A2A server (-tags noa2a); use an image that has it"
	if err == nil || err.Error() != want {
		t.Fatalf("newA2A: %v, want %q", err, want)
	}
}
