#!/bin/sh
# The Login App, once the key `customer.sh` minted for it is there and Hydra
# has a client to raise challenges for.
#
# Told everything from the shell like the other two (`cli/login.go`), and handed
# its key the same way: read from the file and put in the environment under the
# name `roster login serve` reads, so it is in neither the compose file nor the
# process list.
set -eu

: "${SEED_CUSTOMER:=contoso}"
: "${PUBLIC_HOST:=localhost}"
: "${ACCOUNT_STATE:=/var/lib/roster-account}"
: "${LOGIN_PORT:=8091}"
: "${HYDRA_ADMIN:=http://hydra:4445}"
: "${OAUTH_CLIENT:=demo}"
: "${LOGIN_CONSENT:=skip}"
: "${LOGIN_REMEMBER:=1h}"

key="${ACCOUNT_STATE}/${SEED_CUSTOMER}.login.key"
until [ -e "${key}" ]; do
	echo "roster: waiting for ${key}" >&2
	sleep 1
done

# Hydra's admin API, which this app cannot start without and which comes up on
# its own clock. Waited for here rather than with a compose condition because
# what matters is that it **answers**, not that its container is running.
until wget -qO- "${HYDRA_ADMIN}/health/ready" >/dev/null 2>&1; do
	echo "roster: waiting for ${HYDRA_ADMIN}" >&2
	sleep 1
done

alias="$(printf '%s' "${SEED_CUSTOMER}" | tr '[:lower:]-' '[:upper:]_')"
export "ROSTER_LOGIN_KEY_${alias}=$(cat "${key}")"

# How long Hydra skips the form for a browser that has already signed in. On by
# default here because it is what makes "signed out everywhere" observable at
# all: with nothing remembered there is nothing for roster's sign-out to reach.
export ROSTER_LOGIN_REMEMBER="${LOGIN_REMEMBER}"

# One replica, so no `--seal`: the key is made at start, and what it seals is a
# session that lives from the form to the consent screen.
exec roster login serve \
	--listen ":${LOGIN_PORT}" \
	--roster roster:50051 --insecure \
	--hydra "${HYDRA_ADMIN}" \
	--client "${SEED_CUSTOMER}=${OAUTH_CLIENT}" \
	--consent "${LOGIN_CONSENT}" \
	--static /usr/share/roster/login \
	--insecure-cookie "$@"
