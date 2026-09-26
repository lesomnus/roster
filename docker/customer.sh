#!/bin/sh
# Its "once" is a file on a volume, so a seed that **grows** -- a role with one
# more method, a key for a service that did not exist last week -- does not
# reach a deployment that has already been stood up. `docker compose down -v`
# is how to get it, and `scripts/hydra.sh` does that on every run for exactly
# this reason.
# The first customer, once, and the keys the account app and the directory
# front them with.
#
# `roster init` seeds no customer on purpose -- the admin console makes the first one
# the same way it makes the hundredth -- and a stack somebody brings up to work
# on the account page needs one already there, with a person who has a
# password and a host that resolves to them. So this is `docs/operating.md`'s
# recipe run by a container: the four writes, a password, a host, and a key
# for the app, written to a file the `account` service reads.
#
# Once, decided by the key file: a key is minted exactly once, so its presence
# is the marker. It reads the databases directly, as a shell on the box would.
set -eu

: "${SEED_CUSTOMER:=contoso}"
: "${SEED_USER:=erin}"
: "${SEED_PASSWORD:=correct horse battery staple}"
: "${PUBLIC_HOST:=localhost}"
: "${ACCOUNT_STATE:=/var/lib/roster-account}"

key="${ACCOUNT_STATE}/${SEED_CUSTOMER}.key"
if [ -e "${key}" ]; then
	exit 0
fi
mkdir -p "${ACCOUNT_STATE}"

echo "roster: standing ${SEED_CUSTOMER} up, once" >&2

t="${SEED_CUSTOMER}"
u="${SEED_USER}"
roster tenant add "@${t}" >/dev/null
roster holder add "@${t}/${u}" >/dev/null
# The `everything` role arrives with the tenant (`server/core/tenant.go`), so
# this binds to it rather than writing a second one.
printf '{"role":{"slug":{"alias":"everything","tenant":{"alias":"%s"}}},"holder":{"slug":{"alias":"%s","tenant":{"alias":"%s"}}}}' "${t}" "${u}" "${t}" \
	| roster binding add - >/dev/null
printf '%s' "${SEED_PASSWORD}" | roster vouch set --password-stdin "@${t}/${u}" >/dev/null 2>&1
# Every name a browser reaches this tenant at, and there are four of them
# because this rig publishes on some and resolves others inside its own network.
#
# All of them, and not just the public one, because a `Host` row is what says
# which tenant a name is -- and #36 made the Login App read it: a flow resolves
# to a tenant through the **redirect** the authorization request named, so a
# client registered at `http://127.0.0.1:5555/callback` reaches nobody unless
# something here says `127.0.0.1` is contoso's. `roster login doctor` is what
# tells a deployment it forgot one.
#
#	${PUBLIC_HOST}   the user console, the account app, a browser
#	127.0.0.1        the demo product, as the host publishes it
#	behind           `oauth2-proxy`, inside the compose network
#	product          the relying party that is the app itself
for h in "${PUBLIC_HOST}" 127.0.0.1 behind product; do
	printf '{"tenant":{"alias":"%s"},"name":"%s"}' "${t}" "${h}" | roster host add - >/dev/null
done

# The account app's own person and key, holding what the app calls as itself.
roster holder add "@${t}/account" >/dev/null
printf '{"role":{"slug":{"alias":"everything","tenant":{"alias":"%s"}}},"holder":{"slug":{"alias":"account","tenant":{"alias":"%s"}}}}' "${t}" "${t}" \
	| roster binding add - >/dev/null

# The directory's own person and key: what a directory reads, and `Verify`
# so that `LDAP_BIND=password` works when somebody sets it (`docs/ldap.md`
# § The key this process holds).
roster holder add "@${t}/directory" >/dev/null
roster role add "@${t}/directory" '{"methods":["/roster.TenantService/Get","/roster.HolderService/Get","/roster.HolderService/List","/roster.HolderService/Search","/roster.EmailService/Get","/roster.EmailService/List","/roster.GroupService/Get","/roster.GroupService/List","/roster.GroupMembershipService/List","/roster.SiteService/Get","/roster.SiteService/List","/roster.TeamService/Get","/roster.TeamService/List","/roster.TeamMembershipService/List","/roster.VouchService/Verify"]}' >/dev/null
printf '{"role":{"slug":{"alias":"directory","tenant":{"alias":"%s"}}},"holder":{"slug":{"alias":"directory","tenant":{"alias":"%s"}}}}' "${t}" "${t}" \
	| roster binding add - >/dev/null

# To a file first and moved into place, so a half-written key is never read.
# The directory's and the Login App's first and the account app's last, because
# the account app's is the marker this script's "once" is decided by.
umask 077
# The Login App's, through the command rather than by hand: one `rk_` for the
# whole deployment and, per name above, the holder that name borrows -- the
# holder, its role and the nomination, all of them idempotent.
#
# It was a `key add --tenant … --holder login-app` here with the method list
# spelled out, beside a `holder add`, a `role add` and a `binding add` that spelt
# it again. `roster login provision` is the one place that list lives now
# (`cli.LoginMethods`), which is what stopped it being written three times and
# drifting once -- and it is the command `deploy/` runs, so this rig exercises it
# rather than a hand-rolled equivalent of it.
roster login provision --out "${ACCOUNT_STATE}" >/dev/null
roster key add --tenant "${t}" --holder directory --name directory --allow '/roster.TenantService/Get,/roster.HolderService/Get,/roster.HolderService/List,/roster.HolderService/Search,/roster.EmailService/Get,/roster.EmailService/List,/roster.GroupService/Get,/roster.GroupService/List,/roster.GroupMembershipService/List,/roster.SiteService/Get,/roster.SiteService/List,/roster.TeamService/Get,/roster.TeamService/List,/roster.TeamMembershipService/List,/roster.VouchService/Verify' 2>/dev/null >"${ACCOUNT_STATE}/${SEED_CUSTOMER}.ldap.key.tmp"
mv "${ACCOUNT_STATE}/${SEED_CUSTOMER}.ldap.key.tmp" "${ACCOUNT_STATE}/${SEED_CUSTOMER}.ldap.key"
roster key add --tenant "${t}" --holder account --name account --allow '/roster.*/*' 2>/dev/null >"${key}.tmp"
mv "${key}.tmp" "${key}"
