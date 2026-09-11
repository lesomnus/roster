#!/bin/sh
# The other shape of relying party, walked: a page that knows nothing about
# authentication, with `oauth2-proxy` in front of it.
#
# `docker/itself.sh` walks our own code -- `examples/product` trusting our
# issuer, which is lenient about our tokens in ways we cannot see from inside,
# and `docker/flow.sh` walks the protocol with `curl`. This walks a **standard
# third party**, and it is here because the deployment of exactly this shape
# produced two defects every local gate was green on:
#
#   - a sign-out that ended on Hydra's own fallback page, whose text tells the
#     person who clicked it to contact an administrator
#   - a session the page could read nothing out of
#
# Both are about the proxy and neither is reachable from `flow.sh`.
#
# Run through `scripts/hydra.sh`, from inside the compose network.
# `docs/relying-party.md` is the shape of both demos and what each assertion
# here is for.
set -eu

: "${ISSUER:=http://hydra.test:4444}"
: "${SEED_USER:=erin}"
: "${SEED_PASSWORD:=correct horse battery staple}"

behind=$(getent hosts behind | awk '{print $1; exit}')
login=$(getent hosts login | awk '{print $1; exit}')
hydra=$(getent hosts hydra | awk '{print $1; exit}')
[ -n "${behind}" ] && [ -n "${login}" ] && [ -n "${hydra}" ] || {
	echo "behind: the three are not all up" >&2
	exit 1
}

i=0
until curl -sS -o /dev/null "http://behind:4180/ping" 2>/dev/null; do
	i=$((i + 1))
	[ "${i}" -lt 60 ] || { echo "behind: the proxy never answered" >&2; exit 1; }
	sleep 1
done

jar=$(mktemp)
trap 'rm -f "${jar}"' EXIT

c() { curl -sS -c "${jar}" -b "${jar}" "$@"; }
loc() { tr -d '\r' | awk '/^[Ll]ocation:/{print $2}'; }
code() { tr -d '\r' | awk '/^HTTP/{print $2; exit}'; }
# Only the Login App's host moves: the issuer already answers to a name this
# network resolves (`ISSUER_HOST` in `compose.yaml`), and it has to -- the
# browser's session cookie is scoped to whatever host it was set on, so a walk
# that reached the same Hydra under two names would be two browsers.
fix() { sed "s|http://localhost:8091|http://login:8091|; s|http://localhost:4444|${ISSUER}|"; }
die() { echo "behind: $*" >&2; exit 1; }
step() { printf '%-38s %s\n' "$1" "$2"; }

# A page nobody is signed in for. The proxy sends the browser to the issuer,
# which sends it to the Login App, which asks roster.
l=$(c -o /dev/null -D - "http://behind:4180/" | loc)
case "${l}" in
*/oauth2/start*) l=$(c -o /dev/null -D - "http://behind:4180${l}" | loc) ;;
esac
case "${l}" in
*/oauth2/auth*) ;;
*) die "the proxy did not send the browser to the issuer: ${l}" ;;
esac
step "a page nobody is signed in for" "-> the issuer"

l=$(c -o /dev/null -D - "$(printf '%s' "${l}" | fix)" | loc | fix)
challenge=$(printf '%s' "${l}" | sed 's/.*login_challenge=//')
[ -n "${challenge}" ] || die "hydra raised no login challenge: ${l}"

head=$(c -o /dev/null -D - -X POST "http://login:8091/session?login_challenge=${challenge}" \
	-H 'content-type: application/json' \
	-d "$(printf '{"alias":"%s","password":"%s"}' "${SEED_USER}" "${SEED_PASSWORD}")" | tr -d '\r')
cookie=$(printf '%s' "${head}" | awk '/^[Ss]et-[Cc]ookie:/{print $2}' | sed 's/;$//')
[ -n "${cookie}" ] || die "the password was not accepted"

to=$(c -X POST "http://login:8091/accept?login_challenge=${challenge}" -H "Cookie: ${cookie}" \
	| sed 's/.*"redirect_to":"//; s/".*//; s|\\u0026|\&|g' | fix)
l=$(c -o /dev/null -D - "${to}" | loc | fix)
l=$(c -o /dev/null -D - "${l}" -H "Cookie: ${cookie}" | loc | fix)
step "${SEED_USER} signs in" "-> back to the proxy"

# The callback, and then the page.
l=$(c -o /dev/null -D - "${l}" | loc)
case "${l}" in
http*) l=$(c -o /dev/null -D - "${l}" | loc) ;;
esac
got=$(c -o /dev/null -D - "http://behind:4180/" | code)
[ "${got}" = "200" ] || die "the page did not open for a browser that signed in (${got})"
step "and the page opens" "${got}"

# **What the session says**, which is the half a page draws and the half that
# was empty in the cluster. It is the proxy's own endpoint over the proxy's own
# cookie: no call to roster, because the point is what a product ends up
# knowing, which is the token and nothing else.
who=$(c -w '\n%{http_code}' "http://behind:4180/oauth2/userinfo")
got=$(printf '%s' "${who}" | tail -1)
who=$(printf '%s' "${who}" | sed '$d')
[ "${got}" = "200" ] || die "the session says nothing about who is signed in (${got}): ${who}"
printf '%s' "${who}" | grep -q '"user"' || die "there is no identifier in the session: ${who}"
printf '%s' "${who}" | grep -qE '"user":"[^"]+"' || die "the identifier in the session is empty: ${who}"
step "and the session names somebody" "$(printf '%s' "${who}" | cut -c1-40)…"

# The asymmetry a page in front of this has to cope with, and the one that got
# reported as *signed in, but the session shows nothing*.
#
# A browser with no session is answered two different ways by the same proxy: a
# **page** is a redirect, and since the issuer remembers the browser the whole
# chain is silent and the page comes back looking as it did -- while a **fetch**
# for the session is a bare 401. So a page that outlives its session, from the
# cache or the back button, draws *signed in* over an empty answer, and the
# only thing that looks broken is the part that is working.
#
# Written down here because the fix for it lives in whatever page is in front,
# and a page cannot be written against a rule nobody stated.
away=$(mktemp)
got=$(curl -sS -c "${away}" -b "${away}" -o /dev/null -w '%{http_code}' "http://behind:4180/oauth2/userinfo")
[ "${got}" = "401" ] || die "a fetch with no session answered ${got}, not 401"
l=$(curl -sS -c "${away}" -b "${away}" -o /dev/null -D - "http://behind:4180/" | loc)
case "${l}" in
*/oauth2/*) ;;
*) die "a page with no session was not sent to sign in: ${l}" ;;
esac
rm -f "${away}"
step "a page and a fetch, with no session" "a redirect and a 401"

# Signing out, which for this shape is two hops and no `id_token_hint`: the
# proxy ends its own session and sends the browser on to the issuer, and
# `oauth2-proxy` has no way to put the token on that link. So Hydra marks the
# logout not rp-initiated, the Login App draws the confirmation, and the last
# page is whatever `urls.post_logout_redirect` names.
end=$(printf '%s/oauth2/sessions/logout?client_id=behind' "${ISSUER}" | sed 's|:|%3A|g; s|/|%2F|g; s|?|%3F|g; s|=|%3D|g')
l=$(c -o /dev/null -D - "http://behind:4180/oauth2/sign_out?rd=${end}" | loc | fix)
case "${l}" in
*/oauth2/sessions/logout*) ;;
*) die "the proxy did not send the browser on to the issuer: ${l}" ;;
esac
step "the person signs out" "-> the issuer"

l=$(c -o /dev/null -D - "${l}" | loc | fix)
case "${l}" in
*/logout?logout_challenge=*) ;;
*) die "hydra did not ask the login app about it: ${l}" ;;
esac
leaving=$(printf '%s' "${l}" | sed 's/.*logout_challenge=//')

got=$(c -o /dev/null -w '%{http_code}' "${l}")
[ "${got}" = "200" ] || die "the confirmation was not drawn (${got})"
step "  and is asked to confirm" "${got}"

said=$(c -X POST "http://login:8091/logout" -d "logout_challenge=${leaving}" -d "allow=1")
case "${said}" in
*'"signed_out":true'*) ;;
*) die "the confirmation was not accepted: ${said}" ;;
esac

l=$(printf '%s' "${said}" | sed 's/.*"to":"//; s/".*//; s|\\u0026|\&|g' | fix)
l=$(c -o /dev/null -D - "${l}" | loc | fix)

# Where a person ends up, which is the line that was a URL nobody read.
case "${l}" in
*/signed-out*) ;;
*fallback*) die "a finished sign-out ends on hydra's fallback page: ${l}" ;;
*) die "a finished sign-out did not end anywhere this deployment named: ${l}" ;;
esac
got=$(c -o /dev/null -w '%{http_code}' "${l}")
[ "${got}" = "200" ] || die "the page a sign-out ends on answered ${got}"
step "  and lands on a page for a person" "${got}"

# And the whole of what the second hop is for: the issuer has forgotten this
# browser, so opening the page again is a form and not a silent sign-in.
l=$(c -o /dev/null -D - "http://behind:4180/" | loc)
case "${l}" in
*/oauth2/start*) l=$(c -o /dev/null -D - "http://behind:4180${l}" | loc) ;;
esac
l=$(c -o /dev/null -D - "$(printf '%s' "${l}" | fix)" | loc | fix)
got=$(c -o /dev/null -D - "${l}" | code)
[ "${got}" = "200" ] || die "the issuer still remembers her after a sign-out (${got})"
step "and the form is asked for again" "${got}"

echo "behind: ok"
