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
forget_consent() {
	if [ "${CONSENT}" = "ask" ] && [ -n "${EXPECT_SUB:-}" ]; then
		curl -sS -o /dev/null -X DELETE \
			"http://hydra:4445/admin/oauth2/auth/sessions/consent?subject=${EXPECT_SUB}&all=true" || true
	fi
}
forget_consent
resolve="--resolve login.test:8091:${login} --resolve hydra.test:4444:${hydra}"

c() { curl -sS ${resolve} -c "${jar}" -b "${jar}" "$@"; }
loc() { tr -d '\r' | awk '/^[Ll]ocation:/{print $2}'; }
code() { tr -d '\r' | awk '/^HTTP/{print $2; exit}'; }
# What Hydra was told to redirect to is a name a browser resolves, and this is
# not a browser. Only the host moves; the challenge in the query does not.
fix() { sed 's|http://localhost:8091|http://login.test:8091|; s|http://localhost:4444|http://hydra.test:4444|'; }
step() { printf '%-38s %s\n' "$1" "$2"; }
die() { echo "flow: $1" >&2; exit 1; }

# One call to roster, over its own HTTP port, as a caller with a key -- which is
# how anything that is not gRPC reaches it. `connect-protocol-version` because a
# call is what **says so** and is addressed to a service: without it the
# transcoder reads the request as a page's route and answers 404.
rpc() {
	curl -sS -X POST "http://roster:8080/roster.$1" \
		-H "authorization: Bearer ${INVALIDATE_KEY}" \
		-H 'content-type: application/json' -H 'connect-protocol-version: 1' \
		-d "$2"
}
json() { sed "s/.*\"$1\":\"//; s/\".*//"; }

authorize="http://hydra.test:4444/oauth2/auth?client_id=${OAUTH_CLIENT}&response_type=code&scope=openid+profile+email&redirect_uri=$(printf '%s' "${CALLBACK}" | sed 's|:|%3A|g; s|/|%2F|g')&state=abcdefghijklmnopqrst"

# begin is a product sending a browser to Hydra, as far as the login app's door.
# It answers the challenge, and leaves the status in `began`.
began=""
begin() {
	l=$(c -o /dev/null -D - "${authorize}" | loc)
	challenge=$(printf '%s' "${l}" | sed 's/.*login_challenge=//')
	[ -n "${challenge}" ] || die "hydra raised no login challenge"

	# The answer once, read twice: a challenge is single-use, so asking again to
	# find out where it went would be asking about one that has been spent.
	head=$(c -o /dev/null -D - "$(printf '%s' "${l}" | fix)" | tr -d '\r')
	began=$(printf '%s' "${head}" | code)
	went=$(printf '%s' "${head}" | awk '/^[Ll]ocation:/{print $2}' | fix)
}

# An authenticator on her account, when this walk is the one about second
# factors. `who` names no tenant on purpose: the key is one, which is what
# `Vouch` reads it from.
if [ "${FACTOR:-}" = "totp" ]; then
	[ -n "${INVALIDATE_KEY:-}" ] || die "a factor needs a key to enrol it with"

	seed=$(rpc CredentialService/Enrol \
		"$(printf '{"ref":{"slug":{"alias":"%s","tenant":{"alias":"%s"}}},"kind":"totp"}' "${SEED_USER}" "${SEED_CUSTOMER}")" \
		| json seed)
	case "${seed}" in "") die "roster enrolled no authenticator";; esac

	# The **previous** window, so the current code stays unspent for the sign-in
	# a moment later: a step that has been spent does not work twice, which is
	# roster's replay rule and would otherwise refuse the right code.
	#
	# base64 because `secret` is a protobuf `bytes` and this is JSON. Sent as
	# the six digits it looks like, it is decoded into something else and
	# compared against the real code, which answers *nobody* -- the same answer
	# a wrong password gets, and about a different thing.
	#
	# And `who` names no tenant, which the key already does. That is the rule
	# `proto/app/vouch.proto` states and this is a caller relying on it.
	out=$(rpc VouchService/Verify \
		"$(printf '{"who":{"alias":"%s"},"kind":"totp","secret":"%s"}' \
			"${SEED_USER}" "$(printf '%s' "$(oathtool --totp -b -N '-30 seconds' "${seed}")" | base64)")")
	printf '%s' "${out}" | grep -q '"satisfied":\["totp"\]' || die "the factor did not confirm: ${out}"
	step "${SEED_USER} enrols an authenticator" "confirmed"
fi

begin
[ "${began}" = "200" ] || die "the sign-in page answered ${began}"
step "the product sends a browser to hydra" "a form, ${began}"

# sign_in is the whole walk from the form to a token, and it is a function
# because it is run twice: the sign-out at the bottom **consumes** the session
# everything above depends on, so what comes after it needs a new one.
#
# It sets `cookie`, `id` and `sub`, which is what a shell function does instead
# of answering.
sign_in() {
	# `remember` covers the **consent** as well as the login, so the second and
	# third of these would be granted without drawing anything -- and this
	# asserts a screen when `CONSENT=ask`. Forgotten here rather than by each
	# caller, because a caller that has to remember is a caller that will not.
	forget_consent

	# The cookie by hand for the rest, because a jar will not send one to a host
	# `--resolve` invented. Everything else about the walk is the browser's.
	head=$(c -o /dev/null -D - -X POST "http://login.test:8091/session?login_challenge=${challenge}" \
		-H 'content-type: application/json' \
		-d "$(printf '{"alias":"%s","password":"%s"}' "${SEED_USER}" "${SEED_PASSWORD}")" | tr -d '\r')
	cookie=$(printf '%s' "${head}" | awk '/^[Ss]et-[Cc]ookie:/{print $2}' | sed 's/;$//')
	[ -n "${cookie}" ] || die "the password was not accepted"

	if [ "${FACTOR:-}" = "totp" ]; then
		# 200 and not 204: a third answer, and the whole point of it is that the
		# flow is **not** finished. What the page draws the second form from is in
		# the body; what this checks is that Hydra is told nobody yet.
		got=$(printf '%s' "${head}" | code)
		[ "${got}" = "200" ] || die "a password alone finished a sign-in with a factor on it (${got})"
		step "${SEED_USER} types the password" "${got}, one more to prove"

		got=$(c -o /dev/null -w '%{http_code}' -X POST "http://login.test:8091/accept?login_challenge=${challenge}" \
			-H "Cookie: ${cookie}")
		[ "${got}" = "401" ] || die "a half-signed-in browser was accepted (${got})"
		step "and hydra is told nobody yet" "${got}"

		head=$(c -o /dev/null -D - -X POST "http://login.test:8091/session/continue?login_challenge=${challenge}" \
			-H "Cookie: ${cookie}" -H 'content-type: application/json' \
			-d "$(printf '{"kind":"totp","secret":"%s"}' "$(oathtool --totp -b "${seed}")")" | tr -d '\r')
		got=$(printf '%s' "${head}" | code)
		[ "${got}" = "204" ] || die "the code from her authenticator was refused (${got})"

		# A finished sign-in is a new session: the half one carried a continuation
		# and an empty grant, and this one carries the delegation.
		cookie=$(printf '%s' "${head}" | awk '/^[Ss]et-[Cc]ookie:/{print $2}' | sed 's/;$//')
		[ -n "${cookie}" ] || die "finishing the second form set no session"
		step "and the code from her authenticator" "${got}, ${cookie%%=*}"
	else
		step "${SEED_USER} types the password" "204, ${cookie%%=*}"
	fi

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

		# 200 and not a redirect: a screen was drawn. **What** it says is `/flow`,
		# because the page is one document for both screens and asks the app which
		# it is -- so grepping the HTML would be grepping a bundle.
		got=$(c -o /dev/null -w '%{http_code}' "${l}" -H "Cookie: ${cookie}")
		[ "${got}" = "200" ] || die "consent=ask answered ${got} where a screen was asked for"
		asking=$(c "http://login.test:8091/flow?consent_challenge=${consent}" -H "Cookie: ${cookie}")
		printf '%s' "${asking}" | grep -q "${OAUTH_CLIENT}" || die "the screen has nothing to say which app is asking: ${asking}"
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

}

sign_in
# The payload, base64url with the padding put back.
payload() { cut -d. -f2 | tr '_-' '/+' | awk '{ n = length($0) % 4; if (n) $0 = $0 substr("===", 1, 4 - n); print }' | base64 -d; }
claims=$(printf '%s' "${id}" | payload)
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

# And **what is in the token it hands back**, which nothing here looked at until
# a deployment reported that a page could read nothing about the person it had
# just signed in. The claims are read as the person, with the delegation the
# app's own session holds -- and that session was closed at the end of every
# flow, so this one, and every flow after the first, carried `sub` and nothing
# else. With `remember` set, that is most of the tokens a deployment issues.
#
# The step above was here the whole time and passed: it asserted that the form
# was skipped, which is a fact about Hydra, and stopped before the fact about
# this app.
l="${went}"
for _ in 1 2 3 4 5; do
	case "${l}" in "${CALLBACK}"*) break ;; esac
	l=$(c -o /dev/null -D - "${l}" | loc | fix)
done
case "${l}" in
"${CALLBACK}"*) ;;
*) die "the remembered flow did not reach the callback: ${l}" ;;
esac

grant=$(printf '%s' "${l}" | sed 's/.*[?&]code=//; s/&.*//')
[ -n "${grant}" ] || die "the remembered flow handed back no code: ${l}"
again=$(c -X POST http://hydra.test:4444/oauth2/token \
	-d grant_type=authorization_code -d "code=${grant}" \
	-d "redirect_uri=${CALLBACK}" -d "client_id=${OAUTH_CLIENT}" -d "client_secret=${CLIENT_SECRET}" \
	| sed 's/.*"id_token":"//; s/".*//' | payload)
printf '%s' "${again}" | grep -q '"preferred_username":"'"${SEED_USER}"'"' \
	|| die "a token from a remembered browser carries no claims: ${again}"
step "  and it carries the claims too" "preferred_username"

rpc HolderService/Invalidate \
	"$(printf '{"ref":{"slug":{"alias":"%s","tenant":{"alias":"%s"}}}}' "${SEED_USER}" "${SEED_CUSTOMER}")" \
	| grep -q 'dateInvalidated' || die "roster refused the sign-out"
step "roster signs ${SEED_USER} out everywhere" "ok"

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

# The other sign-out: the **person's**, through the issuer's own endpoint.
#
# This is where the gap was. A product ends its own session and stops, the next
# page starts a flow, Hydra still remembers the browser and answers it without a
# form, and somebody who clicked *sign out* is looking at their name again --
# which is what two HAR files from a real deployment showed. The Login App
# serves `/logout` for it now, and this is the walk that would have caught the
# thing that got past every other gate: **the fake Hydra in `login/`'s own tests
# does not refuse a `post_logout_redirect_uri` that comes with no
# `id_token_hint`, and the real one does.**
#
# Not on the second-factor walk, and the reason is roster's replay rule working:
# a TOTP step that has been spent does not work twice, so a second sign-in
# inside the same thirty seconds is refused with a 401. Waiting out the window
# would put half a minute into every run to assert a third time what the two
# walks above already do -- and signing out has nothing to do with how somebody
# signed in.
if [ "${FACTOR:-}" = "totp" ]; then
	echo "flow: ok"
	exit 0
fi

# A session to sign out **of**: the half above ended the one the walk started
# with, and Hydra forgetting a browser is the thing this is about.
begin
[ "${began}" = "200" ] || die "the form was not asked for before the sign-out walk (${began})"
sign_in

step "the person signs out at the issuer" "…"

# Hydra's rule, tried the wrong way round first on purpose: a redirect back
# without a hint is refused, and it says so.
end_session="http://hydra.test:4444/oauth2/sessions/logout"
back_to=$(printf '%s' "${CALLBACK}" | sed 's|:|%3A|g; s|/|%2F|g')

no_hint=$(c -o /dev/null -D - "${end_session}?client_id=${OAUTH_CLIENT}&post_logout_redirect_uri=${back_to}" | loc)
case "${no_hint}" in
*error*id_token_hint*) step "  a redirect with no hint" "refused, as it should be" ;;
*) die "hydra allowed a post_logout_redirect_uri with no id_token_hint: ${no_hint}" ;;
esac

# And with one. Hydra sends the browser to the Login App's `/logout`, which
# accepts without drawing anything, and then back to where the request said.
to=$(c -o /dev/null -D - "${end_session}?client_id=${OAUTH_CLIENT}&id_token_hint=${id}&post_logout_redirect_uri=${back_to}" | loc)
case "${to}" in
*"/logout?logout_challenge="*) ;;
*) die "hydra did not ask the login app about the logout: ${to}" ;;
esac
# Two hops back, not one: the app answers with Hydra's own URL carrying a
# `logout_verifier`, and **that** is what ends the session and redirects. The
# same shape a login and a consent have -- the app says yes to the issuer and
# the issuer decides what the browser does next.
back=$(c -o /dev/null -D - "$(printf '%s' "${to}" | fix)" | loc | fix)
case "${back}" in
*logout_verifier=*) step "  the login app said yes" "hydra verifies it" ;;
*) die "the login app did not send the browser back to hydra: ${back}" ;;
esac

back=$(c -o /dev/null -D - "${back}" | loc)
case "${back}" in
"${CALLBACK}"*) step "  and hydra sends it on" "back to the product" ;;
*) die "hydra did not send the browser back to the product: ${back}" ;;
esac

# The whole of what it was for: the next flow asks for the form again, because
# the issuer has forgotten this browser.
begin
[ "${began}" = "200" ] || die "hydra still remembers her after a logout (${began})"
step "  and the form is asked for again" "${began}"

# And the other shape, which is the one the fake was agreeable about and a
# cluster was not: a relying party that sends the browser to the end session
# endpoint **without** the token. Hydra raises the challenge anyway and marks it
# not rp-initiated -- and this app answered `no` to it for a day, which is every
# sign-out from a product that does not keep its `id_token` around.
sign_in
step "a sign-out with no hint" "…"

to=$(c -o /dev/null -D - "${end_session}" | loc)
case "${to}" in
*"/logout?logout_challenge="*) ;;
*) die "hydra did not ask the login app about a hintless logout: ${to}" ;;
esac
# Its own name: `begin` writes `challenge`, and a walk that kept the logout's
# there would post a login challenge to `/logout` and read the 502 as the app.
leaving=$(printf '%s' "${to}" | sed 's/.*logout_challenge=//')

# Drawn, and nothing done until it is answered. 200 and not a redirect is the
# whole assertion: a redirect here is either the refusal this used to be or a
# session ended without anybody being asked.
got=$(c -o /dev/null -w '%{http_code}' "$(printf '%s' "${to}" | fix)")
[ "${got}" = "200" ] || die "a sign-out with no hint answered ${got} where a screen was asked for"
c "http://login.test:8091/flow?logout_challenge=${leaving}" | grep -q '"logout":true' \
	|| die "the page was not told which screen this is"
step "  a screen, not a refusal" "${got}"

# Still signed in until somebody says so. The screen being drawn is half of
# what this walk is about; the other half is that drawing it did nothing.
begin
[ "${began}" = "303" ] || die "a drawn confirmation ended the session anyway (${began})"
step "  and nothing ended yet" "${began}, still remembered"

# The answer. It is a fetch and not a form post -- the screen is a page, so what
# comes back is where to send the browser rather than a redirect -- and the hop
# after it is Hydra's own, the same `logout_verifier` the hinted walk follows.
said=$(c -X POST "http://login.test:8091/logout" -d "logout_challenge=${leaving}" -d "allow=1")
case "${said}" in
*'"signed_out":true'*) ;;
*) die "the confirmation was not accepted: ${said}" ;;
esac
step "  somebody says sign out" "accepted"

back=$(printf '%s' "${said}" | sed 's/.*"to":"//; s/".*//; s|\\u0026|\&|g' | fix)
back=$(c -o /dev/null -D - "${back}" | loc | fix)

# **Where it lands**, and this line was a `step` printing a URL nobody read.
# What it printed was Hydra's own fallback page -- the true end of a successful
# sign-out, addressed to an administrator: *the Default Post Logout URL is not
# set which is why you are seeing this fallback page*. A person who clicked sign
# out read that and reported the sign-out as broken, and this walk had been
# green on it.
#
# There is no `post_logout_redirect_uri` on this flow and there cannot be: Hydra
# refuses one without a hint. So the last page is the deployment's to name,
# which is `urls.post_logout_redirect` -- `URLS_POST_LOGOUT_REDIRECT` in
# `compose.yaml`, and this refuses to pass without it.
case "${back}" in
*/signed-out*) ;;
*fallback*) die "a finished sign-out ends on hydra's fallback page: ${back}" ;;
*) die "a finished sign-out did not end anywhere this deployment named: ${back}" ;;
esac
got=$(c -o /dev/null -w '%{http_code}' "${back}")
[ "${got}" = "200" ] || die "the page a sign-out ends on answered ${got}"
step "  and lands on a page for a person" "${got}"

begin
[ "${began}" = "200" ] || die "hydra still remembers her after a confirmed logout (${began})"
step "  and the form is asked for again" "${began}"

echo "flow: ok"
