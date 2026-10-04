#!/bin/sh
# The directory, once the key `customer.sh` minted for it is there.
#
# Told everything from the shell like the account app (`cli/ldap.go`), and
# handed its key the same way: one deployment key, by reference to the file, so
# it is in neither the compose file nor the process list. The suffixes it
# serves are the tenants that nominated it (#76).
set -eu

: "${SEED_CUSTOMER:=contoso}"
: "${ACCOUNT_STATE:=/var/lib/roster-account}"
: "${LDAP_PORT:=1389}"
: "${LDAP_BIND:=key}"

key="${ACCOUNT_STATE}/directory.key"
until [ -e "${key}" ]; do
	echo "roster: waiting for ${key}" >&2
	sleep 1
done


# In the clear, because this is a development stack on one box: a real
# deployment gives `--tls cert,key` and `--require-tls`, or terminates TLS in
# front and passes the plain port on a private network.
exec roster ldap serve \
	--listen ":${LDAP_PORT}" \
	--roster roster:50051 --insecure \
	--deployment-key "file:${key}" \
	--bind "${LDAP_BIND}" "$@"
