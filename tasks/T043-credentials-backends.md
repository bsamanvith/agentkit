---
id: T043
title: "Credential backends: encrypted file now, SQLite on T029"
epic: E07
status: todo
depends_on: [T042]
---

# T043: credential backends

## Context

The memory vault (T042) loses credentials on restart. A single process needs a file; replicas need a shared database. Both store only the sealed blobs of T042, so neither handles plaintext. Decisions: [E07](E07-auth-credentials.md).

## Scope

- **`credentials/file`**: one file, the `BlobStore` contents as JSON.
  - Atomic writes: temp file, then `fsync`, then rename, then `fsync` of the directory.
  - Mode 0600 on create; refuse to open a file readable by group or others.
  - A lock file stops two processes from writing at once (refused at open, not corrupted later).
  - The whole file is loaded at open; each change writes it back.
  - For local and single-process use. Documented as such.
- **`credentials/sqlite`** (after T029): a `credential_blobs` table in the same database file as the store:
  - columns `tenant, user, slot, version, kind, blob, created_at, updated_at, expiry`;
  - primary key `(tenant, user, slot)`;
  - compare-and-swap as `UPDATE ... WHERE version = ?`.
  - It reuses T029's connection setup (pure-Go driver).
- **Config:** a `credentials` section in `agentkit serve`: `{"backend": "memory"|"file"|"sqlite", "path": ...}`. The key ring comes from `AGENTKIT_CREDENTIALS_KEY`. Startup fails if a non-memory backend has no key.

## Acceptance criteria

- [ ] **File backend** passes `credentialstest.RunBlobStore` and `RunVault`.
- [ ] **Crash safety:** a crash during a write never leaves a truncated or mixed file. The test kills a child process at injected points (before rename, after rename) and reopens; the result is the old or the new state.
- [ ] **Permissions:**
  - a group- or world-readable file is refused at open, with a message naming the path and the fix;
  - new files are 0600.
- [ ] **One writer:** a second `Open` of the same file while the first is open fails with `ErrLocked`.
- [ ] **SQLite backend** passes both suites on a temp database. CAS holds under concurrent writers from two `*sql.DB` handles on the same file. (This criterion waits for T029; the rest of the task does not.)
- [ ] **Canary:** the canary is absent from the file or database bytes (`grep` the file).
- [ ] **Mutation list** recorded (at least: write without rename, mode 0644, missing CAS `WHERE`, lock not taken).

## Notes

- Split it if T029 is far off: ship the file backend, set `status: review` with the SQLite criterion open, and say so in the handoff.
