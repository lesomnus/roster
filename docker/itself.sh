#!/bin/sh
# The app that is the relying party **itself**, walked end to end.
#
# `docker/behind.sh` walks a page with a proxy in front; this walks one that
# holds the token, keeps a session of its own, and asks the issuer to end it.
# Both shapes are deployed and they fail differently.
#
# It exists because nothing ran this app. Its own tests use a fake IdP, and
# `docker/flow.sh` walks the protocol with `curl` rather than with the app -- it
# builds the end-session URL by hand out of the same values it registered the
# client with, so it agrees with itself by construction. What was never checked
# is **the URL this app builds**, and the first one that was wrong was found by
# a person clicking sign out in a cluster:
#
#   Logout failed because query parameter post_logout_redirect_uri is not a
#   whitelisted as a post_logout_redirect_uri for the client.
#
# The app asks to come back to its own origin; the client had only its callback
# registered, because that is all the template in the deployment's README wrote.
#
# Run through `scripts/hydra.sh`, from inside the compose network.
set -eu

# Three addresses and nothing about where they are. The compose network is the
# default; `scripts/cluster.sh` runs this same script as a Job inside a cluster,
# where they are Services and the issuer is https on a certificate that Job is
# handed. A walk that only knew one of those would only ever check one of them.
: "${ISSUER:=http://hydra.test:4444}"
: "${LOGIN:=http://login:8091}"
: "${BASE:=http://product:5555}"
: "${SEED_USER:=erin}"
: "${SEED_PASSWORD:=correct horse battery staple}"

i=0
until curl -sS -o /dev/null "${BASE}/healthz" 2>/dev/null; do
	i=$((i + 1))
	[ "${i}" -lt 60 ] || { echo "itself: the app never answered" >&2; exit 1; }
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
fix() { sed "s|http://localhost:8091|${LOGIN}|; s|http://localhost:4444|${ISSUER}|"; }
die() { echo "itself: $*" >&2; exit 1; }
step() { printf '%-38s %s\n' "$1" "$2"; }

# The app sends a browser nobody is signed in for to the issuer.
l=$(c -o /dev/null -D - "${BASE}/" | loc | fix)
case "${l}" in
*/oauth2/auth*) ;;
*) die "the app did not send the browser to the issuer: ${l}" ;;
esac
step "a page nobody is signed in for" "-> the issuer"

l=$(c -o /dev/null -D - "${l}" | loc | fix)
challenge=$(printf '%s' "${l}" | sed 's/.*login_challenge=//')
[ -n "${challenge}" ] || die "hydra raised no login challenge: ${l}"

head=$(c -o /dev/null -D - -X POST "${LOGIN}/session?login_challenge=${challenge}" \
	-H 'content-type: application/json' \
	-d "$(printf '{"alias":"%s","password":"%s"}' "${SEED_USER}" "${SEED_PASSWORD}")" | tr -d '\r')
cookie=$(printf '%s' "${head}" | awk '/^[Ss]et-[Cc]ookie:/{print $2}' | sed 's/;$//')
[ -n "${cookie}" ] || die "the password was not accepted"

to=$(c -X POST "${LOGIN}/accept?login_challenge=${challenge}" -H "Cookie: ${cookie}" \
	| sed 's/.*"redirect_to":"//; s/".*//; s|\\u0026|\&|g' | fix)
l=$(c -o /dev/null -D - "${to}" | loc | fix)
l=$(c -o /dev/null -D - "${l}" -H "Cookie: ${cookie}" | loc | fix)
l=$(c -o /dev/null -D - "${l}" | loc | fix)
case "${l}" in
*error=*)
	# **The issuer's refusals come back to the app's own callback**, so the
	# host is not enough to say a flow worked. Checked for the host alone,
	# this read an `?error=` as a success and handed the app a callback with
	# no code in it -- and what came out was a 400 from the app about an
	# authorization code the issuer had never issued. The real sentence was in
	# the issuer's log the whole time.
	die "the issuer refused the flow: $(printf '%s' "${l}" | sed 's/.*error_description=//; s/&.*//' | sed 's/+/ /g; s/%20/ /g; s/%27/'"'"'/g; s/%2C/,/g; s/%3A/:/g; s/%2F/\//g')" ;;
"${BASE}"*code=*) ;;
*) die "the code did not come back to the app: ${l}" ;;
esac
step "${SEED_USER} signs in" "-> back to the app"

# The callback, and then the page it sends the browser to.
#
# Its **status** first, because everything this app refuses it refuses with a
# 400 and one word, so a walk that only followed the redirect read a missing
# `Location` as an empty URL and said `curl: option : blank argument`. What
# actually happened is in the app's log; what it costs to find out is what this
# line saves.
head=$(c -o /dev/null -D - "${l}" | tr -d '\r')
got=$(printf '%s' "${head}" | code)
[ "${got}" = "303" ] || die "the callback answered ${got}; the app's log says which of its checks refused it"
l=$(printf '%s' "${head}" | loc)
case "${l}" in
/*) l="${BASE}${l}" ;;
esac
page=$(c "${l}")
printf '%s' "${page}" | grep -q "${SEED_USER}" \
	|| die "the app's page says nothing about who signed in: $(printf '%s' "${page}" | head -c 200)"
step "and the page names her" "${SEED_USER}"

# **The sign-out this app builds itself**, which is the half nothing checked.
# Two hops: the app ends its own session and hands the browser to the issuer
# with the token it kept for exactly this.
l=$(c -o /dev/null -D - "${BASE}/sign-out" | loc | fix)
case "${l}" in
*/oauth2/sessions/logout*) ;;
*) die "signing out did not reach the issuer: ${l}" ;;
esac
printf '%s' "${l}" | grep -q 'id_token_hint=' \
	|| die "the app sent no id_token_hint, so it cannot ask to come back: ${l}"
step "she signs out" "-> the issuer, with the hint"

l=$(c -o /dev/null -D - "${l}" | loc | fix)
case "${l}" in
*error=*)
	# Hydra's own words, and worth printing whole: this is where a registration
	# that does not match what the app asks for says so.
	die "the issuer refused the sign-out: $(printf '%s' "${l}" | sed 's/.*error_description=//' | sed 's/+/ /g; s/%2C/,/g; s/%20/ /g')"
	;;
*/logout?logout_challenge=*) ;;
*) die "hydra did not ask the login app about the logout: ${l}" ;;
esac

# With a hint there is nothing to confirm, so the app answers and Hydra sends
# the browser on -- to the app's own origin, because it asked and was allowed.
l=$(c -o /dev/null -D - "${l}" | loc | fix)
case "${l}" in
*logout_verifier=*) ;;
*) die "the login app did not send the browser back to hydra: ${l}" ;;
esac
step "  the login app says yes" "hydra verifies it"

l=$(c -o /dev/null -D - "${l}" | loc)
case "${l}" in
"${BASE}"*) ;;
*fallback*) die "the sign-out ended on the issuer's own page rather than the app's: ${l}" ;;
*) die "the sign-out did not come back to the app: ${l}" ;;
esac
step "  and lands back on the app" "$(printf '%s' "${l}" | cut -c1-40)"

# And the whole of what the second hop is for.
l=$(c -o /dev/null -D - "${BASE}/" | loc | fix)
case "${l}" in
*/oauth2/auth*) ;;
*) die "the app did not send the browser to sign in again: ${l}" ;;
esac
l=$(c -o /dev/null -D - "${l}" | loc | fix)
got=$(c -o /dev/null -D - "${l}" | code)
[ "${got}" = "200" ] || die "the issuer still remembers her after a sign-out (${got})"
step "and the form is asked for again" "${got}"

echo "itself: ok"
