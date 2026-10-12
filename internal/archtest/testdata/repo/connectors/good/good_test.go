package good_test

import (
	"testing"

	kit "example.com/m/pkg/toolset/toolsettest"
)

func TestConformance(t *testing.T) { kit.RunConnector(t, kit.Connector{}) }

func FuzzResponses(f *testing.F) { kit.FuzzResponses(f, kit.Connector{}, "good_search") }
