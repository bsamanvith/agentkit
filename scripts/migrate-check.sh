#!/usr/bin/env bash
# Check `agentkit migrate` the way a release runs it (T102): against an empty
# Postgres database it applies the migrations and exits 0, and a second run
# exits 0 with nothing to do (the schema and schema_migrations unchanged).
# scripts/smoke.sh runs it before serving with AGENTKIT_STORE_MIGRATE=off.
#
#   AGENTKIT_STORE=postgres://... scripts/migrate-check.sh CMD...
#
# CMD runs the binary or image that holds the store, with AGENTKIT_STORE in
# its environment; this script appends `migrate`. Needs psql (PSQL, default
# psql on PATH).
set -euo pipefail

[ $# -gt 0 ] || { echo "usage: AGENTKIT_STORE=postgres://... $0 CMD..." >&2; exit 2; }
url="${AGENTKIT_STORE:?set AGENTKIT_STORE to the postgres:// URL of an empty database}"
psql="${PSQL:-psql}"

q() { "$psql" "$url" -X -q -t -A -v ON_ERROR_STOP=1 -c "$1"; }
fail() { echo "migrate-check: $*" >&2; exit 1; }

# The schema in one line: every column of every table in the search path's
# schema, and every applied migration with its time (a reapplied one moves).
fingerprint() {
	q "SELECT md5(coalesce((SELECT string_agg(table_name || '.' || column_name || ' ' || data_type, ',' ORDER BY table_name, column_name)
		FROM information_schema.columns WHERE table_schema = current_schema()), '')
		|| '|' || (SELECT string_agg(version || ' ' || name || ' ' || applied_at, ',' ORDER BY version) FROM schema_migrations))"
}

[ "$(q "SELECT count(*) FROM information_schema.tables WHERE table_schema = current_schema()")" = 0 ] ||
	fail "the database is not empty; give it one of its own"

"$@" migrate || fail "first run (empty database): exit $?"
[ "$(q "SELECT to_regclass('schema_migrations') IS NOT NULL")" = t ] &&
	n="$(q "SELECT count(*) FROM schema_migrations")" && [ "$n" -gt 0 ] ||
	fail "first run exited 0 but applied no migration"
before="$(fingerprint)"

"$@" migrate || fail "second run: exit $?"
[ "$(fingerprint)" = "$before" ] || fail "second run changed the schema or schema_migrations; it should have had nothing to do"

echo "migrate-check: $n migrations applied to an empty database; a second run had nothing to do"
