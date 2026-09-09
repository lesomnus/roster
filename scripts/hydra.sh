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

hold=0
if [ "${1:-}" = "--hold" ]; then
	hold=1
	shift
fi

down() {
	if [ "${hold}" = "1" ]; then
		echo
		echo "left up. 'docker compose down' when you are done with it"
		return
	fi
	docker compose down >/dev/null 2>&1 || true
}
trap down EXIT

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

echo "== the flow"
# In the dev image and on the compose network, so nothing has to be published
# and the walk works wherever the engine is.
#
# `--no-deps`, and it is not a nicety. Without it `run` sees that `--build`
# above gave the services a new image, **recreates** `roster` and `customer`,
# and starts the walk while the seed is still running -- so the password is
# refused and the failure reads like the app. It cost a red CI to find, on a
# machine with nothing cached, and never happened on a desk where the volumes
# were already warm.
docker compose run --rm --no-deps --entrypoint /usr/local/bin/flow.sh \
	-e "EXPECT_SUB=${sub}" login "$@"
