#!/usr/bin/env bash
# One OAuth flow through a real Hydra, against a real roster, end to end.
#
# `scripts/test.sh` checks that the Login App compiles and that its own tests
# pass, and those run against a **fake** Hydra -- right for what they are about
# (which operator a challenge resolves to, and what ends up in the token) and
# blind to the protocol around it. Two things were wrong there with every other
# gate green: Hydra's challenge does not fit in a cookie, and the app's own
# environment variables were reported as typos by the loader.
#
# So this is the gate for the half only the real thing has. It stands
# `compose.yaml` up -- Postgres, both planes, a customer, Hydra, its client, the
# Login App -- and walks an authorization-code flow to an `id_token`, checking
# that its `sub` is the `Holder.id` roster holds for the person who typed the
# password.
#
#     ./scripts/hydra.sh            # up, walk, down
#     ./scripts/hydra.sh --hold     # leave it up to look at
#
# Not in `scripts/test.sh` for `scripts/e2e.sh`'s reason: it needs an engine and
# a minute. CI runs it as a job of its own.
set -o errexit
set -o pipefail

__root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${__root}"

# The issuer answers to a name **this network** resolves, because the two demo
# relying parties read its discovery document and every URL in it is built from
# here. `hydra.test` is an alias on the service; `compose.yaml` says the rest. A
# person running `docker compose up` by hand leaves this alone and gets
# `localhost`, which is what their own browser wants.
export ISSUER_HOST=hydra.test

hold=0
if [ "${1:-}" = "--hold" ]; then
	hold=1
	shift
fi

down() {
	if [ "${hold}" = "1" ]; then
		echo
		echo "left up. 'docker compose down -v' when you are done with it"
		return
	fi
	docker compose down -v >/dev/null 2>&1 || true
}
trap down EXIT

# From nothing, every time.
#
# `customer.sh` decides it has already run by one file on a volume, so a seed
# that grows -- a role with one more method, a key for a service that did not
# exist last week -- never reaches a deployment that is already there. That is
# fine for `docker compose up`, which is a thing to look at, and wrong for a
# gate: a run whose answer depends on what a previous run left is not a gate.
# It cost an afternoon here, as `PermissionDenied` for a method the seed had
# been granting for an hour.
echo "== from nothing"
docker compose down -v >/dev/null 2>&1 || true

echo "== up"
# `login` rather than the default set, because its dependencies are everything
# this walk needs and nothing it does not: the account app and the directory are
# other gates' business.
#
# This is the step that waits: `login` depends on `customer` having **completed**,
# so when it returns the tenant is seeded and the app has its key.
docker compose up -d --build login >/dev/null

# `customer.sh` has already run -- `login` waits on it -- so the person exists
# and this is the identifier the token has to name.
echo "== who erin is, to roster"
sub="$(docker compose exec -T roster sh -lc \
	'roster holder get -o json "@${SEED_CUSTOMER:-contoso}/${SEED_USER:-erin}" 2>/dev/null' \
	| sed -n 's/.*"id": "\([^"]*\)".*/\1/p' | head -1)"
if [ -z "${sub}" ]; then
	echo "the seeded person is not there; 'docker compose logs customer'" >&2
	exit 1
fi
echo "   ${sub}"

# A key that may sign her out and give her an authenticator, for the steps of
# the walk that are about her rather than about a flow. `erin` holds the seeded
# `everything` role and `mayReach` passes for your own row, so this is her doing
# both to herself -- which is the same write an operator makes and needs nobody
# wider to exist.
echo "== a key that may sign her out"
# A name of this run's own, because a key's name is unique per holder and this
# script is run again on the same volumes.
gate="hydra-gate-${RANDOM}${RANDOM}"
invalidate="$(docker compose exec -T -e "GATE=${gate}" roster sh -lc \
	'roster key add --tenant "${SEED_CUSTOMER:-contoso}" --holder "${SEED_USER:-erin}" \
		--name "${GATE}" \
		--allow /roster.HolderService/Invalidate,/roster.CredentialService/Enrol,/roster.VouchService/Verify \
		2>/dev/null' | tr -d "\r\n")"
case "${invalidate}" in rt_*) ;; *) echo "no key to sign her out with: ${invalidate}" >&2; exit 1;; esac

# The walk, twice: once as deployed, and once with the consent screen on.
#
# In the dev image and on the compose network, so nothing has to be published
# and it works wherever the engine is.
#
# `--no-deps`, and it is not a nicety. Without it `run` sees that `--build`
# above gave the services a new image, **recreates** `roster` and `customer`,
# and starts the walk while the seed is still running -- so the password is
# refused and the failure reads like the app. It cost a red CI to find, on a
# machine with nothing cached, and never happened on a desk where the volumes
# were already warm.
walk() {
	echo
	echo "== the flow, consent=$1"
	docker compose run --rm --no-deps --entrypoint /usr/local/bin/flow.sh \
		-e "EXPECT_SUB=${sub}" -e "CONSENT=$1" -e "INVALIDATE_KEY=${invalidate}" \
		-e "FACTOR=${FACTOR:-}" login "${@:2}"
}

walk skip "$@"

# The second mode, which is a different app: the setting is read at start.
# Put back afterwards, so `--hold` leaves what `compose.yaml` describes.
echo
echo "== again, with the consent screen on"
LOGIN_CONSENT=ask docker compose up -d --no-deps login >/dev/null
walk ask "$@"
LOGIN_CONSENT=skip docker compose up -d --no-deps login >/dev/null

# The other kind of relying party, before the factor walk changes what a
# password is worth: `oauth2-proxy` in front of a page, which is the shape half
# the deployment's apps are in and the one that produced the last two defects.
echo
echo "== and the same issuer, seen by somebody else's relying party"
docker compose up -d --no-deps --wait behind >/dev/null
docker compose run --rm --no-deps --entrypoint /usr/local/bin/behind.sh login "$@"

# And the other one, which fails differently: an app that holds the token and
# builds its own URLs. What `flow.sh` cannot check is exactly those, because it
# builds them itself out of what it registered the client with.
echo
echo "== and the same issuer, seen by an app that is the relying party itself"
docker compose up -d --no-deps --wait product >/dev/null
docker compose run --rm --no-deps --entrypoint /usr/local/bin/itself.sh login "$@"

# And last, because it gives her an authenticator and does not take it away:
# from here on the password alone is half of a sign-in.
echo
echo "== and with a second factor on her account"
FACTOR=totp walk skip "$@"
