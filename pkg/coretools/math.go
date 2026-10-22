package coretools

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"

	"github.com/bsamanvith/agentkit/pkg/tool"
	"github.com/bsamanvith/agentkit/pkg/toolset"
)

// MathType is the type of the [Math] factory.
const MathType = "math"

// maxNumbers bounds the list tools' input (add, multiply, statistics).
const maxNumbers = 10_000

// Math returns the "math" factory: add, subtract, multiply, divide, modulo,
// power, sqrt, round and statistics on float64 numbers. Every tool is
// read/low. Results are rounded to 15 significant digits, so 0.1 + 0.2 is
// 0.3; a result that is not finite (an overflow) is an error. It takes no
// settings.
func Math() toolset.Factory { return mathFactory{} }

type mathFactory struct{}

func (mathFactory) Type() string { return MathType }

type (
	numbersIn struct {
		Numbers []float64 `json:"numbers" jsonschema:"the numbers, at least one"`
	}
	pairIn struct {
		A float64 `json:"a" jsonschema:"first operand"`
		B float64 `json:"b" jsonschema:"second operand"`
	}
	powerIn struct {
		Base     float64 `json:"base" jsonschema:"the base"`
		Exponent float64 `json:"exponent" jsonschema:"the exponent"`
	}
	sqrtIn struct {
		X float64 `json:"x" jsonschema:"a number, zero or more"`
	}
	roundIn struct {
		X      float64 `json:"x" jsonschema:"the number to round"`
		Places int     `json:"places,omitempty" jsonschema:"decimal places, 0 to 15 (default 0); half rounds away from zero"`
	}
	resultOut struct {
		Result float64 `json:"result"`
	}
	statsOut struct {
		Count  int     `json:"count"`
		Sum    float64 `json:"sum"`
		Min    float64 `json:"min"`
		Max    float64 `json:"max"`
		Mean   float64 `json:"mean"`
		Median float64 `json:"median"`
		// StdDev is the population standard deviation.
		StdDev float64 `json:"stddev"`
	}
)

var errDivByZero = errors.New("division by zero")

func (mathFactory) Build(_ context.Context, in toolset.BuildInput) ([]tool.Tool, error) {
	settings := in.Settings
	if err := toolset.DecodeSettings(settings, &struct{}{}); err != nil {
		return nil, err
	}
	meta := func(title string) tool.FuncOption {
		return tool.WithMetadata(tool.Metadata{
			Kind: tool.KindRead, Risk: tool.RiskLow, Tags: []string{tool.TagIdempotent},
			Display: tool.Display{Title: title, Icon: "calc", Present: "Calculating", Past: "Calculated"},
		})
	}
	list := func(name, desc, title string, f func([]float64) float64) (tool.Tool, error) {
		return tool.Func(name, desc, func(_ context.Context, in numbersIn) (resultOut, error) {
			if err := checkNumbers(in.Numbers); err != nil {
				return resultOut{}, err
			}
			return result(f(in.Numbers))
		}, meta(title))
	}
	pair := func(name, desc, title string, f func(a, b float64) (float64, error)) (tool.Tool, error) {
		return tool.Func(name, desc, func(_ context.Context, in pairIn) (resultOut, error) {
			r, err := f(in.A, in.B)
			if err != nil {
				return resultOut{}, err
			}
			return result(r)
		}, meta(title))
	}

	var tools []tool.Tool
	add := func(t tool.Tool, err error) error {
		if err == nil {
			tools = append(tools, t)
		}
		return err
	}
	err := errors.Join(
		add(list("add", "Add numbers: the sum of a list.", "Add", sum)),
		add(pair("subtract", "Subtract b from a: a - b.", "Subtract", func(a, b float64) (float64, error) { return a - b, nil })),
		add(list("multiply", "Multiply numbers: the product of a list.", "Multiply", product)),
		add(pair("divide", "Divide a by b: a / b. Dividing by zero is an error.", "Divide", func(a, b float64) (float64, error) {
			if b == 0 {
				return 0, errDivByZero
			}
			return a / b, nil
		})),
		add(pair("modulo", "The remainder of a / b, with the sign of a. Zero b is an error.", "Modulo", func(a, b float64) (float64, error) {
			if b == 0 {
				return 0, errDivByZero
			}
			return math.Mod(a, b), nil
		})),
		add(tool.Func("power", "Raise base to exponent: base ^ exponent.", func(_ context.Context, in powerIn) (resultOut, error) {
			return result(math.Pow(in.Base, in.Exponent))
		}, meta("Power"))),
		add(tool.Func("sqrt", "The square root of x (x must be zero or more).", func(_ context.Context, in sqrtIn) (resultOut, error) {
			if in.X < 0 {
				return resultOut{}, fmt.Errorf("square root of a negative number (%v)", in.X)
			}
			return result(math.Sqrt(in.X))
		}, meta("Square root"))),
		add(tool.Func("round", "Round x to a number of decimal places (default 0); half rounds away from zero.", func(_ context.Context, in roundIn) (resultOut, error) {
			if in.Places < 0 || in.Places > 15 {
				return resultOut{}, fmt.Errorf("places %d out of range 0..15", in.Places)
			}
			p := math.Pow10(in.Places)
			return result(math.Round(in.X*p) / p)
		}, meta("Round"))),
		add(tool.Func("statistics", "Count, sum, min, max, mean, median and population standard deviation of a list of numbers.",
			func(_ context.Context, in numbersIn) (statsOut, error) {
				if err := checkNumbers(in.Numbers); err != nil {
					return statsOut{}, err
				}
				return statistics(in.Numbers)
			}, meta("Statistics"))),
	)
	if err != nil {
		return nil, err
	}
	return tools, nil
}

func checkNumbers(ns []float64) error {
	switch {
	case len(ns) == 0:
		return errors.New("numbers is empty: give at least one")
	case len(ns) > maxNumbers:
		return fmt.Errorf("too many numbers (%d, at most %d)", len(ns), maxNumbers)
	}
	return nil
}

func sum(ns []float64) float64 {
	var s float64
	for _, n := range ns {
		s += n
	}
	return s
}

func product(ns []float64) float64 {
	p := 1.0
	for _, n := range ns {
		p *= n
	}
	return p
}

// tidy rounds x to 15 significant digits, which drops binary floating-point
// noise (0.30000000000000004 becomes 0.3) and keeps every digit a float64
// holds reliably. Non-finite values are an error: JSON cannot carry them.
func tidy(x float64) (float64, error) {
	if math.IsInf(x, 0) || math.IsNaN(x) {
		return 0, errors.New("the result is not a finite number (overflow)")
	}
	r, err := strconv.ParseFloat(strconv.FormatFloat(x, 'g', 15, 64), 64)
	if err != nil {
		return 0, err
	}
	if r == 0 {
		r = 0 // no -0 in results
	}
	return r, nil
}

func result(x float64) (resultOut, error) {
	r, err := tidy(x)
	return resultOut{Result: r}, err
}

func statistics(ns []float64) (statsOut, error) {
	s := sum(ns)
	mean := s / float64(len(ns))
	var sq float64
	for _, n := range ns {
		sq += (n - mean) * (n - mean)
	}
	sorted := slices.Clone(ns)
	slices.Sort(sorted)
	median := sorted[len(sorted)/2]
	if len(sorted)%2 == 0 {
		median = (sorted[len(sorted)/2-1] + median) / 2
	}
	out := statsOut{Count: len(ns)}
	var errs []error
	for _, f := range []struct {
		dst *float64
		v   float64
	}{
		{&out.Sum, s},
		{&out.Min, sorted[0]},
		{&out.Max, sorted[len(sorted)-1]},
		{&out.Mean, mean},
		{&out.Median, median},
		{&out.StdDev, math.Sqrt(sq / float64(len(ns)))},
	} {
		v, err := tidy(f.v)
		*f.dst = v
		errs = append(errs, err)
	}
	return out, errors.Join(errs...)
}
