#!/bin/sh
# Runs pnpm audit, retrying only when the registry's audit endpoint could not
# be reached (ERR_PNPM_AUDIT_BAD_RESPONSE: a timeout or a 5xx). Advisories at
# or above the level still fail at once. Run from the repository root (make
# web-check does).
set -u
pnpm=${PNPM:-pnpm}
tries=${AUDIT_TRIES:-3}
pause=${AUDIT_PAUSE:-5}
i=1
while :; do
	out=$("$pnpm" --dir web audit --audit-level high 2>&1)
	status=$?
	printf '%s\n' "$out"
	[ "$status" -eq 0 ] && exit 0
	case $out in *ERR_PNPM_AUDIT_BAD_RESPONSE*) ;; *) exit "$status" ;; esac
	[ "$i" -ge "$tries" ] && exit "$status"
	echo "audit endpoint unreachable (try $i of $tries); retrying" >&2
	sleep $((i * pause))
	i=$((i + 1))
done
