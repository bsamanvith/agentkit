package imposter_test

import (
	"testing"

	"example.com/other/toolsettest"
)

// Calls a RunConnector, but not the kit's.
func TestConformance(t *testing.T) { toolsettest.RunConnector(t) }

func FuzzResponses(f *testing.F) { toolsettest.FuzzResponses(f) }
