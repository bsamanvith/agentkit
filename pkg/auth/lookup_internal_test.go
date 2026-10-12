package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"strconv"
	"testing"
)

// TestLookupComparesEveryEntry is white box: the time a lookup takes must
// not say which entry matched, so lookup compares the presented digest with
// every entry, matching or not, and whole digests. (That the comparison itself is
// subtle.ConstantTimeCompare is checked by reading Authenticate.)
func TestLookupComparesEveryEntry(t *testing.T) {
	t.Parallel()
	var entries []APIKey
	for i := range 5 {
		entries = append(entries, APIKey{ID: "k" + strconv.Itoa(i), Hash: HashKey("key-number-" + strconv.Itoa(i) + "-0123456789"), Tenant: "acme"})
	}
	k, err := NewAPIKeys(entries)
	if err != nil {
		t.Fatal(err)
	}
	for want := -1; want < len(entries); want++ {
		d := sha256.Sum256([]byte("key-number-" + strconv.Itoa(want) + "-0123456789"))
		calls := 0
		got := k.lookup(d, func(x, y []byte) int {
			calls++
			if len(x) != sha256.Size || len(y) != sha256.Size {
				t.Errorf("compared %d and %d bytes, want whole digests", len(x), len(y))
			}
			return subtle.ConstantTimeCompare(x, y)
		})
		if got != want || calls != len(entries) {
			t.Errorf("key %d: lookup = %d after %d comparisons; want %d after %d", want, got, calls, want, len(entries))
		}
	}
}
