package policy_test

import (
	"encoding/json"
	"testing"

	"github.com/bsamanvith/agentkit/pkg/policy"
	"github.com/bsamanvith/agentkit/pkg/tool"
)

// bytes hands out fuzz input one small number at a time, then zeros.
type fuzzBytes struct{ b []byte }

func (s *fuzzBytes) next(n int) int {
	if len(s.b) == 0 {
		return 0
	}
	v := int(s.b[0]) % n
	s.b = s.b[1:]
	return v
}

var (
	fuzzActions  = append(policy.Actions(), "bogus") // an invalid action must fail closed
	fuzzTags     = []string{"external", "pii", "billing"}
	fuzzTools    = []string{"read", "send_*", "gh__*", "*", "x?"}
	fuzzToolsets = []string{"gh", "g*", "mail"}
	fuzzNames    = []string{"read", "send_mail", "gh__read", "gh__drop", "x1", "mail__send_mail"}
)

func pick[T any](s *fuzzBytes, from []T) []T {
	var out []T
	for i := range from {
		if s.next(3) == 0 {
			out = append(out, from[i])
		}
	}
	return out
}

func fuzzPolicy(s *fuzzBytes, depth int) policy.Policy {
	if s.next(5) == 0 && depth == 0 {
		return policy.MustPreset(policy.Presets()[s.next(len(policy.Presets()))])
	}
	p := policy.Policy{Default: fuzzActions[s.next(len(fuzzActions))]}
	for range s.next(5) {
		p.Rules = append(p.Rules, policy.Rule{
			Match: policy.Match{
				Kinds: pick(s, tool.Kinds()), Risks: pick(s, tool.Risks()), Tags: pick(s, fuzzTags),
				Tools: pick(s, fuzzTools), Toolsets: pick(s, fuzzToolsets),
			},
			Action: fuzzActions[s.next(len(fuzzActions))],
		})
	}
	if depth < 2 && s.next(4) == 0 {
		p = policy.Narrow(p, fuzzPolicy(s, depth+1))
	}
	return p
}

func fuzzCall(s *fuzzBytes) (tool.Definition, string) {
	d := tool.Definition{Name: fuzzNames[s.next(len(fuzzNames))]}
	kinds, risks := append(tool.Kinds(), ""), append(tool.Risks(), "")
	d.Kind, d.Risk = kinds[s.next(len(kinds))], risks[s.next(len(risks))]
	d.Tags = pick(s, fuzzTags)
	d.RequiresApproval = s.next(4) == 0
	toolsets := append([]string{""}, fuzzToolsets...)
	return d, toolsets[s.next(len(toolsets))]
}

// rank is the strictness of an action as the package documents it; anything
// else counts as deny.
func rank(a policy.Action) int {
	switch a {
	case policy.Allow:
		return 0
	case policy.Flag:
		return 1
	case policy.Approve:
		return 2
	}
	return 3
}

// FuzzPolicy checks, for random policies and calls, that Narrow is never more
// permissive than either input (and is exactly the stricter of the two), that
// a decision is always one of the four actions with its class, and that a
// valid policy survives its JSON.
func FuzzPolicy(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16})
	f.Add([]byte{0, 0, 3, 1, 0, 2, 2, 1, 0, 9, 4, 0, 1, 1, 0, 3, 3, 2})
	f.Add([]byte("read only, but the agent allows everything; the environment flags billing"))
	f.Fuzz(func(t *testing.T, data []byte) {
		s := &fuzzBytes{b: data}
		a, b := fuzzPolicy(s, 0), fuzzPolicy(s, 0)
		n := policy.Narrow(a, b)
		for range 4 {
			d, set := fuzzCall(s)
			da, db, dn := a.Decide(d, set), b.Decide(d, set), n.Decide(d, set)
			if rank(dn.Action) < rank(da.Action) || rank(dn.Action) < rank(db.Action) {
				t.Fatalf("Narrow is more permissive: %s, %s narrowed by %s\na %+v\nb %+v", dn.Action, da.Action, db.Action, a, b)
			}
			if want := max(rank(da.Action), rank(db.Action)); rank(dn.Action) != want {
				t.Fatalf("Narrow is stricter than both: %s from %s and %s", dn.Action, da.Action, db.Action)
			}
			if rank(dn.Action) == rank(da.Action) && dn != da {
				t.Fatalf("on a tie the base decides: %+v, want %+v", dn, da)
			}
			k, r := d.Class()
			for _, x := range []policy.Decision{da, db, dn} {
				if x.Action != policy.Allow && x.Action != policy.Flag && x.Action != policy.Approve && x.Action != policy.Deny {
					t.Fatalf("decision %+v has no known action", x)
				}
				if x.Kind != k || x.Risk != r || x.Reason == "" || x.Rule == "" {
					t.Fatalf("decision %+v of %+v", x, d)
				}
				if d.RequiresApproval && rank(x.Action) < rank(policy.Approve) && x.Rule == "default" {
					t.Fatalf("a tool that requires approval is %s by default", x.Action)
				}
			}
		}
		if n.Validate() != nil {
			return
		}
		raw, err := json.Marshal(n)
		if err != nil {
			t.Fatal(err)
		}
		var back policy.Policy
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatalf("a valid policy does not read back: %v\n%s", err, raw)
		}
		for range 4 {
			d, set := fuzzCall(s)
			if got, want := back.Decide(d, set), n.Decide(d, set); got != want {
				t.Fatalf("after JSON: %+v, want %+v", got, want)
			}
		}
	})
}
