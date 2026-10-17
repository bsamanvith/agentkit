package specs_test

import (
	"testing"

	"github.com/bsamanvith/agentkit/internal/specs"
)

func TestMatch(t *testing.T) {
	t.Parallel()
	prefixes := []string{"memory", "sqlite:", "postgres://"}
	for spec, want := range map[string]bool{
		"memory": true, "memoryx": false, "mem": false,
		"sqlite:/a.db": true, "sqlite:": true, "sqlitex:/a": false,
		"postgres://h/db": true, "postgres:/h": false, "postgress://h": false,
	} {
		if got := specs.Match(prefixes, spec); got != want {
			t.Errorf("Match(%q) = %v, want %v", spec, got, want)
		}
	}
	if got := specs.Want([]string{"a"}, []string{"b:", "c://"}); got != "a…, b:…, c://…" {
		t.Errorf("Want = %q", got)
	}
}
