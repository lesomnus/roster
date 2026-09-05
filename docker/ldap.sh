#!/bin/sh
# The directory, once the key `customer.sh` minted for it is there.
#
# Told everything from the shell like the account app (`cmd/ldap.go`), and
# handed its key the same way: read from the file and put in the environment
# under the name `roster ldap serve` reads, so it is in neither the compose
# file nor the process list.
set -eu

: "${SEED_CUSTOMER:=contoso}"
: "${ACCOUNT_STATE:=/var/lib/roster-account}"
: "${LDAP_PORT:=1389}"
: "${LDAP_BIND:=key}"

key="${ACCOUNT_STATE}/${SEED_CUSTOMER}.ldap.key"
until [ -e "${key}" ]; do
	echo "roster: waiting for ${key}" >&2
	sleep 1
done

alias="$(printf '%s' "${SEED_CUSTOMER}" | tr '[:lower:]-' '[:upper:]_')"
export "ROSTER_LDAP_KEY_${alias}=$(cat "${key}")"

# In the clear, because this is a development stack on one box: a real
# deployment gives `--tls cert,key` and `--require-tls`, or terminates TLS in
# front and passes the plain port on a private network.
exec roster ldap serve \
	--listen ":${LDAP_PORT}" \
	--roster roster:50051 --insecure \
	--bind "${LDAP_BIND}" "$@"
