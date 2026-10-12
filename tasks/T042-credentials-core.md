---
id: T042
title: "pkg/credentials: secrets, sealer, vault interface, memory vault"
epic: E07
status: todo
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

- [ ] `Secret` never prints its value through `fmt` (every verb including `%#v` and `%x`), `slog` (text and JSON handlers, nested in structs and groups), `encoding/json` (an error, also when nested), or `errors` wrapping. Table test over all of these.
- [ ] **At rest:** a recording `BlobStore` sees only ciphertext. The canary value never appears in any stored byte, key or metadata.
- [ ] **Binding:**
  - a blob moved to another tenant, user or slot (by editing the `BlobStore` directly) fails to open with `ErrCorrupt`;
  - so does a single flipped bit anywhere in the blob.
- [ ] **Rotation:**
  - with the ring `k2,k1`, blobs sealed under `k1` still open;
  - `Reseal` moves them to `k2`;
  - after `k1` is removed they still open;
  - a blob under an unknown key ID is `ErrUnknownKey`.
- [ ] **Bad key config fails at startup** with a clear message (no secret in it): a malformed key ring, a wrong key length, a duplicate ID, or no key.
- [ ] **Compare-and-swap:**
  - of two writers with the same `ifVersion`, exactly one wins;
  - the other gets `ErrConflict`;
  - create-only on an existing slot gives `ErrExists`;
  - the simulation checks this against a model with many writers.
- [ ] **Tenant isolation:** no tenant in `ctx` gives `ErrNoTenant`; tenant A can never get, list, overwrite or delete B's credentials (suite with the same user and slot in both).
- [ ] **Listing:** `List` returns metadata only. Marshal `Meta` to JSON, and the canary is absent.
- [ ] **Memory backend** passes both suites; mutation list recorded (at least: AAD omitted, nonce reused, tenant ignored in `List`, CAS not checked, `Secret` printed by `%v`).

## Notes

- Use `crypto/cipher` and `crypto/rand` only. No new dependency.
- `AGENTKIT_CREDENTIALS_KEY` is read once, by `cmd/agentkit`. The package takes a key ring value, never the environment.
