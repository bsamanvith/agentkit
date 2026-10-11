package flow_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/flow"
)

type point struct {
	X, Y int
	Tags []string
}

func TestStateSetGetRoundTrip(t *testing.T) {
	t.Parallel()
	st := flow.NewState()
	p := point{X: 1, Y: 2, Tags: []string{"a"}}
	mustSet(t, st, "p", p)

	p.Tags[0] = "changed after Set" // the stored value is a snapshot
	got := mustGet[point](t, st, "p")
	if got.X != 1 || got.Y != 2 || got.Tags[0] != "a" {
		t.Errorf("Get = %+v, want the value as it was when Set", got)
	}
	got.Tags[0] = "changed after Get" // and every Get is a fresh copy
	if again := mustGet[point](t, st, "p"); again.Tags[0] != "a" {
		t.Errorf("Get returned shared memory: %+v", again)
	}

	if _, err := flow.Get[int](st, "missing"); !errors.Is(err, flow.ErrNotFound) {
		t.Errorf("Get(missing) error = %v, want ErrNotFound", err)
	}
	if err := flow.Set(st, "f", func() {}); !errors.Is(err, flow.ErrInvalidValue) {
		t.Errorf("Set(func) error = %v, want ErrInvalidValue", err)
	}
	if err := flow.Set(st, "", 1); !errors.Is(err, flow.ErrInvalidValue) {
		t.Errorf("Set(empty key) error = %v, want ErrInvalidValue", err)
	}
	if st.Has("f") {
		t.Error("a failed Set stored something")
	}
}

func TestStateJSONIsStableAndStrict(t *testing.T) {
	t.Parallel()
	st := flow.NewState()
	mustSet(t, st, "b", 2)
	mustSet(t, st, "a", "<x>")
	out, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if want := `{"a":"\u003cx\u003e","b":2}`; string(out) != want {
		t.Errorf("Marshal = %s, want %s", out, want)
	}

	back := flow.NewState()
	if err := json.Unmarshal([]byte(` { "b" : 2 , "a" : "<x>" } `), back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out2, _ := json.Marshal(back); string(out2) != string(out) {
		t.Errorf("round trip = %s, want %s", out2, out)
	}

	for _, bad := range []string{`null`, `[]`, `"s"`, `1`, `{"a":`, `{"":1}`} {
		keep := flow.NewState()
		mustSet(t, keep, "k", 1)
		if err := json.Unmarshal([]byte(bad), keep); err == nil {
			t.Errorf("Unmarshal(%s) succeeded, want an error", bad)
		} else if !errors.Is(err, flow.ErrInvalidState) && !isSyntax(err) {
			t.Errorf("Unmarshal(%s) error = %v, want ErrInvalidState", bad, err)
		}
		if !keep.Has("k") {
			t.Errorf("a failed Unmarshal(%s) changed the state", bad)
		}
	}
}

// isSyntax reports whether err came from encoding/json itself, which checks
// validity before calling UnmarshalJSON.
func isSyntax(err error) bool {
	var se *json.SyntaxError
	return errors.As(err, &se)
}

func TestStateCloneIsIndependent(t *testing.T) {
	t.Parallel()
	st := flow.NewState()
	mustSet(t, st, "a", 1)
	c := st.Clone()
	mustSet(t, c, "a", 2)
	c.Delete("a")
	mustSet(t, c, "b", 1)
	if got := mustGet[int](t, st, "a"); got != 1 || st.Has("b") {
		t.Errorf("changing a clone changed the original: keys %v", st.Keys())
	}
}

func FuzzStateUnmarshal(f *testing.F) {
	for _, seed := range []string{
		`{}`, `{"a":1}`, `{"a":{"b":[1,2,{"c":null}]},"z":"<"}`, ` {"k" : "v" } `,
		`{"a":1,"a":2}`, `{"":1}`, `null`, `[]`, `{"a":`, `{"a":1e400}`, "{\"a\":\"\xff\"}",
		`{"a":" <>&"}`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		st := flow.NewState()
		if err := st.UnmarshalJSON(data); err != nil {
			return // rejecting is fine; panicking is not
		}
		// Whatever decodes must encode, and encode to a fixed point.
		first, err := json.Marshal(st)
		if err != nil {
			t.Fatalf("Marshal of decoded state: %v", err)
		}
		again := flow.NewState()
		if err := json.Unmarshal(first, again); err != nil {
			t.Fatalf("Unmarshal of own output %s: %v", first, err)
		}
		second, err := json.Marshal(again)
		if err != nil {
			t.Fatalf("second Marshal: %v", err)
		}
		if string(first) != string(second) {
			t.Fatalf("not a fixed point: %s then %s", first, second)
		}
		// Parallel compares bytes, so decoding must be canonical: a decoded
		// state has no changes against its own clone.
		if keys := st.Keys(); len(keys) != len(again.Keys()) {
			t.Fatalf("keys differ after round trip: %v vs %v", keys, again.Keys())
		}
	})
}
