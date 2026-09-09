#!/bin/sh
# One authorization-code flow, end to end, from inside the compose network.
#
# What it checks is the half no unit test can: that a **real** Hydra's
# redirects, challenges and cookies fit the Login App. `login/login_test.go`
# runs the same flow against a fake, which is right for what that test is about
# -- which operator a challenge resolves to, and what ends up in the token --
# and wrong for the protocol around it. Two things were wrong here that were
# green there: the challenge does not fit in a cookie, and the app's own
# environment variables were reported as typos.
#
# Run through `scripts/hydra.sh`, which stands the deployment up first.
#
# Inside the network rather than against published ports, so it needs nothing
# exposed and works wherever the engine is. `--resolve` gives the two services
# names with a dot in them, because a cookie jar will not answer to a
# single-label host and this walk is mostly cookies.
set -eu

: "${SEED_USER:=erin}"
: "${SEED_PASSWORD:=correct horse battery staple}"
: "${OAUTH_CLIENT:=demo}"
: "${CLIENT_SECRET:=demo-secret}"
: "${CALLBACK:=http://127.0.0.1:5555/callback}"

login=$(getent hosts login | awk '{print $1; exit}')
hydra=$(getent hosts hydra | awk '{print $1; exit}')
[ -n "${login}" ] && [ -n "${hydra}" ] || { echo "flow: login and hydra are not both up" >&2; exit 1; }

# The app answers when it has its key and Hydra is up, which `login.sh` waits
# for inside the container -- so a container that is running is not yet an app
# that is serving. Waited for here rather than assumed, because the failure
# otherwise is a refused password and a message about the wrong thing.
i=0
until curl -sS -o /dev/null "http://${login}:8091/" 2>/dev/null; do
	i=$((i + 1))
	[ "${i}" -lt 60 ] || { echo "flow: the login app never answered" >&2; exit 1; }
	sleep 1
done

jar=$(mktemp)
trap 'rm -f "${jar}"' EXIT
resolve="--resolve login.test:8091:${login} --resolve hydra.test:4444:${hydra}"

c() { curl -sS ${resolve} -c "${jar}" -b "${jar}" "$@"; }
loc() { tr -d '\r' | awk '/^[Ll]ocation:/{print $2}'; }
# What Hydra was told to redirect to is a name a browser resolves, and this is
# not a browser. Only the host moves; the challenge in the query does not.
fix() { sed 's|http://localhost:8091|http://login.test:8091|; s|http://localhost:4444|http://hydra.test:4444|'; }
step() { printf '%-38s %s\n' "$1" "$2"; }

authorize="http://hydra.test:4444/oauth2/auth?client_id=${OAUTH_CLIENT}&response_type=code&scope=openid+profile+email&redirect_uri=$(printf '%s' "${CALLBACK}" | sed 's|:|%3A|g; s|/|%2F|g')&state=abcdefghijklmnopqrst"

l=$(c -o /dev/null -D - "${authorize}" | loc)
challenge=$(printf '%s' "${l}" | sed 's/.*login_challenge=//')
[ -n "${challenge}" ] || { echo "flow: hydra raised no login challenge" >&2; exit 1; }
step "the product sends a browser to hydra" "$(printf '%s' "${l}" | cut -c1-40)…"

code=$(c -o /dev/null -w '%{http_code}' "$(printf '%s' "${l}" | fix)")
[ "${code}" = "200" ] || { echo "flow: the sign-in page answered ${code}" >&2; exit 1; }
step "hydra -> the login app, and a form" "${code}"

# The cookie by hand for the rest, because a jar will not send one to a host
# `--resolve` invented. Everything else about the walk is the browser's.
cookie=$(c -o /dev/null -D - -X POST "http://login.test:8091/session?login_challenge=${challenge}" \
	-H 'content-type: application/json' \
	-d "$(printf '{"alias":"%s","password":"%s"}' "${SEED_USER}" "${SEED_PASSWORD}")" \
	| tr -d '\r' | awk '/^[Ss]et-[Cc]ookie:/{print $2}' | sed 's/;$//')
[ -n "${cookie}" ] || { echo "flow: the password was not accepted" >&2; exit 1; }
step "${SEED_USER} types the password" "204, ${cookie%%=*}"

to=$(c -X POST "http://login.test:8091/accept?login_challenge=${challenge}" -H "Cookie: ${cookie}" \
	| sed 's/.*"redirect_to":"//; s/".*//; s|\\u0026|\&|g' | fix)
case "${to}" in http*) ;; *) echo "flow: nothing was accepted: ${to}" >&2; exit 1;; esac
step "hydra is told the subject" "$(printf '%s' "${to}" | cut -c1-40)…"

l=$(c -o /dev/null -D - "${to}" | loc | fix)
step "hydra -> consent" "$(printf '%s' "${l}" | cut -c1-40)…"
l=$(c -o /dev/null -D - "${l}" -H "Cookie: ${cookie}" | loc | fix)
step "consent -> hydra" "$(printf '%s' "${l}" | cut -c1-40)…"
l=$(c -o /dev/null -D - "${l}" | loc)
step "the code, at the product's callback" "$(printf '%s' "${l}" | cut -c1-40)…"

grant=$(printf '%s' "${l}" | sed 's/.*[?&]code=//; s/&.*//')
[ -n "${grant}" ] || { echo "flow: no authorization code came back: ${l}" >&2; exit 1; }

token=$(c -X POST http://hydra.test:4444/oauth2/token \
	-d grant_type=authorization_code -d "code=${grant}" \
	-d "redirect_uri=${CALLBACK}" -d "client_id=${OAUTH_CLIENT}" -d "client_secret=${CLIENT_SECRET}")
id=$(printf '%s' "${token}" | sed 's/.*"id_token":"//; s/".*//')
[ -n "${id}" ] || { echo "flow: no id_token: ${token}" >&2; exit 1; }

# The payload, base64url with the padding put back.
claims=$(printf '%s' "${id}" | cut -d. -f2 | tr '_-' '/+' | awk '{ n = length($0) % 4; if (n) $0 = $0 substr("===", 1, 4 - n); print }' | base64 -d)
sub=$(printf '%s' "${claims}" | sed 's/.*"sub":"//; s/".*//')
step "the id_token's sub" "${sub}"

# The whole point, and the sentence `docs/position.md` makes: the identifier
# every product now trusts is a row in roster.
[ -n "${sub}" ] || { echo "flow: the id_token carries no subject" >&2; exit 1; }
if [ -n "${EXPECT_SUB:-}" ] && [ "${sub}" != "${EXPECT_SUB}" ]; then
	echo "flow: the id_token names ${sub}, and ${SEED_USER} is ${EXPECT_SUB}" >&2
	exit 1
fi

printf '%s' "${claims}" | grep -q '"preferred_username":"'"${SEED_USER}"'"' || {
	echo "flow: the id_token carries no preferred_username for ${SEED_USER}: ${claims}" >&2
	exit 1
}

# And what must never be in one: roster's answer about roster, which a product
# holding a copy of would hold a stale one. `login/claims.go` says why.
if printf '%s' "${claims}" | grep -q '"methods"'; then
	echo "flow: the id_token carries roster's method list" >&2
	exit 1
fi

echo "flow: ok"
