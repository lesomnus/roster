#!/bin/sh
# The first customer, once, and the keys the account app and the directory
# front them with.
#
# `roster init` seeds no customer on purpose -- the console makes the first one
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
roster role add "@${t}/everything" '{"methods":["/roster.*/*"]}' >/dev/null
printf '{"role":{"slug":{"alias":"everything","tenant":{"alias":"%s"}}},"holder":{"slug":{"alias":"%s","tenant":{"alias":"%s"}}}}' "${t}" "${u}" "${t}" \
	| roster binding add - >/dev/null
printf '%s' "${SEED_PASSWORD}" | roster vouch set --password-stdin "@${t}/${u}" >/dev/null 2>&1
printf '{"tenant":{"alias":"%s"},"name":"%s"}' "${t}" "${PUBLIC_HOST}" | roster host add - >/dev/null

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
# The directory's key first and the account app's last, because the account
# app's is the marker this script's "once" is decided by.
umask 077
roster key add --tenant "${t}" --holder directory --name directory --allow '/roster.TenantService/Get,/roster.HolderService/Get,/roster.HolderService/List,/roster.HolderService/Search,/roster.EmailService/Get,/roster.EmailService/List,/roster.GroupService/Get,/roster.GroupService/List,/roster.GroupMembershipService/List,/roster.SiteService/Get,/roster.SiteService/List,/roster.TeamService/Get,/roster.TeamService/List,/roster.TeamMembershipService/List,/roster.VouchService/Verify' 2>/dev/null >"${ACCOUNT_STATE}/${SEED_CUSTOMER}.ldap.key.tmp"
mv "${ACCOUNT_STATE}/${SEED_CUSTOMER}.ldap.key.tmp" "${ACCOUNT_STATE}/${SEED_CUSTOMER}.ldap.key"
roster key add --tenant "${t}" --holder account --name account --allow '/roster.*/*' 2>/dev/null >"${key}.tmp"
mv "${key}.tmp" "${key}"
