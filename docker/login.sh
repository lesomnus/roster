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
# Three clients reach this one sign-in -- the demo product, `oauth2-proxy` in
# front of a page, and the app that is the relying party itself -- and this app
# is told about none of them.
#
# It was `--client contoso=demo,behind,itself`, because the client was what said
# which tenant a flow was about. Which tenant comes from the **redirect** the
# authorization request named now (#36), resolved through that tenant's own
# `Host` row -- so `customer.sh` writes a row per redirect host and there is
# nothing here to keep in step with Hydra's registrations.
: "${LOGIN_CONSENT:=skip}"
: "${LOGIN_REMEMBER:=1h}"

# One key for the deployment and not one per tenant, which is why the name has
# no tenant in it: `roster login provision` writes `login-app.key`.
key="${ACCOUNT_STATE}/login-app.key"
until [ -e "${key}" ]; do
	echo "roster: waiting for ${key}" >&2
	sleep 1
done

# Hydra's admin API, which this app cannot start without and which comes up on
# its own clock. Waited for here rather than with a compose condition because
# what matters is that it **answers**, not that its container is running.
#
# `-T 2` for the reason `docker/dial.sh` gives about the walks: the loop around
# it is the patience, and an attempt that hangs on a name spends the whole
# budget on its first iteration. busybox `wget` waits fifteen minutes by
# default, which is longer than anything that runs this.
until wget -T 2 -qO- "${HYDRA_ADMIN}/health/ready" >/dev/null 2>&1; do
	echo "roster: waiting for ${HYDRA_ADMIN}" >&2
	sleep 1
done

# One variable, with no tenant in its name: the app holds one credential, so the
# loader reads `ROSTER_LOGIN_KEY` like any other setting. It was
# `ROSTER_LOGIN_KEY_<ALIAS>`, one per tenant, which the loader had to be told was
# not a typo.
export "ROSTER_LOGIN_KEY=$(cat "${key}")"

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
	--consent "${LOGIN_CONSENT}" \
	--static /usr/share/roster/login \
	--insecure-cookie "$@"
