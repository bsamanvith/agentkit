// Package specs matches the specs that name a backend, such as
// $AGENTKIT_STORE and $AGENTKIT_BUS ("memory", "sqlite:/data/a.db",
// "redis://host"), for the tables of backends that open them (storeopen,
// busopen): one rule for both.
package specs

import "strings"

// Match reports whether spec is one of a backend's: a prefix that ends in
// ":" or "://" ("sqlite:", "postgres://") starts it, and any other
// ("memory") is the whole spec.
func Match(prefixes []string, spec string) bool {
	for _, p := range prefixes {
		if spec == p || (strings.HasSuffix(p, ":") || strings.HasSuffix(p, "://")) && strings.HasPrefix(spec, p) {
			return true
		}
	}
	return false
}

// Want lists every backend's prefixes for an error that says what a spec
// may be ("memory…, sqlite:…"), never the spec itself: a URL can carry a
// password.
func Want(prefixes ...[]string) string {
	var names []string
	for _, ps := range prefixes {
		for _, p := range ps {
			names = append(names, p+"…")
		}
	}
	return strings.Join(names, ", ")
}
