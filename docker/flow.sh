#!/bin/sh
# One authorization-code flow, end to end, from inside the compose network --
# and then the two things around it that only a real Hydra has.
#
# What it checks is the half no unit test can: that a **real** Hydra's
# redirects, challenges, cookies and remembered sessions fit the Login App.
# `login/login_test.go` runs the same flow against a fake, which is right for
# what that test is about -- which operator a challenge resolves to, and what
# ends up in the token -- and wrong for the protocol around it. Three things
# were wrong here that were green there: the challenge does not fit in a cookie,
# the app's own environment variables were reported as typos, and a one-shot
# recreated the deployment under the walk.
#
# Run through `scripts/hydra.sh`, which stands the deployment up first.
#
# Inside the network rather than against published ports, so it needs nothing
# exposed and works wherever the engine is. `--resolve` gives the two services
# names with a dot in them, because a cookie jar will not answer to a
# single-label host and this walk is mostly cookies.
set -eu

: "${SEED_CUSTOMER:=contoso}"
: "${SEED_USER:=erin}"
: "${SEED_PASSWORD:=correct horse battery staple}"
: "${OAUTH_CLIENT:=demo}"
: "${CLIENT_SECRET:=demo-secret}"
: "${CALLBACK:=http://127.0.0.1:5555/callback}"
: "${CONSENT:=skip}"

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

# A browser Hydra remembers nothing about.
#
# `remember` is on (`login.sh`), which is what makes the sign-out below
# observable at all -- and it applies to the **consent** too, so once somebody
# has allowed a client Hydra grants it again without asking. That is the right
# behaviour and it would make the `ask` walk see a redirect where it expects a
# screen, so this run starts by forgetting what an earlier one agreed to. A new
# jar is not enough: the memory is Hydra's, keyed on the subject.
if [ "${CONSENT}" = "ask" ] && [ -n "${EXPECT_SUB:-}" ]; then
	curl -sS -o /dev/null -X DELETE \
		"http://hydra:4445/admin/oauth2/auth/sessions/consent?subject=${EXPECT_SUB}&all=true" || true
fi
resolve="--resolve login.test:8091:${login} --resolve hydra.test:4444:${hydra}"

c() { curl -sS ${resolve} -c "${jar}" -b "${jar}" "$@"; }
loc() { tr -d '\r' | awk '/^[Ll]ocation:/{print $2}'; }
code() { tr -d '\r' | awk '/^HTTP/{print $2; exit}'; }
# What Hydra was told to redirect to is a name a browser resolves, and this is
# not a browser. Only the host moves; the challenge in the query does not.
fix() { sed 's|http://localhost:8091|http://login.test:8091|; s|http://localhost:4444|http://hydra.test:4444|'; }
step() { printf '%-38s %s\n' "$1" "$2"; }
die() { echo "flow: $1" >&2; exit 1; }

authorize="http://hydra.test:4444/oauth2/auth?client_id=${OAUTH_CLIENT}&response_type=code&scope=openid+profile+email&redirect_uri=$(printf '%s' "${CALLBACK}" | sed 's|:|%3A|g; s|/|%2F|g')&state=abcdefghijklmnopqrst"

# begin is a product sending a browser to Hydra, as far as the login app's door.
# It answers the challenge, and leaves the status in `began`.
began=""
begin() {
	l=$(c -o /dev/null -D - "${authorize}" | loc)
	challenge=$(printf '%s' "${l}" | sed 's/.*login_challenge=//')
	[ -n "${challenge}" ] || die "hydra raised no login challenge"

	began=$(c -o /dev/null -D - "$(printf '%s' "${l}" | fix)" | code)
}

begin
[ "${began}" = "200" ] || die "the sign-in page answered ${began}"
step "the product sends a browser to hydra" "a form, ${began}"

# The cookie by hand for the rest, because a jar will not send one to a host
# `--resolve` invented. Everything else about the walk is the browser's.
cookie=$(c -o /dev/null -D - -X POST "http://login.test:8091/session?login_challenge=${challenge}" \
	-H 'content-type: application/json' \
	-d "$(printf '{"alias":"%s","password":"%s"}' "${SEED_USER}" "${SEED_PASSWORD}")" \
	| tr -d '\r' | awk '/^[Ss]et-[Cc]ookie:/{print $2}' | sed 's/;$//')
[ -n "${cookie}" ] || die "the password was not accepted"
step "${SEED_USER} types the password" "204, ${cookie%%=*}"

to=$(c -X POST "http://login.test:8091/accept?login_challenge=${challenge}" -H "Cookie: ${cookie}" \
	| sed 's/.*"redirect_to":"//; s/".*//; s|\\u0026|\&|g' | fix)
case "${to}" in http*) ;; *) die "nothing was accepted: ${to}";; esac
step "hydra is told the subject" "$(printf '%s' "${to}" | cut -c1-40)…"

l=$(c -o /dev/null -D - "${to}" | loc | fix)
step "hydra -> consent" "$(printf '%s' "${l}" | cut -c1-40)…"

# The consent hop, and the two shapes it has. `skip` is a redirect and nothing
# drawn; `ask` is a page that grants nothing until it is answered -- which is
# the half a unit test cannot check against a real Hydra's challenge.
if [ "${CONSENT}" = "ask" ]; then
	consent=$(printf '%s' "${l}" | sed 's/.*consent_challenge=//')
	page=$(c "${l}" -H "Cookie: ${cookie}")
	printf '%s' "${page}" | grep -q 'name="allow"' || die "consent=ask drew no screen: ${page}"
	printf '%s' "${page}" | grep -q "${OAUTH_CLIENT}" || die "the screen does not say which app is asking"
	step "consent, drawn and not yet granted" "a screen"

	l=$(c -o /dev/null -D - -X POST "http://login.test:8091/consent" -H "Cookie: ${cookie}" \
		-d "consent_challenge=${consent}" -d "allow=1" | loc | fix)
	step "somebody says allow" "$(printf '%s' "${l}" | cut -c1-40)…"
else
	l=$(c -o /dev/null -D - "${l}" -H "Cookie: ${cookie}" | loc | fix)
	step "consent -> hydra" "$(printf '%s' "${l}" | cut -c1-40)…"
fi

l=$(c -o /dev/null -D - "${l}" | loc)
step "the code, at the product's callback" "$(printf '%s' "${l}" | cut -c1-40)…"

grant=$(printf '%s' "${l}" | sed 's/.*[?&]code=//; s/&.*//')
[ -n "${grant}" ] || die "no authorization code came back: ${l}"

token=$(c -X POST http://hydra.test:4444/oauth2/token \
	-d grant_type=authorization_code -d "code=${grant}" \
	-d "redirect_uri=${CALLBACK}" -d "client_id=${OAUTH_CLIENT}" -d "client_secret=${CLIENT_SECRET}")
id=$(printf '%s' "${token}" | sed 's/.*"id_token":"//; s/".*//')
[ -n "${id}" ] || die "no id_token: ${token}"

# The payload, base64url with the padding put back.
claims=$(printf '%s' "${id}" | cut -d. -f2 | tr '_-' '/+' | awk '{ n = length($0) % 4; if (n) $0 = $0 substr("===", 1, 4 - n); print }' | base64 -d)
sub=$(printf '%s' "${claims}" | sed 's/.*"sub":"//; s/".*//')
step "the id_token's sub" "${sub}"

# The whole point, and the sentence `docs/position.md` makes: the identifier
# every product now trusts is a row in roster.
[ -n "${sub}" ] || die "the id_token carries no subject"
if [ -n "${EXPECT_SUB:-}" ] && [ "${sub}" != "${EXPECT_SUB}" ]; then
	die "the id_token names ${sub}, and ${SEED_USER} is ${EXPECT_SUB}"
fi

printf '%s' "${claims}" | grep -q '"preferred_username":"'"${SEED_USER}"'"' \
	|| die "the id_token carries no preferred_username for ${SEED_USER}: ${claims}"

# And what must never be in one: roster's answer about roster, which a product
# holding a copy of would hold a stale one. `login/claims.go` says why.
printf '%s' "${claims}" | grep -q '"methods"' \
	&& die "the id_token carries roster's method list"

# What roster's sign-out is worth, which needs Hydra to be remembering something
# in the first place -- `login.sh` sets `remember`, and this is why.
if [ -z "${INVALIDATE_KEY:-}" ]; then
	echo "flow: ok"
	exit 0
fi

begin
[ "${began}" = "303" ] || die "hydra asked for the form again for a browser it remembers (${began})"
step "a second flow skips the form" "${began}, remembered"

# Over roster's own HTTP port, as a caller with a key -- which is how anything
# that is not gRPC reaches it, and one more thing this walk gets for free.
#
# `connect-protocol-version` because a call is what **says so** and is addressed
# to a service: without it the request is a page's route as far as the
# transcoder is concerned, and the answer is 404 rather than a refusal.
out=$(c -o /dev/null -w '%{http_code}' -X POST "http://roster:8080/roster.HolderService/Invalidate" \
	-H "authorization: Bearer ${INVALIDATE_KEY}" -H 'content-type: application/json' \
	-H 'connect-protocol-version: 1' \
	-d "$(printf '{"ref":{"slug":{"alias":"%s","tenant":{"alias":"%s"}}}}' "${SEED_USER}" "${SEED_CUSTOMER}")")
[ "${out}" = "200" ] || die "roster refused the sign-out: ${out}"
step "roster signs ${SEED_USER} out everywhere" "${out}"

# And the Login App carries it across. `SyncService` is a stream, so this is the
# one place a moment has to pass -- and what it is waiting for is a fact about
# Hydra rather than about roster, which is why it is asked of Hydra.
i=0
until begin; [ "${began}" = "200" ]; do
	i=$((i + 1))
	[ "${i}" -lt 30 ] || die "hydra still remembers her; the sync stream did not reach it"
	sleep 1
done
step "and hydra asks for the form again" "${began}"

echo "flow: ok"
