#!/bin/sh
# The account app, once the key `customer.sh` minted is there to front with.
#
# The app is told everything from the shell (`cmd/account.go`) and takes the
# key from the environment rather than a file, so the file is read here and
# handed over that way -- which keeps it out of the compose file and the
# process list both.
set -eu

: "${SEED_CUSTOMER:=contoso}"
: "${PUBLIC_HOST:=localhost}"
: "${ACCOUNT_STATE:=/var/lib/roster-account}"
: "${ACCOUNT_PORT:=8090}"

key="${ACCOUNT_STATE}/${SEED_CUSTOMER}.key"
until [ -e "${key}" ]; do
	echo "roster: waiting for ${key}" >&2
	sleep 1
done

alias="$(printf '%s' "${SEED_CUSTOMER}" | tr '[:lower:]-' '[:upper:]_')"
export "ROSTER_ACCOUNT_KEY_${alias}=$(cat "${key}")"

# One replica, so no `--seal`: the key is made at start. A second replica
# needs `--seal env:NAME` here with the same 32 bytes, base64, in both.
exec roster account serve \
	--listen ":${ACCOUNT_PORT}" \
	--roster roster:50051 --connect http://roster:8080 --insecure \
	--base "http://${PUBLIC_HOST}:${ACCOUNT_PORT}" \
	--static /usr/share/roster/account \
	--insecure-cookie "$@"
