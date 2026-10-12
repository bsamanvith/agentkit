package bus_test

import (
	"errors"
	"math"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/bus"
	"github.com/bsamanvith/agentkit/pkg/tenancy"
)

// IDs come back from clients as Last-Event-ID: each ID has one spelling, and
// parsing never accepts a string that is not exactly an ID's String.
func FuzzParseID(f *testing.F) {
	for _, s := range []string{"0", "1", "7.3", "18446744073709551615.18446744073709551615", "", ".", "1.", ".1", "01", "1.0", "1.01", "-1", "+1", "1.2.3", " 1", "1e3", "18446744073709551616"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		id, err := bus.ParseID(s)
		if err != nil {
			if !errors.Is(err, bus.ErrInvalid) {
				t.Fatalf("ParseID(%q): err = %v, want ErrInvalid", s, err)
			}
			return
		}
		if id.String() != s {
			t.Fatalf("ParseID(%q) = %v, which prints as %q", s, id, id.String())
		}
	})
}

func TestIDRoundTripAndOrder(t *testing.T) {
	t.Parallel()
	ids := []bus.ID{{}, {Seq: 0, N: 1}, {Seq: 1}, {Seq: 1, N: 1}, {Seq: 1, N: math.MaxUint64}, {Seq: 2}, {Seq: math.MaxUint64, N: math.MaxUint64}}
	for i, id := range ids {
		got, err := bus.ParseID(id.String())
		if err != nil || got != id {
			t.Errorf("ParseID(%q) = %v, %v; want %v", id.String(), got, err, id)
		}
		for j, other := range ids {
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if c := id.Compare(other); c != want {
				t.Errorf("%v.Compare(%v) = %d, want %d", id, other, c, want)
			}
		}
	}
}

// Topic names never collide across tenants, kinds or ids, whatever the ids
// contain.
func TestTopicNamesAreDistinct(t *testing.T) {
	t.Parallel()
	acme, _ := tenancy.NewTenant("acme")
	acmeX, _ := tenancy.NewTenant("acme-x")
	ids := []string{"r", "r:cmd", "r%3Acmd", "x:r", "r:", ":", "%", "%25", "u/1", "ü", "a b"}
	seen := map[string]string{}
	for _, tn := range []tenancy.Tenant{acme, acmeX} {
		for _, id := range ids {
			for kind, mk := range map[string]func(tenancy.Tenant, string) (bus.Topic, error){
				"run": bus.RunTopic, "cmd": bus.CommandTopic, "user": bus.UserTopic,
			} {
				topic, err := mk(tn, id)
				if err != nil {
					t.Fatalf("%s(%s, %q): %v", kind, tn.ID(), id, err)
				}
				who := kind + " " + tn.ID() + " " + id
				if prev, ok := seen[topic.String()]; ok {
					t.Errorf("%s and %s share the topic %q", prev, who, topic)
				}
				seen[topic.String()] = who
			}
		}
	}
	for _, bad := range []func() (bus.Topic, error){
		func() (bus.Topic, error) { return bus.RunTopic(tenancy.Tenant{}, "r") },
		func() (bus.Topic, error) { return bus.UserTopic(acme, "") },
	} {
		if _, err := bad(); !errors.Is(err, bus.ErrInvalid) {
			t.Errorf("topic with no tenant or id: err = %v, want ErrInvalid", err)
		}
	}
}
