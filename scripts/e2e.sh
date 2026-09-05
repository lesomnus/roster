#!/usr/bin/env bash
# The two pages, driven by a browser against a deployment stood up the way
# `docs/operating.md` says to stand one up.
#
# `scripts/test.sh` checks that the pages type-check and bundle, and the Go
# suites check every rule the pages rely on -- against a server in the same
# process, with the calls a page would make written by hand. What none of that
# checks is the page itself: that the form posts what `frontdoor` reads, that
# `navigator.credentials` is asked for the key roster named, that the console
# reaches the admin listener from the origin the control listener served it
# from. Each of those was wrong once with every other gate green, which is why
# this exists.
#
# It builds the binary and the pages, writes a `roster.yaml` into a scratch
# directory, runs `roster init` and the seed a first customer takes, starts
# `roster serve` and `roster account serve`, and runs Playwright against both.
# Whatever it is handed goes to Playwright:
#
#     ./scripts/e2e.sh                    # everything
#     ./scripts/e2e.sh account            # one spec
#     ./scripts/e2e.sh --headed -g totp   # one test, watching
#
# It is not in `scripts/test.sh`, because it needs a browser and a minute, and
# a gate nobody runs is a gate. CI runs it as a job of its own.
set -o errexit
set -o pipefail

__root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${__root}"

work="$(mktemp -d)"
pids=()
trap 'kill "${pids[@]}" 2>/dev/null; wait 2>/dev/null; rm -rf "${work}"' EXIT

echo "== build"
go build -o "${work}/roster" ./cmd/roster
npm --prefix ts run build >/dev/null
# The sandbox too, unless told not to: two wasm builds, which is most of what
# this step costs. `E2E_SANDBOX=0` skips it and its spec.
if [ "${E2E_SANDBOX:-1}" != "0" ]; then
	npm --prefix ts run wasm >/dev/null
fi

# Ports nothing else on a desk is likely to be on -- and nothing may be on
# them now, or `up` below answers from whatever that is. It happened: a dev
# server left over from a `--hold` the day before answered for the sandbox,
# serving the modules it had cached, and a change to the library it served
# was invisible for an afternoon.
for port in 18051 18052 18061 18062 18071 18072 18090 18100; do
	if (exec 3<>"/dev/tcp/127.0.0.1/${port}") 2>/dev/null; then
		echo "something is already listening on 127.0.0.1:${port}; stop it first (a --hold left running?)" >&2
		exit 1
	fi
done

# Ports nothing else on a desk is likely to be on. `localhost` rather than
# `127.0.0.1` for the account app because a security key's relying party is a
# domain, and an address is not one.
export E2E_CONSOLE="http://127.0.0.1:18062/console/"
export E2E_ACCOUNT="http://localhost:18090"
export E2E_OPS_PASSWORD="ops-$(head -c 12 /dev/urandom | base64 | tr -d '/+=')"
export E2E_ERIN_PASSWORD="correct horse battery staple"
admin_http="http://127.0.0.1:18072"

cat > "${work}/roster.yaml" <<YAML
db:
  driver: sqlite3
  dsn: "file:${work}/roster.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
  migrate: true
watch:
  broker: memory
server:
  addr: 127.0.0.1:18051
  http:
    addr: 127.0.0.1:18052
    allow_web: true
control:
  db:
    driver: sqlite3
    dsn: "file:${work}/control.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
    migrate: true
  watch:
    broker: memory
  addr: 127.0.0.1:18061
  http:
    addr: 127.0.0.1:18062
    allow_web: true
    allow_pprof: true
  console:
    dir: ${__root}/ts/dist/console
    admin: ${admin_http}
admin:
  addr: 127.0.0.1:18071
  http:
    addr: 127.0.0.1:18072
    allow_web: true
    origins: ["http://127.0.0.1:18062"]
vouch:
  keys: ["e2e:$(head -c 32 /dev/urandom | base64)"]
YAML
r() { "${work}/roster" --config "${work}/roster.yaml" "$@"; }

echo "== init and seed"
echo "${E2E_OPS_PASSWORD}" | r init --operator admin --password-stdin >/dev/null
r tenant add @contoso '{"name":"Contoso"}' >/dev/null
r holder add @contoso/erin >/dev/null
r role add @contoso/everything '{"methods":["/roster.*/*"]}' >/dev/null
echo '{"role":{"slug":{"alias":"everything","tenant":{"alias":"contoso"}}},"holder":{"slug":{"alias":"erin","tenant":{"alias":"contoso"}}}}' \
	| r binding add - >/dev/null
echo "${E2E_ERIN_PASSWORD}" | r vouch set --password-stdin @contoso/erin >/dev/null 2>&1
r host add '{"tenant":{"alias":"contoso"},"name":"localhost"}' >/dev/null
# The account app's own key: a person of the tenant's, holding what the app
# calls as itself (`account/account.go` says which), which is not everything.
r holder add @contoso/account >/dev/null
echo '{"role":{"slug":{"alias":"everything","tenant":{"alias":"contoso"}}},"holder":{"slug":{"alias":"account","tenant":{"alias":"contoso"}}}}' \
	| r binding add - >/dev/null
key="$(r key add --tenant contoso --holder account --name e2e --allow '/roster.*/*' 2>/dev/null)"

echo "== serve"
"${work}/roster" --config "${work}/roster.yaml" serve >"${work}/serve.log" 2>&1 &
pids+=($!)
up() {
	for _ in $(seq 1 50); do
		if curl -fsS -o /dev/null "$1" 2>/dev/null; then return 0; fi
		sleep 0.2
	done
	echo "$1 never answered" >&2
	cat "${work}"/*.log >&2
	return 1
}
up "${E2E_CONSOLE}"

# After roster answers: the account app checks each key against the server it
# is handed as it starts, and does not start without one.
"${work}/roster" --config "${work}/roster.yaml" account serve --listen 127.0.0.1:18090 \
	--roster 127.0.0.1:18051 --connect http://127.0.0.1:18052 --insecure \
	--base "${E2E_ACCOUNT}" --static "${__root}/ts/dist/account" \
	--key "contoso=${key}" --insecure-cookie >"${work}/account.log" 2>&1 &
pids+=($!)

up "${E2E_ACCOUNT}/providers"

if [ "${E2E_SANDBOX:-1}" != "0" ]; then
	export E2E_SANDBOX="http://localhost:18100/console/"
	(cd ts && VITE_SANDBOX=1 npx vite --config vite.console.ts --port 18100 --strictPort >"${work}/sandbox.log" 2>&1) &
	pids+=($!)
	up "${E2E_SANDBOX}"
else
	unset E2E_SANDBOX
fi

# `--hold` leaves the deployment up for a browser or a curl, which is how a
# failure the specs report is looked at.
if [ "${1:-}" = "--hold" ]; then
	echo "up: console ${E2E_CONSOLE}, account ${E2E_ACCOUNT} (erin / ${E2E_ERIN_PASSWORD}); admin / ${E2E_OPS_PASSWORD}; ^C stops"
	wait
	exit 0
fi

echo "== playwright"
specs=()
if [ -z "${E2E_SANDBOX:-}" ]; then
	specs+=(--grep-invert sandbox)
fi
(cd ts && npx tsc -p e2e/tsconfig.json && npx playwright test "${specs[@]}" "$@") || {
	echo >&2; echo "-- serve.log" >&2; tail -40 "${work}/serve.log" >&2
	echo "-- account.log" >&2; tail -40 "${work}/account.log" >&2
	exit 1
}
