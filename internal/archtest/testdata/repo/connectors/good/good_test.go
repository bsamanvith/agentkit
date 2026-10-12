package good_test

import (
	"testing"

	kit "example.com/m/pkg/toolset/toolsettest"
)

func TestConformance(t *testing.T) { kit.RunConnector(t, kit.Connector{}) }

// Not a Fuzz target, so scripts/fuzz.sh does not try to run this fixture.
func fuzzResponses(f *testing.F) { kit.FuzzResponses(f, kit.Connector{}, "good_search") }
