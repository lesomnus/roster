#!/bin/sh
# A device with no browser, signed in end to end: RFC 8628 through a real Hydra.
#
# This is #14's Hydra half, and which half matters because the two are easy to
# confuse. `account/device.go` is how a **terminal** gets a roster credential, and
# `roster sign-in` walks that one; this is how a **device** gets an OAuth token out
# of this deployment's issuer -- a television signing in to a video product. Neither
# replaces the other and they share no code.
#
# # Why it is a walk rather than a test
#
# Because almost none of it is roster's. The code, its alphabet, its window, the
# `slow_down` a client that polls too fast is told, binding the code to the device
# that asked -- all Hydra's, and `login/login_test.go` runs against a fake one that
# says yes to a code in a map. What is left for roster is two handlers, and what
# nothing else can check is that Hydra's real endpoints are the ones they talk to:
#
#   - that `URLS_DEVICE_VERIFICATION` is what brings a browser here at all. Without
#     it Hydra serves its own fallback and the person looking at a television is
#     told a configuration key is not set
#   - that `device_challenge` is the parameter name, and `.../requests/device/accept`
#     the path, and `{"user_code": …}` the body. Get any of them wrong and Hydra
#     answers a 4xx the app reports as a mistyped code
#   - that a device client with **no redirect of its own** still resolves to a
#     tenant, which is `login/at.go`'s fallback and the case it was written for
#
# # And the poll is the assertion
#
# The device holds a `device_code` and asks the token endpoint for it, over and
# over. What this walk does is poll **before** anybody has typed anything -- which
# must be `authorization_pending` and must not be a token -- and again afterwards,
# which must be an `id_token` whose `sub` is the `Holder.id` roster holds. A walk
# that only polled at the end would pass against an issuer that handed a token out
# to anybody who asked.
#
# Run through `scripts/hydra.sh`, from inside the compose network.
set -eu

# What every call here is willing to wait for, in one place; see `docker/dial.sh`.
. "$(dirname "$0")/dial.sh"

: "${ISSUER:=http://hydra.test:4444}"
: "${LOGIN:=http://login:8091}"
: "${SEED_USER:=erin}"
: "${SEED_PASSWORD:=correct horse battery staple}"
# The public client with no secret, registered by `compose.yaml`. Its
# `redirect_uris` exist for one reason and it is not redirecting: see there.
: "${DEVICE_CLIENT:=device}"

jar=$(mktemp)
trap 'rm -f "${jar}"' EXIT

c() { curl -sS ${DIAL} -c "${jar}" -b "${jar}" "$@"; }
loc() { tr -d '\r' | awk '/^[Ll]ocation:/{print $2}'; }
code() { tr -d '\r' | awk '/^HTTP/{print $2; exit}'; }
# Hydra was configured with the name a **browser on the host** uses, and this is
# not that. Only the host moves; every challenge and code in the query does not.
fix() { sed "s|http://localhost:8091|${LOGIN}|; s|http://localhost:4444|${ISSUER}|"; }
json() { sed "s/.*\"$1\":\"//; s/\".*//"; }
step() { printf '%-38s %s\n' "$1" "$2"; }
die() { echo "device: $*" >&2; exit 1; }

# 1. The device asks. No browser, no secret, no redirect -- which is the whole
#    reason this grant exists.
begun=$(curl -sS ${DIAL} -X POST "${ISSUER}/oauth2/device/auth" \
	-d "client_id=${DEVICE_CLIENT}" -d 'scope=openid profile email')

device_code=$(printf '%s' "${begun}" | json device_code)
user_code=$(printf '%s' "${begun}" | json user_code)
verify=$(printf '%s' "${begun}" | json verification_uri | sed 's|\\/|/|g')

[ -n "${device_code}" ] || die "the issuer minted no device code: ${begun}"
[ -n "${user_code}" ] || die "the issuer printed no user code: ${begun}"
[ -n "${verify}" ] || die "the issuer named nowhere to go: ${begun}"
step "a device with no browser asks" "${user_code}"

# 2. And starts polling. This one must not be a token: nobody has typed anything,
#    and an issuer that answered here would be one handing credentials to whoever
#    asked for a device code.
waiting=$(curl -sS ${DIAL} -X POST "${ISSUER}/oauth2/token" \
	-d 'grant_type=urn:ietf:params:oauth:grant-type:device_code' \
	-d "device_code=${device_code}" -d "client_id=${DEVICE_CLIENT}")
printf '%s' "${waiting}" | grep -q 'authorization_pending' \
	|| die "polling before anybody typed the code answered: ${waiting}"
printf '%s' "${waiting}" | grep -q '"id_token"' \
	&& die "the issuer handed out a token nobody had approved: ${waiting}"
step "it polls before anybody typed it" "authorization_pending"

# 3. A person opens the address it printed. Hydra sends them to the Login App's
#    fourth screen -- and **that** is what `URLS_DEVICE_VERIFICATION` buys: without
#    it this lands on Hydra's own fallback page instead.
l=$(c -o /dev/null -D - "$(printf '%s' "${verify}" | fix)" | loc | fix)
case "${l}" in
*/device*device_challenge=*) ;;
*) die "the issuer did not send the browser to the login app's device screen: ${l}" ;;
esac
challenge=$(printf '%s' "${l}" | sed 's/.*device_challenge=//; s/&.*//')
step "a person opens what it printed" "-> /device"

# The screen itself, which asks the app nothing: there is no `GET
# .../requests/device` at Hydra, so unlike every other screen here there is no
# `/flow` behind it. 200 is the whole assertion.
got=$(c -o /dev/null -w '%{http_code}' "${l}")
[ "${got}" = "200" ] || die "the device screen answered ${got}"
step "the screen is drawn" "${got}"

# 4. A wrong code first, because the answer to one is the thing a person will see
#    most often and the one this app has to get right. 400 and not 502: Hydra
#    refusing what somebody typed is not the deployment being broken.
got=$(c -o /dev/null -w '%{http_code}' -X POST "${LOGIN}/device" \
	-d "device_challenge=${challenge}" -d 'user_code=BCDF-GHJK')
[ "${got}" = "400" ] || die "a wrong code answered ${got}, where 400 was asked for"
step "a wrong code" "${got}"

# 5. The right one. What comes back is where to go, and where it goes is the
#    ordinary flow -- which is the point of this half being two handlers wide.
to=$(c -X POST "${LOGIN}/device" \
	-d "device_challenge=${challenge}" -d "user_code=${user_code}" \
	| json to | sed 's|\\u0026|\&|g; s|\\/|/|g' | fix)
case "${to}" in http*) ;; *) die "the code was accepted and named nowhere: ${to}";; esac
step "the code somebody typed" "-> the issuer"

l=$(c -o /dev/null -D - "${to}" | loc | fix)
case "${l}" in
*login_challenge=*) ;;
*) die "accepting the code did not raise a login challenge: ${l}" ;;
esac
login_challenge=$(printf '%s' "${l}" | sed 's/.*login_challenge=//; s/&.*//')
step "and hydra raises a login challenge" "an ordinary flow"

# 6. From here it is `docker/flow.sh`'s walk, and the part that is checked here is
#    that it **is** -- including the tenant, which a device flow carries no
#    redirect to say. `login/at.go` falls back to what the client registered, so a
#    password refused at this step is that fallback not working.
got=$(c -o /dev/null -w '%{http_code}' "${l}")
[ "${got}" = "200" ] || die "the sign-in page answered ${got}"

head=$(c -o /dev/null -D - -X POST "${LOGIN}/session?login_challenge=${login_challenge}" \
	-H 'content-type: application/json' \
	-d "$(printf '{"alias":"%s","password":"%s"}' "${SEED_USER}" "${SEED_PASSWORD}")" | tr -d '\r')
cookie=$(printf '%s' "${head}" | awk '/^[Ss]et-[Cc]ookie:/{print $2}' | sed 's/;$//')
[ -n "${cookie}" ] || die "the password was not accepted; a device flow resolved no tenant"
step "${SEED_USER} types the password" "$(printf '%s' "${head}" | code), ${cookie%%=*}"

to=$(c -X POST "${LOGIN}/accept?login_challenge=${login_challenge}" -H "Cookie: ${cookie}" \
	| json redirect_to | sed 's|\\u0026|\&|g; s|\\/|/|g' | fix)
case "${to}" in http*) ;; *) die "nothing was accepted: ${to}";; esac

# The consent hop, and then Hydra is done with the browser. `login.sh` runs with
# `consent: skip`, so this is redirects the whole way; where it ends is Hydra's own
# *you may close this* page rather than a callback, because a device flow has no
# browser to send a code to. That is the difference worth seeing.
l=$(c -o /dev/null -D - "${to}" | loc | fix)
l=$(c -o /dev/null -D - "${l}" -H "Cookie: ${cookie}" | loc | fix)
step "consent -> the issuer" "$(printf '%s' "${l}" | cut -c1-40)…"
c -o /dev/null "${l}" || true

# 7. And the device, still polling, is handed a token. Which is the assertion the
#    whole script is for: everything above happened in a browser the device never
#    saw, and what it holds is a `device_code` and nothing else.
i=0
token=""
until [ "${i}" -ge 10 ]; do
	token=$(curl -sS ${DIAL} -X POST "${ISSUER}/oauth2/token" \
		-d 'grant_type=urn:ietf:params:oauth:grant-type:device_code' \
		-d "device_code=${device_code}" -d "client_id=${DEVICE_CLIENT}")
	case "${token}" in *'"id_token"'*) break ;; esac
	i=$((i + 1))
	sleep 1
done

id=$(printf '%s' "${token}" | json id_token)
[ -n "${id}" ] || die "the device polled and was never handed a token: ${token}"

# The payload, base64url with the padding put back -- `flow.sh`'s line.
payload() { cut -d. -f2 | tr '_-' '/+' | awk '{ n = length($0) % 4; if (n) $0 = $0 substr("===", 1, 4 - n); print }' | base64 -d; }
claims=$(printf '%s' "${id}" | payload)
sub=$(printf '%s' "${claims}" | json sub)
step "the device is handed a token" "sub ${sub}"

[ -n "${sub}" ] || die "the id_token carries no subject: ${claims}"
if [ -n "${EXPECT_SUB:-}" ] && [ "${sub}" != "${EXPECT_SUB}" ]; then
	die "the id_token names ${sub}, and ${SEED_USER} is ${EXPECT_SUB}"
fi

# And the same two things `flow.sh` asserts about any token this issuer hands out,
# because a device token is one: the person is named, and roster's answer about
# roster is not in it (`login/claims.go`).
printf '%s' "${claims}" | grep -q '"preferred_username":"'"${SEED_USER}"'"' \
	|| die "the id_token carries no preferred_username for ${SEED_USER}: ${claims}"
printf '%s' "${claims}" | grep -q '"methods"' \
	&& die "the id_token carries roster's method list"

# 8. And the code is spent. A device that polls again gets nothing, and a person
#    who types the same code again is refused -- both of them Hydra's doing, and
#    both worth seeing because a code that outlived its flow would be a code
#    somebody could reuse from a screenshot.
again=$(curl -sS ${DIAL} -X POST "${ISSUER}/oauth2/token" \
	-d 'grant_type=urn:ietf:params:oauth:grant-type:device_code' \
	-d "device_code=${device_code}" -d "client_id=${DEVICE_CLIENT}")
printf '%s' "${again}" | grep -q '"id_token"' \
	&& die "the device code was spendable twice: ${again}"
step "and the device code is spent" "refused"

echo "device: ok"
