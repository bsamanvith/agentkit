package imposter_test

import (
	"testing"

	"example.com/other/toolsettest"
)

// Calls a RunConnector, but not the kit's.
func TestConformance(t *testing.T) { toolsettest.RunConnector(t) }

// Not a Fuzz target, so scripts/fuzz.sh does not try to run this fixture.
func fuzzResponses(f *testing.F) { toolsettest.FuzzResponses(f) }
