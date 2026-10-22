//go:build noagui

package main

import "testing"

// aguiSays is what serve says of any agui setup in a build without the
// component: that it is not built, naming the tag.
func aguiSays(string) string {
	return "agui: " + envComponents + ": this agentkit is built without the AG-UI server (-tags noagui)"
}

// TestAGUINotBuilt: an image without AG-UI (runner, toolsets) refuses the
// agui component at startup and names the tag. Run with -tags noagui.
func TestAGUINotBuilt(t *testing.T) {
	t.Parallel()
	_, err := newAGUI(nil, nil)
	const want = envComponents + ": this agentkit is built without the AG-UI server (-tags noagui); use an image that has it"
	if err == nil || err.Error() != want {
		t.Fatalf("newAGUI: %v, want %q", err, want)
	}
}
