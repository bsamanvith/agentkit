// Package credentials stores the credentials connectors use (E07): API keys,
// passwords, OAuth2 tokens and client credentials, per tenant and optionally
// per user, sealed so the storage never sees a value.
//
//	ring, err := credentials.ParseKeyRing(os.Getenv("AGENTKIT_CREDENTIALS_KEY"))
//	v := credentials.NewVault(memory.New(), credentials.NewSealer(ring))
//	meta, err := v.Put(ctx, "", "salesforce", credentials.APIKey{Key: credentials.NewSecret(k)}, 0)
//	cred, meta, err := v.Get(ctx, "", "salesforce")
//
// A [Secret] reaches its value only through [Secret.Reveal]: printing,
// logging and JSON never show it (JSON refuses to encode it).
//
// A credential lives at a [Key]: the tenant of the context (as in
// pkg/store), a user ("" for one the whole tenant shares) and a slot, named
// like a toolset. The [Vault] seals each credential with AES-256-GCM, with the
// key, its version of the format and the key id bound in as associated data,
// so a blob copied to another tenant, user or slot does not open, and a
// [BlobStore] backend stores only the sealed bytes and [Meta]. Keys rotate
// through a [KeyRing]: the first key seals, any key opens, and
// [Sealer.Reseal] moves a blob to the current key.
//
// Writes are compare-and-swap on [Meta.Version]: 0 creates, n replaces
// version n. Versions never repeat for a key, even after a delete.
//
// Tools reach credentials through a [Source], which [Sources] hands out per
// toolset: it can read one slot only, picks the tenant (and, for
// [ScopeUser], the user) from the call's context, refreshes OAuth2 tokens
// within a margin of their expiry (once per credential in a process,
// written back with compare-and-swap, invalid_grant marking
// NeedsReconnect), fetches client credentials tokens, and adds the
// credential to an http.Client. A missing credential is [ErrNotConnected].
//
// credentials/memory is the reference backend; credentials/file keeps the
// blobs in one file for one process, credentials/sqlite in the SQLite store's
// file (local images), credentials/postgres in the Postgres store's database
// (replicas). credentialstest has the conformance suites every backend runs.
package credentials
