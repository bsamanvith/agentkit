---
id: T043
title: "Credential backends: encrypted file now, SQLite on T029"
epic: E07
status: done
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
- **`credentials/postgres`** (planner, owner decision: SQLite only in local images, Postgres for dev and cloud, T088 with RLS from T093): the same table in the Postgres store's migration set, with the tenant RLS policy, CAS via version, on the store's pool and URL; run against real Postgres with `make integration STORE=postgres`.
- **Config:** a `credentials` section in `agentkit serve`: `{"backend": "memory"|"file"|"sqlite"|"postgres", "path": ...}`. The key ring comes from `AGENTKIT_CREDENTIALS_KEY`. Startup fails if a non-memory backend has no key.

## Acceptance criteria

- [x] **File backend** passes `credentialstest.RunBlobStore` and `RunVault`.
- [x] **Crash safety:** a crash during a write never leaves a truncated or mixed file. The test kills a child process at injected points (before rename, after rename) and reopens; the result is the old or the new state.
- [x] **Permissions:**
  - a group- or world-readable file is refused at open, with a message naming the path and the fix;
  - new files are 0600.
- [x] **One writer:** a second `Open` of the same file while the first is open fails with `ErrLocked`.
- [x] **SQLite backend** passes both suites on a temp database. CAS holds under concurrent writers from two `*sql.DB` handles on the same file. (This criterion waits for T029; the rest of the task does not.)
- [x] **Canary:** the canary is absent from the file or database bytes (`grep` the file).
- [x] **Postgres backend** (planner) passes both suites as a role under row-level security, also with two pools on one database; the RLS policy holds for unfiltered queries; the `sqlite` backend links only into the local image and `postgres` not into it (`make deps-check`).
- [x] **Mutation list** recorded (at least: write without rename, mode 0644, missing CAS `WHERE`, lock not taken).

## Notes

- Split it if T029 is far off: ship the file backend, set `status: review` with the SQLite criterion open, and say so in the handoff. (T029 had landed: not split.)
- Done 2026-10-21. Tests per criterion:

| Criterion | Test |
|---|---|
| File backend, both suites | `pkg/credentials/file`: `TestBlobStore`, `TestVault`, and `TestReopened` (both suites with the file closed and read again around every call) |
| Crash safety | `TestCrashSafety`: a child (`TestCrashChild`) SIGKILLs itself mid-write of the temp file, before the rename and after it; the reopened file holds the old state, old, new |
| Permissions | `TestRefusesOpenFile` (0640, 0604, 0620, 0602, 0644, 0666: `ErrInsecure`, the path and `chmod 600 <path>`), `TestNewFileMode` |
| One writer | `TestOneWriter` (`ErrLocked`, works after Close); `cmd/agentkit` `TestCredentialsFile` |
| SQLite, both suites, CAS across `*sql.DB` handles | `pkg/credentials/sqlite`: `TestBlobStore`, `TestVault`, `TestTwoHandles` (both suites, races and simulations included, alternating between two Opens of one file), `TestConcurrentHandles` |
| Postgres, both suites | `pkg/credentials/postgres` (`-tags integration`): `TestBlobStore`, `TestVault` as a NOBYPASSRLS role, `TestTwoReplicas`, `TestConcurrentCreates`, `TestRowLevelSecurity`, `TestNeedsTheMigration`; `cmd/agentkit` `TestCredentialsPostgres` |
| Canary | `credentialstest.CheckAtRest` (every kind, two tenants, user and shared, a delete) over the file's bytes, the SQLite file with its WAL and SHM, and every Postgres row as text |
| Images | `make deps-check` names `pkg/credentials/sqlite` (forbidden in dev, api, runner, toolsets) and `pkg/credentials/postgres` (forbidden in local, runner, toolsets); `TestCredentialsSQLiteNotBuilt` (`-tags nosqlite`), `TestCredentialsPostgresNotBuilt` (`-tags nopostgres`) |
| Config | `TestServeRejectsBadSetup` rows `credentials: ...` (no key, unknown backend or field, file without path, memory with path, postgres without a Postgres store), `TestCredentialsNoVault`, `TestCredentialsSQLiteInTheStore` |

- Decisions:
  - **Column `user_id`, not `user`:** `user` is reserved in Postgres; both SQL backends use the same name.
  - **Tombstones:** a delete sets `blob` to NULL and keeps the row (the file keeps a `"deleted": true` record), so a key's versions never repeat, even across restarts. A create is one `INSERT ... ON CONFLICT DO UPDATE ... WHERE blob IS NULL`: it takes a tombstone's row with the next version, and racing creators get `ErrExists`, never a unique violation. A replace is one `UPDATE ... WHERE version = ? AND blob IS NOT NULL`; `UpdatedAt` is `MAX`/`GREATEST(new, created_at)` in the same statement.
  - **The SQL tables are store migrations** (SQLite 0009, Postgres 0010, with 0008's RLS policy), so `agentkit migrate` and `AGENTKIT_STORE_MIGRATE=off` cover them. The backends reach the store's connections through two new accessors, `(*sqlite.Store).DB()` and `(*postgres.Store).Pool()`; `New(store)` shares an open store's connections, `Open(path|url)` opens one of its own. Postgres connections set `app.tenant` as the store's do, so RLS applies.
  - **Config:** `credentials.backend` defaults to `memory`; `sqlite` without a path uses the file `AGENTKIT_STORE=sqlite:...` names; `postgres` takes no path and needs `AGENTKIT_STORE=postgres://...` (one secret, not two) and honors `AGENTKIT_STORE_MIGRATE`. Every backend but `memory` fails startup without `AGENTKIT_CREDENTIALS_KEY`; `memory` without a key gives no vault (as before). The vault is opened at startup for every component and kept in `serveEnv.vault`, closed at shutdown.
  - **File lock:** `flock(2)` on `<path>.lock`, released by the kernel when the process dies, so a crash never leaves the file locked; other systems than Unix refuse the file backend. Group or other bits of any kind (not only read) are refused. The lock file itself holds nothing and is not mode-checked.
  - **Times** in the SQL backends are Unix nanoseconds, as in the stores; a time outside 1678 to 2262 (an OAuth expiry in 9999) is `ErrInvalidCredential` rather than wrapped (`TestTimeRange`).
  - **`NeedsReconnect` is not stored:** `BlobStore` has no way to set it; T044 adds the column with a migration when it does.
  - Fixed in the shared suite: the rig's invariant check ran on `t.Context()`, which is canceled before cleanups, so every backend that honors its context failed it. It now uses `context.WithoutCancel`.
- Sizes (stripped `GOOS=linux CGO_ENABLED=0 -trimpath` binaries per variant, main at `bc9029e` and the branch): all +86,016, local +77,824, dev +77,824, api +90,112, runner +73,728, toolsets +69,632. Under 100 KB for every image, so no docs/SIZES.md row. No new dependency.
- Mutations (each planted by a script, each caught unless noted): file written in place without the rename (`TestCrashSafety`); mode 0644 on create (`TestNewFileMode`); group and other bits not checked (`TestRefusesOpenFile`); lock not taken (`TestOneWriter`); tombstones dropped, so versions repeat (suite `delete`, simulation); file `List` ignoring the tenant (suite); a stale temp file kept at open (`TestCrashSafety`); SQLite missing the CAS `WHERE version = ?` (suite `versions`, `delete`, simulation); SQLite create over a live credential (no `WHERE blob IS NULL`); SQLite hard `DELETE` (versions repeat); SQLite `List` ignoring the tenant; SQLite `UpdatedAt` before `CreatedAt`; Postgres missing the CAS `WHERE`; Postgres hard `DELETE`; no RLS on `credential_blobs` (`TestRowLevelSecurity`); `CheckAtRest` reading no bytes (its slot check); the SQLite backend linked into every image and the Postgres one into local (`make deps-check`); a non-memory backend without a key (`TestServeRejectsBadSetup`). Survives, as expected: no `fsync` of the temp file, which only a power loss shows.

## For T044

- The vault is `serveEnv.vault` (`cmd/agentkit/serve.go`): nil when there is no key ring and the backend is `memory`. Fail startup naming `AGENTKIT_CREDENTIALS_KEY` when a toolset needs a credential and it is nil.
- Setting `NeedsReconnect` needs a column (`needs_reconnect`) in both SQL backends (a new store migration each), a field in the file's records, and a `BlobStore` method or a `Put` that keeps it; every backend runs the suites again.
- Postgres replicas share credentials through `credentials.backend: "postgres"`; a refresh's compare-and-swap is the backend's `UPDATE ... WHERE version`.
