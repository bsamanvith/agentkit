package toolset

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"time"

	"github.com/bsamanvith/agentkit/pkg/tool"
)

// DemoType is the type of the built-in demo factory.
const DemoType = "demo"

// DemoOption configures [Demo].
type DemoOption func(*demo)

// DemoClock sets the clock behind the current_time tool (default [time.Now]).
// It must be safe for concurrent use.
func DemoClock(now func() time.Time) DemoOption { return func(d *demo) { d.now = now } }

// Demo returns the built-in "demo" factory: echo, current_time and calculate.
// It needs no external accounts, so examples and smoke tests have a real
// toolset to serve.
//
// Settings (all optional):
//
//	{"echo_prefix": "> ", "utc_offset_minutes": 330}
//
// echo_prefix is prepended by the echo tool, which lets two instances be told
// apart. utc_offset_minutes is the zone current_time reports in (-720..840).
func Demo(opts ...DemoOption) Factory {
	d := &demo{now: time.Now}
	for _, o := range opts {
		o(d)
	}
	return d
}

type demo struct{ now func() time.Time }

func (*demo) Type() string { return DemoType }

type demoSettings struct {
	EchoPrefix       string `json:"echo_prefix,omitempty"`
	UTCOffsetMinutes int    `json:"utc_offset_minutes,omitempty"`
}

type (
	echoIn struct {
		Text string `json:"text" jsonschema:"text to echo"`
	}
	echoOut struct {
		Text string `json:"text"`
	}
	timeOut struct {
		Time string `json:"time" jsonschema:"current time, RFC 3339"`
	}
	calcIn struct {
		Op string `json:"op" jsonschema:"operation: add, sub, mul or div (integer division)"`
		A  int64  `json:"a" jsonschema:"first operand"`
		B  int64  `json:"b" jsonschema:"second operand"`
	}
	calcOut struct {
		Result int64 `json:"result"`
	}
)

func (d *demo) Build(_ context.Context, settings json.RawMessage) ([]tool.Tool, error) {
	var s demoSettings
	if err := DecodeSettings(settings, &s); err != nil {
		return nil, err
	}
	if s.UTCOffsetMinutes < -720 || s.UTCOffsetMinutes > 840 {
		return nil, fmt.Errorf("%w: utc_offset_minutes %d out of range -720..840", ErrInvalidSettings, s.UTCOffsetMinutes)
	}
	zone := time.FixedZone("", s.UTCOffsetMinutes*60)

	echo, err := tool.Func("echo", "Repeat the input text.",
		func(_ context.Context, in echoIn) (echoOut, error) {
			return echoOut{Text: s.EchoPrefix + in.Text}, nil
		})
	if err != nil {
		return nil, err
	}
	now, err := tool.Func("current_time", "Return the current time.",
		func(context.Context, struct{}) (timeOut, error) {
			return timeOut{Time: d.now().In(zone).Format(time.RFC3339)}, nil
		})
	if err != nil {
		return nil, err
	}
	calc, err := tool.Func("calculate", "Integer arithmetic: add, sub, mul or div two numbers.",
		func(_ context.Context, in calcIn) (calcOut, error) {
			r, err := calculate(in)
			return calcOut{Result: r}, err
		})
	if err != nil {
		return nil, err
	}
	return []tool.Tool{echo, now, calc}, nil
}

func calculate(in calcIn) (int64, error) {
	a, b := big.NewInt(in.A), big.NewInt(in.B)
	r := new(big.Int)
	switch in.Op {
	case "add":
		r.Add(a, b)
	case "sub":
		r.Sub(a, b)
	case "mul":
		r.Mul(a, b)
	case "div":
		if in.B == 0 {
			return 0, fmt.Errorf("division by zero")
		}
		r.Quo(a, b)
	default:
		return 0, fmt.Errorf("unknown op %q (want add, sub, mul or div)", in.Op)
	}
	if !r.IsInt64() {
		return 0, fmt.Errorf("result overflows int64")
	}
	return r.Int64(), nil
}
