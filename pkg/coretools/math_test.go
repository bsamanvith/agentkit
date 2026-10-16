package coretools_test

import (
	"testing"
)

func TestMath(t *testing.T) {
	reg := build(t, `{"name": "math", "type": "math"}`)
	for _, c := range []struct {
		tool, input string
		want        float64
	}{
		{"add", `{"numbers": [17, 23, 2]}`, 42},
		{"add", `{"numbers": [0.1, 0.2]}`, 0.3}, // no floating-point noise
		{"subtract", `{"a": 10, "b": 25}`, -15},
		{"multiply", `{"numbers": [17, 23]}`, 391},
		{"multiply", `{"numbers": [1.1, 3]}`, 3.3},
		{"divide", `{"a": 1, "b": 3}`, 0.333333333333333},
		{"modulo", `{"a": -7, "b": 3}`, -1},
		{"power", `{"base": 2, "exponent": 10}`, 1024},
		{"power", `{"base": 9, "exponent": 0.5}`, 3},
		{"sqrt", `{"x": 2}`, 1.4142135623731},
		{"round", `{"x": 2.345, "places": 2}`, 2.35},
		{"round", `{"x": -2.5}`, -3},
		{"subtract", `{"a": 0, "b": 0}`, 0},
	} {
		got := mustCall(t, reg, c.tool, c.input)["result"]
		if got != c.want {
			t.Errorf("%s(%s) = %v, want %v", c.tool, c.input, got, c.want)
		}
	}
}

func TestMathErrors(t *testing.T) {
	reg := build(t, `{"name": "math", "type": "math"}`)
	for _, c := range []struct{ tool, input, want string }{
		{"divide", `{"a": 1, "b": 0}`, "division by zero"},
		{"modulo", `{"a": 1, "b": 0}`, "division by zero"},
		{"sqrt", `{"x": -4}`, "negative"},
		{"power", `{"base": 10, "exponent": 400}`, "not a finite number"},
		{"add", `{"numbers": []}`, "at least one"},
		{"round", `{"x": 1, "places": 16}`, "out of range"},
		{"subtract", `{"a": 1}`, "b"}, // schema: b is required
		{"add", `{"numbers": ["1"]}`, "numbers"},
	} {
		wantErr(t, reg, c.tool, c.input, c.want)
	}
}

func TestStatistics(t *testing.T) {
	reg := build(t, `{"name": "math", "type": "math"}`)
	got := mustCall(t, reg, "statistics", `{"numbers": [4, 1, 3, 2]}`)
	want := map[string]float64{"count": 4, "sum": 10, "min": 1, "max": 4, "mean": 2.5, "median": 2.5, "stddev": 1.11803398874989}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if m := mustCall(t, reg, "statistics", `{"numbers": [5, 1, 9]}`)["median"]; m != 5.0 {
		t.Errorf("median of an odd list = %v, want 5", m)
	}
}
