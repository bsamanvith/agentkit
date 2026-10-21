#!/bin/sh
# Fails if git's config holds a credential: the auth header actions/checkout
# persists (http.<url>.extraheader), a config it includes for one, or a token
# in a remote URL. CI runs it right after checkout with
# persist-credentials: false (T101), so no later step can read the job's
# token from .git/config. It prints config names, never values.
set -eu
names=$(git config --list --name-only)
values=$(git config --list | sed 's/^[^=]*=//')
bad=$(printf '%s\n' "$names" | grep -iE '^http\..*\.extraheader$|^includeif\.' || true)
if [ -n "$bad" ]; then
	echo "git config holds credentials:" >&2
	printf '%s\n' "$bad" >&2
	exit 1
fi
if printf '%s\n' "$values" | grep -qiE 'https?://[^/@[:space:]]+@|authorization:|x-access-token'; then
	echo "a git config value carries a credential (a user in an http(s) URL, an auth header or an access token)" >&2
	exit 1
fi
echo "no credentials in git config"
