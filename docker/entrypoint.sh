#!/bin/sh
# Seed the deployment once, then serve it.
#
# This is the image's job and not the CLI's. `roster init` takes no password
# argument and will not grow one -- an argument is in the shell history and in
# the process list, which is why `roster key add` will not take a key either.
# Reading an environment variable and handing it over on a pipe is the
# container's convention, the way `POSTGRES_PASSWORD` is that image's and
# `KEYCLOAK_ADMIN_PASSWORD` is that one's.
#
# # Once, and how that is decided
#
# By asking whether there is anything there, which is what every image doing
# this does -- Postgres looks for `PG_VERSION` in its data directory. Here it is
# a marker beside the databases, written after `init` succeeds. Running `init`
# twice is an error rather than a no-op, deliberately, so tolerating that error
# would be tolerating the one case it exists to report: somebody pointed this at
# the wrong deployment.
#
# # What an environment variable costs
#
# It is visible in `docker inspect`, in the process environment, and in whatever
# file the compose came from. Postgres says the same thing about its own and
# offers `POSTGRES_PASSWORD_FILE` for anything that matters; this image has no
# equivalent because it is a development image, and a development image that
# pretended otherwise would be worse than one that says so.
#
# `admin`/`admin` is the default for the same reason and with the same warning.
# Keycloak stopped having a default at all, because a default ships.
#
# # Not `ROSTER_ADMIN_*`
#
# That prefix is roster's own, for the **admin listener** -- `ROSTER_ADMIN_ADDR`
# is a port. `ROSTER_ROOT_*` is this script's and collides with nothing, which
# `roster config env` will confirm.
set -eu

: "${ROSTER_ROOT_USER:=admin}"
: "${ROSTER_ROOT_PASSWORD:=admin}"
: "${ROSTER_SEED_TENANT:=acme}"
: "${ROSTER_SEED_HOLDER:=admin}"
: "${ROSTER_STATE:=/var/lib/roster}"

seeded="${ROSTER_STATE}/seeded"

if [ ! -e "${seeded}" ]; then
	mkdir -p "${ROSTER_STATE}"

	echo "roster: seeding, once" >&2

	if [ "${ROSTER_ROOT_PASSWORD}" = "admin" ]; then
		echo "roster: the operator's password is the default. this is a development image." >&2
	fi

	# On a pipe, so it is in neither the process list nor the history. `init`
	# refuses an empty one rather than falling back to generating, because a
	# container that meant to set a password and did not is one whose operator
	# cannot sign in and does not know why.
	printf '%s' "${ROSTER_ROOT_PASSWORD}" | roster init \
		--tenant "${ROSTER_SEED_TENANT}" \
		--holder "${ROSTER_SEED_HOLDER}" \
		--operator "${ROSTER_ROOT_USER}" \
		--password-stdin

	# After it succeeded, so a failed seed is retried rather than skipped
	# forever.
	touch "${seeded}"
fi

exec roster "$@"
