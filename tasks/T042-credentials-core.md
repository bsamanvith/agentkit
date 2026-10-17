---
id: T042
title: "pkg/credentials: secrets, sealer, vault interface, memory vault"
epic: E07
status: done
depends_on: [T027]
---

# T042: `pkg/credentials` core

## Context

Connectors need per-tenant (and per-user) credentials, stored encrypted and never leaked. This task is the storage half: what a credential is, how it is sealed, and the `Vault` interface every backend implements. T044 puts it in front of tools. Decisions: [E07](E07-auth-credentials.md).

## Scope

- **`credentials.Secret`**, a string-like type whose value is reachable only through `Reveal()`:
  - `String`, `GoString`, `Format` (all verbs) and `slog.LogValuer` print `[redacted]`;
  - `MarshalJSON` and `MarshalText` return an error, so it cannot be serialized by accident;
  - storage uses an explicit encoding.
- **Credential kinds** (a tagged union with a JSON form used only inside the sealed blob):
  - `APIKey{Key Secret}`;
  - `Basic{User string; Password Secret}`;
  - `OAuth2{AccessToken, RefreshToken Secret; Expiry time.Time; Scopes []string; TokenType string}`;
  - `ClientCredentials{ClientID string; ClientSecret Secret; TokenURL string; Scopes []string}`.
- **`Key`:** `{Tenant (from ctx), User string (empty means tenant-wide), Slot string}`. Slot names follow the toolset name rule.
- **`Sealer`:**
  - AES-256-GCM with a random 96-bit nonce per seal;
  - associated data = version, tenant, user, slot;
  - a key ring `{current key ID, keys by ID}`, from `AGENTKIT_CREDENTIALS_KEY` (`<id>:<base64 32 bytes>[,<id>:<base64>...]`, the first is current);
  - `Open` with any known key; `Reseal` moves a blob to the current key.
- **`Vault` interface** (tenant from `ctx`, like `pkg/store`):
  - `Get(ctx, user, slot) (Credential, Meta, error)`;
  - `Put(ctx, user, slot, cred, ifVersion int64) (Meta, error)`, where `ifVersion` 0 means create-only and any other value is compare-and-swap; it returns `ErrConflict` or `ErrExists`;
  - `Delete(ctx, user, slot, ifVersion)`;
  - `List(ctx, user) ([]Meta, error)`.
  - `Meta` holds `{Slot, User, Kind, Version, CreatedAt, UpdatedAt, Expiry?, NeedsReconnect bool}`, and never secrets.
  - The vault seals and opens. Backends implement a lower `BlobStore` (`Get`/`Put`/`Delete`/`List` of opaque bytes with versions) that never sees plaintext.
- **`credentials/memory`**: the reference `BlobStore`.
- **`credentialstest.RunBlobStore`** and **`RunVault`**: conformance suites with invariants checked after every test, plus a seeded simulation of concurrent CAS writers across tenants and users.

## Out of scope

- File and SQLite backends (T043). Using credentials in tools, refresh (T044). HTTP endpoints (T045).

## References

- [NIST SP 800-38D: GCM](https://csrc.nist.gov/pubs/sp/800/38/d/final) (nonce uniqueness, associated data)
- [OWASP Secrets Management Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Secrets_Management_Cheat_Sheet.html)
- Envelope encryption and key rotation in Google Tink (key sets with key IDs) for the key-ring shape

## Acceptance criteria

- [x] `Secret` never prints its value through `fmt` (every verb including `%#v` and `%x`), `slog` (text and JSON handlers, nested in structs and groups), `encoding/json` (an error, also when nested), or `errors` wrapping. Table test over all of these.
- [x] **At rest:** a recording `BlobStore` sees only ciphertext. The canary value never appears in any stored byte, key or metadata.
- [x] **Binding:**
  - a blob moved to another tenant, user or slot (by editing the `BlobStore` directly) fails to open with `ErrCorrupt`;
  - so does a single flipped bit anywhere in the blob.
- [x] **Rotation:**
  - with the ring `k2,k1`, blobs sealed under `k1` still open;
  - `Reseal` moves them to `k2`;
  - after `k1` is removed they still open;
  - a blob under an unknown key ID is `ErrUnknownKey`.
- [x] **Bad key config fails at startup** with a clear message (no secret in it): a malformed key ring, a wrong key length, a duplicate ID, or no key.
- [x] **Compare-and-swap:**
  - of two writers with the same `ifVersion`, exactly one wins;
  - the other gets `ErrConflict`;
  - create-only on an existing slot gives `ErrExists`;
  - the simulation checks this against a model with many writers.
- [x] **Tenant isolation:** no tenant in `ctx` gives `ErrNoTenant`; tenant A can never get, list, overwrite or delete B's credentials (suite with the same user and slot in both).
- [x] **Listing:** `List` returns metadata only. Marshal `Meta` to JSON, and the canary is absent.
- [x] **Memory backend** passes both suites; mutation list recorded (at least: AAD omitted, nonce reused, tenant ignored in `List`, CAS not checked, `Secret` printed by `%v`).

## Notes

- Use `crypto/cipher` and `crypto/rand` only. No new dependency.
- `AGENTKIT_CREDENTIALS_KEY` is read once, by `cmd/agentkit`. The package takes a key ring value, never the environment.
- Done 2026-10-17. Decisions:
  - **Tenant from `ctx`, user and slot as arguments**, as the scope said. `Key` is exported for the sealer only; callers never build one for the vault.
  - **The store owns versions and times.** A version never repeats for a key, even after a delete (the memory store keeps the last version per key), so a stale `ifVersion` from before a delete can never match a new credential. `UpdatedAt` comes from the vault's clock but is never before `CreatedAt`: replicas' clocks differ.
  - **`Get` checks the metadata's kind against the sealed one** (`ErrCorrupt` on a mismatch): `Kind` sits outside the ciphertext, so a store could change it.
  - **Blob format:** version byte, key id length, key id, 12-byte nonce, ciphertext and tag. AAD = label, version, then key id, tenant, user and slot each length-prefixed, so ("us", "") and ("u", "s") never share it.
  - **`AGENTKIT_CREDENTIALS_KEY` is optional for now.** `serve` parses it at startup when set (a bad ring fails with `ErrKeyConfig`, no key material in the message) and keeps the ring in `serveEnv`; nothing reads it until T044. Requiring it now would break every deployment that has no connector credentials.
  - `NeedsReconnect` is in `Meta` but nothing sets it yet: T044's refresh does.
- Mutations (each made by hand, each failed `go test ./pkg/credentials/...`): AAD without the tenant; AAD without length prefixes; a fixed nonce; `List` ignoring the tenant; `Put` ignoring compare-and-swap; `Delete` ignoring it; versions restarting after a delete; `Get` not checking the kind; `Secret.Format` printing the value; `Secret.MarshalJSON` succeeding; the store keeping the caller's byte slice.
- Found while testing: flipping a bit in the key id length can make the id name another (unknown) key, so the bit-flip test accepts `ErrUnknownKey` for bytes 1 through the id, `ErrCorrupt` everywhere else.

## For T043 and T044

- A backend implements `credentials.BlobStore` and runs `credentialstest.RunBlobStore` and `RunVault` (see `pkg/credentials/memory/memory_test.go`). The doc comment on `BlobStore` is the contract: versions, `CreatedAt`, `UpdatedAt`, copies.
- `credentialstest.Simulate(t, newStore, seed)` runs one seed of concurrent writers against a model; `RunBlobStore` runs 20.
- The key ring is `serveEnv.credentialKeys` in `cmd/agentkit/serve.go`; build the vault with `credentials.NewVault(store, credentials.NewSealer(ring))`. When a toolset needs a credential and the ring is nil, fail startup naming `AGENTKIT_CREDENTIALS_KEY`.
- T095's `pkg/mcp/oauth` keeps tokens in its own `oauth.TokenStore` (with `Swap` for compare-and-swap); a vault-backed `TokenStore` maps onto `Put` with `ifVersion` and the `OAuth2` kind.
