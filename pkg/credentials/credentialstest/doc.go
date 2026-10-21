// Package credentialstest has the conformance suites every credentials
// backend runs, so the memory store and the others (file, sqlite, postgres)
// behave the same:
//
//	func TestConformance(t *testing.T) {
//		newStore := func(t *testing.T) credentials.BlobStore { return mybackend.Open(t.TempDir()) }
//		credentialstest.RunBlobStore(t, newStore)
//		credentialstest.RunVault(t, newStore)
//	}
//
// RunBlobStore checks the store's own rules: versions and compare-and-swap,
// tenant and user isolation, copies. RunVault runs [credentials.NewVault]
// over the store with a test key ring and checks what the vault promises on
// top: nothing but ciphertext stored, blobs bound to their tenant, user and
// slot, key rotation. Every test goes through a rig that, when the test
// ends, lists every tenant and user the test touched and checks the
// invariants: metadata sorted by slot, one entry per slot, versions above
// zero, CreatedAt no later than UpdatedAt, and each listed entry readable
// with the same metadata (opening, through a vault). Seeded simulations
// ([Simulate]) check every result against a model, with rounds of
// concurrent writers racing on one version. [CheckAtRest] is the canary test
// of a backend whose bytes a person could read: a file, a database.
package credentialstest
