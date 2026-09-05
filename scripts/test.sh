#!/usr/bin/env bash
# Everything CI decides on, in one command, so that a green run at a desk means
# what a green run there means.
#
# It did not, and that is why this exists. roster's gates were a list in
# `CLAUDE.md` for somebody to remember, and the two that got forgotten were the
# two no compiler complains about: a generated file that was not regenerated
# **compiles perfectly and is wrong**, and `gofmt` cares about a rename that
# nothing else in the repository notices. Both were found by a red push after a
# green afternoon. A gate nobody can run in one command is a gate that runs
# after the push.
#
# It takes whatever it is handed, so the ordinary narrowing still works:
#
#     ./scripts/test.sh                  # the gate
#     ./scripts/test.sh -run TestWall    # while working on one thing
#
# `PDTEST_POSTGRES` is obeyed by the tests rather than by this, so the other
# half of CI is the same command with it set -- everything roster generates is
# SQL, and SQLite is permissive in the directions that hide a mistake:
#
#     PDTEST_POSTGRES='postgres://app:app@127.0.0.1:5432/app?sslmode=disable' ./scripts/test.sh
#
# # What it does not cover
#
# `buf breaking`, which CI runs against `origin/main`. That is a question about
# this branch's relationship to the branch rather than about this checkout, it
# needs a ref a shallow clone does not have, and breaking the wire on purpose is
# a migration somebody argues for in a review. It is the one thing that can be
# red here after this is green.
set -o errexit
set -o pipefail

__root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${__root}"

# First because it is the cheapest thing here and the only one whose answer is
# the same on every machine. `node_modules` is filtered rather than excluded by
# path, because gofmt walks what it is given and a vendored Go file in there is
# nobody's to format.
echo "== gofmt"
if v="$(gofmt -l . | grep -v node_modules || true)"; [ -n "${v}" ]; then
	echo "not gofmt'd:" >&2
	echo "${v}" >&2
	echo >&2
	echo "    gofmt -w ${v}" >&2
	exit 1
fi

echo "== roster"
go build ./...
go vet ./...
go test -count=1 "$@" ./...

# What no compiler and no test can see.
#
# `doctor` is the one that finds a layer with no `WithDriver` -- which fails at
# run time, only inside a transaction, and only once somebody calls a
# multi-write RPC. `gen --check` is the one that finds the file a commit did not
# carry.
#
# `--ts` because that is what CI runs, and the check without it passes while
# `ts/gen` is a schema behind: a green local run and a red one on the branch.
# It needs the plugin, so a checkout with no `node_modules` is told to install
# rather than told the TypeScript is fine.
echo "== what no compiler and no test can see"
if [ ! -x ts/node_modules/.bin/protoc-gen-es ]; then
	echo "no protoc-gen-es, so the TypeScript half cannot be checked:" >&2
	echo >&2
	echo "    npm ci --prefix ts" >&2
	exit 1
fi
go tool pd doctor .
go tool pd gen --check --ts .

# That the account app is a consumer and only a consumer (`account/`, and
# `ts/plan.md`'s first invariant): it reaches roster over the wire with a key
# an operator minted, and if it could reach past that it would be the second
# thing in this repository that can. `server/front` is the one exception --
# `Hostname`, a pure function both sides have to agree on.
# The directory (`ldap/`) is held to the same: a second consumer, the same
# one exception (`front.Address`, so an address is looked up as it is stored).
echo "== the account app and the directory reach roster only over the wire"
for pkg in ./account/ ./ldap/; do
	if go list -f '{{join .Imports "\n"}}' "${pkg}" | grep -E '^github.com/lesomnus/roster/(internal|cmd|server/)' | grep -v '^github.com/lesomnus/roster/server/front$'; then
		echo "${pkg} imports a server package; it is a consumer and reaches roster over the wire" >&2
		exit 1
	fi
done

# That roster still builds for the browser, which is a promise `ts/` already
# makes -- there is a `wasm` script and a sandbox that loads what it produces --
# and one that is kept only by being checked.
echo "== for the browser"
GOOS=js GOARCH=wasm go build ./...

# The console, which the compiler is the whole of the check on: `ts/` declares
# no test script, so what is verified is that it type-checks and bundles. CI
# runs this as a job of its own as well, on a runner with no Go on it, so that a
# broken server does not hide a broken page.
echo "== the console"
npm --prefix ts run check
npm --prefix ts run build
