#!/bin/sh
# The account app, once the key `customer.sh` minted is there to front with.
#
# The app is told everything from the shell (`cli/account.go`). Its key is one
# deployment key, read from the file `roster account provision` wrote -- a
# reference, so the token is in neither the compose file nor the process list
# -- and the tenants it fronts are the ones that nominated it (#76).
set -eu

: "${SEED_CUSTOMER:=contoso}"
: "${PUBLIC_HOST:=localhost}"
: "${ACCOUNT_STATE:=/var/lib/roster-account}"
: "${ACCOUNT_PORT:=8090}"

key="${ACCOUNT_STATE}/account.key"
until [ -e "${key}" ]; do
	echo "roster: waiting for ${key}" >&2
	sleep 1
done


# One replica, so no `--seal`: the key is made at start. A second replica
# needs `--seal env:NAME` here with the same 32 bytes, base64, in both.
exec roster account serve \
	--listen ":${ACCOUNT_PORT}" \
	--roster roster:50051 --connect http://roster:8080 --insecure \
	--base "http://${PUBLIC_HOST}:${ACCOUNT_PORT}" \
	--static /usr/share/roster/account \
	--deployment-key "file:${key}" \
	--insecure-cookie "$@"
