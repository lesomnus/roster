# A directory over LDAP

roster holds what a directory holds: people, what they are called, where they
sit, which groups and teams they are in, and a way to check that somebody is
who they say. Every LDAP client in a building -- a NAS, a VPN, Jenkins, GitLab,
Grafana, a printer's address book -- asks exactly those questions, in a
protocol from 1997 that none of them will stop speaking. `roster ldap serve`
answers them.

This began as the plan for it and is now the description of it; the
[progress](#progress) table at the end is the record of how it was built, and
`docs/operating.md` § A directory, over LDAP is the operator's page.

## What it is, in one paragraph

**`roster ldap serve` is a consumer**, exactly as `roster account serve` is:
its own process, holding one tenant key per operator it fronts, reaching
roster over the wire and never past it. It speaks LDAPv3 on one side and
`rstr` on the other, and it *translates*: a bind is `Vouch.Verify` or a key
read back through `Me.Get`, a search is `Holder.Search`, `Holder.List`,
`Email.List`, `GroupMembership.List`. It holds no data, keeps no cache and
writes nothing. roster does not change for it -- which is the test of whether
it belongs here at all (`position.md` § The line: *who checks this?* roster,
every time).

## Why it is not part of `roster serve`

The reason the account app is not: this process holds tenant keys and faces
a network of appliances, and roster's own listeners -- the admin port most of
all -- must not be in the process that does. And a smaller one: LDAP is
optional. A deployment with nothing that speaks it should not have a port that
does.

## The bind, which is the whole design

A bind is a client saying *this DN, this password*, and the answer is yes or
no. That answer is not a read of the directory: it is what a client uses to
let the person into **itself**. Which is the one thing to get right, because
it is where a second factor would be walked around.

### Two passwords a bind may carry

**An app password, which is an API key.** The person mints an `rt_` for the
app -- the account page's *mint an app password* button, which is
`ApiKey.Issue` on their own row with methods `[/roster.MeService/Get]` and the
app's name as the alias -- and pastes it where the client asks for a password.
This process notices the `rt_` prefix, dials roster bearing that token, calls
`Me.Get`, and binds if the answer names the alias and tenant the DN does. Nothing
else is checked, because nothing else is needed: a key that can read its own
owner is that owner's, the wall put it in its tenant, and the person revokes
it from the account app's key list, one app at a time. This is Google's *app
password* for IMAP, and roster already had the mechanism; the only new thing
is the button, and the hint beside the token saying where to paste it.

**The person's password.** `Vouch.Verify{who: {tenant, alias}, secret}`.
This is what every LDAP client expects to work and what most deployments must
not let work for everybody, so it is a setting: `--bind key` (the default),
`--bind password`, `--bind either`. A password refused in `key` mode says so
in its diagnostic, and an app password refused in `password` mode likewise,
so the person reading their client's log learns which the deployment wanted
-- and nothing else, because the result code is the same 49 a wrong password
gets. Every other refusal of a bind is that code with no message: an unknown
name, somebody else's key, a name that is not a person's.

### Why `password` mode still cannot walk around a second factor

Not by a rule in this process. `Vouch.Verify` answers `ok` **only when the
sign-in is finished**; for somebody with a second factor enrolled it answers
`ok: false` with a continuation, and this process has no second form to spend
one on. So a person who set up TOTP cannot bind with their password in any
mode, and did not have to be told: the same answer that keeps a product app
that has never heard of second factors failing closed keeps this one closed
(`vouch.proto`, `VouchVerifyResponse.ok`). The continuation minted for the
refusal is a row that is swept unspent; that is the price and it is small.

Wrong passwords count toward roster's lockout, because roster counted them.
Nothing here counts anything.

### What a bind is not

- **Not an unauthenticated bind.** A name with an empty password is refused
  rather than taken as anonymous (RFC 4513 § 5.1.2 lets a server choose, and
  a client that sends one usually meant to bind).
- **Not SASL.** `EXTERNAL` is for certificates, `GSSAPI` is for a KDC roster
  is not and must not be (a ticket is issued and verified elsewhere, which is
  the line), `DIGEST-MD5` and `CRAM-MD5` are deprecated and need a
  plaintext-equivalent roster does not store, and `SCRAM` has no LDAP clients
  to speak of. A SASL bind is answered `authMethodNotSupported` (7). None of
  them would have got a second factor across anyway.
- **Not anonymous**, except for the root DSE, which a client reads before it
  can StartTLS and which says nothing about anybody. Every other operation on
  a connection that has not bound is `insufficientAccessRights` (50).
- **Not a way to reach `Ungated`.** A bind decides whether this connection
  may search. It does not decide what a search sees.

### Whose eyes a search uses

The process's own. Once bound, every read on that connection is made with the
tenant key this process holds for that tenant -- **one view for everybody who
binds**, as a directory is. Not the bound person's key, which is `[Me.Get]`
and reads nothing, and not a delegation, which a password bind could mint and
a key bind could not. What the tenant key's role names is the directory's
extent (see [the key](#the-key-this-process-holds)); an operator who wants a
client to see less mints it a narrower key and runs a second instance.

## The tree

One naming context per tenant this process holds a key for, and the key's
alias is the default suffix:

```
o=contoso
├── ou=people
│   └── uid=kim                      Holder
├── ou=groups
│   └── cn=payroll                   Group, member: the DNs of its people
└── ou=sites
    └── ou=seoul                     Site
        └── ou=teams
            └── cn=platform          Team, member: the DNs of its people
```

A team with no site is `cn=<team>,ou=teams,o=contoso`, under the suffix's own
`ou=teams`. `--base contoso=dc=contoso,dc=example` renames the suffix for a
deployment whose clients already expect one. Nothing below the suffix is configurable:
a directory's shape is what clients are configured against once, and two
deployments of roster should agree on it.

### A person

`objectClass: top, person, organizationalPerson, inetOrgPerson`.

| attribute | from | |
| --- | --- | --- |
| `uid` | `Holder.alias` | the RDN |
| `cn` | `Holder.name` | |
| `displayName` | `Profile.display_name` | when set |
| `sn` | `Holder.name` | `inetOrgPerson` requires one and roster does not split names; the whole name, rather than a guess at half of it |
| `mail` | `Email.address`, **verified rows only** | an unverified address is somebody's claim, and a directory is what other systems believe |
| `employeeNumber` | `Profile.employee_no` | |
| `departmentNumber` | `Profile.department` | |
| `preferredLanguage` | `Profile.locale` | |
| `labeledURI` | `Profile.picture` | |
| `memberOf` | the DNs of their groups and teams | AD's convenience, and the one most clients ask for; read only when asked for or filtered on, since it costs two lists |
| `entryUUID` | `Holder.id` | |

Never `userPassword`, never a key, never anything from `Credential`: roster
does not hand those out over its own wire and this process could not fetch
one to leak. Somebody disabled (`date_disabled`) or erased is not in the tree
at all rather than present with a flag, for the reason `Holder.date_disabled`
gives -- a flag is a thing a client has to know to read.

### A group, a team, a site

`groupOfNames`, `cn` the alias, `description` the note, `member` the DNs of
who is in it -- read from `GroupMembership.List` and `TeamMembership.List`. A
team's DN carries its site, because a team is a group of people **within one
site** (`entity.md`). A site is an `organizationalUnit`. Nested groups are not
here because they are not in roster (`position.md` § What we deliberately do
not have).

## Search

Base, one-level and subtree; filters with `&`, `|`, `!`, equality, presence
and substrings over the attributes above, evaluated straight off the BER
tree the request arrived as -- a filter on the wire is already a tree, and
there is no string to parse. Every attribute matches without regard to case,
which is what the matching rules of every attribute in this tree say;
ordering, approximate and extensible matches are *undefined* in RFC 4511's
sense and match nothing, so `(|(uid=kim)(cn~=kim))` still finds kim. What
decides the cost is which roster read a filter becomes:

| the filter says | this process calls |
| --- | --- |
| `(uid=kim)` | `Holder.Get` by alias, then `Holder.Search` for the alias spelled with other case |
| `(mail=kim@contoso.example)` | `Email.Get` by address at the tenant, normalised as `Email.Add` stores it |
| a substring on `uid`, `cn`, `displayName` | `Holder.Search{q}` |
| `(departmentNumber=…)`, `(employeeNumber=…)` | `Holder.Search{department}`, `{employee_no}` |
| `(memberOf=cn=payroll,…)` | `GroupMembership.List` or `TeamMembership.List` by the group or team named |
| anything else, or nothing | `Holder.List`, and the filter is evaluated on what comes back |

In an `&`, the most selective child is the plan and the whole filter is then
evaluated over what it read. The wall is already in every one of those reads,
so a search under `o=contoso` can never answer a row from `o=newco` -- the two
suffixes are two keys. Size limits are the client's `sizeLimit`, and the paged
results control (RFC 2696) carries roster's own cursor as its cookie, so a
client paging through ten thousand people reads them once each and no page is
bigger than `SearchPageLimit`. A subtree search from a suffix (or from the
root, which is every suffix) pages in stages -- the fixed nodes and the
people on roster's cursor, then the groups, then the sites and teams -- and
the cookie carries which tenant and which stage beside the cursor, so the
whole server is read once, entry by entry, with nothing kept on the server
between pages. A page shorter than asked for means the filter dropped rows,
which RFC 2696 allows; a page that would be empty reads on.

The reads of groups, teams, sites and people one search makes are kept for
that search -- a group of forty and a person in six teams cost a read per
row, not per mention -- and dropped with it.

**No cache.** A search is a read of roster, every time. A cache is exactly
where somebody `Holder.Disable`d stays visible for another hour, and the
clients that matter (a VPN) ask on every sign-in and can afford the round
trip. If a deployment's address book hammers it, the answer is `SyncService`
feeding a cache *in that client*, which is where a stale entry is that
client's problem and not this process's.

## What is refused, and how

| operation | answer | why |
| --- | --- | --- |
| Add, Modify, ModifyDN, Delete | `unwillingToPerform` (53) | a directory front is a **read**; the write side of this is SCIM, and it is a separate plan |
| Compare | 53 | nobody's client does this, and `userPassword` compare is a bind that does not count toward a lockout |
| Password Modify extended op (RFC 3062) | 53 for now | it could be `Credential.Set` with `current`; the question is whether a client that cannot do a second factor should change a password -- decision 6, below |
| SASL bind | `authMethodNotSupported` (7) | above |
| StartTLS | **supported**, and LDAPS beside it | a simple bind over plain TCP is the password in the clear; `--tls cert,key` offers StartTLS on `--listen` and enables `--listen-tls`, and `--require-tls` refuses a bind in the clear with `confidentialityRequired` (13) |
| WhoAmI (RFC 4532) | **supported** | free, and what a client uses to see a bind took |
| an unknown critical control | `unavailableCriticalExtension` (12) | ignoring one would answer a question the client did not ask |

## The key this process holds

`--key contoso=rt_…`, or `ROSTER_LDAP_KEY_CONTOSO`, one per tenant, minted
for a holder in that tenant whose role names what a directory reads:

```
/roster.TenantService/Get             # at start: which tenant this key is, and that it sees it
/roster.VouchService/Verify           # --bind password|either only
/roster.HolderService/Get
/roster.HolderService/List
/roster.HolderService/Search
/roster.EmailService/Get
/roster.EmailService/List
/roster.GroupService/Get
/roster.GroupService/List
/roster.GroupMembershipService/List
/roster.SiteService/Get
/roster.SiteService/List
/roster.TeamService/Get
/roster.TeamService/List
/roster.TeamMembershipService/List
```

`docker/customer.sh` mints exactly this for the compose stack (`Verify`
included, since `LDAP_BIND` is a switch there), and a key that
holds less answers `insufficientAccessRights` (50) with roster's message where
the missing method would have been read, rather than an empty tree.

`Me.Get` is not in it: a person's app password is checked bearing **their**
key, not this one. And `Verify` is left off a `--bind key` deployment on
purpose, so that flipping the mode later is a decision with a key to mint
rather than a flag to change.

## The account app's half

One form on the keys screen: **mint an app password** -- asks for the app's
name, mints with `[/roster.MeService/Get]`, shows the token once, and says
where to paste it. No new RPC, no new screen; `roster me issue-key --name nas
--allow /roster.MeService/Get` is the same thing from a shell. The Playwright
account spec walks it (`ts/e2e/account.spec.ts`, *an app password is a key
minted by the app's name*).

## Where it goes

| | |
| --- | --- |
| `ldap/` | the package: the tree, the bind, the search, the filter walker. A consumer, held to it by `scripts/test.sh`'s import check, which learns the second directory |
| `ldap/wire/` | the protocol: one connection's loop, and the handful of messages this process speaks, decoded from and encoded to BER. Nothing in it knows what a holder is |
| `cmd/ldap.go` | `roster ldap serve`: `--listen` (`:389`), `--listen-tls`, `--roster`, `--insecure`, `--key`/`ROSTER_LDAP_KEY_<ALIAS>`, `--base`, `--bind`, `--tls`, `--require-tls` |
| `docker/ldap.sh`, `compose.yaml` | the `ldap` service beside `account`, on `1389`, its key from the same `customer` one-shot; `LDAP_BIND=either` turns password binds on |
| `docs/operating.md` | § A directory, over LDAP -- the operator's page |
| `docs/usage/ways-in.md` | a paragraph under the tenant key: an app password is a key |
| `docs/baseline.md` | § A directory over LDAP, one row per promise below |

### The wire is ours

There is no server library in this. `ldap/wire` reads and writes the messages
on `github.com/go-asn1-ber/asn1-ber`, which is the BER codec `go-ldap`
already brings in, so the dependency graph grows by nothing.

Why not `jimlambrt/gldap`, which does this for you and was the first draft's
answer: the subset spoken here is five requests -- simple bind, search,
unbind, the StartTLS extended operation, abandon -- their answers, a paging
control, and one table of *which response tag refuses which operation*. That
is a few hundred lines, and it is the whole of what this process says on the
wire, which is the kind of thing this repository keeps rather than borrows:
gldap is one person's library whose last release was 2024-08, and the rule
about fixing upstream when it is in the way becomes a fork when upstream has
stopped. It also hands the filter over as a string, to be compiled back into
the tree it was decoded from; reading the tree directly is both less code and
the natural shape.

What the loop has to get right, and what a library would have got right for
it, is written out so it is tested rather than assumed: **message IDs** (every
answer carries its request's, and a connection may have several searches in
flight), **abandon** (stops a search that is still paging), **the StartTLS
turn** (the response is written in the clear and the very next byte is the
TLS handshake), **controls** (paging is the one read; unknown critical
controls refuse the operation with `unavailableCriticalExtension` (12)), and
**size and time limits** as the client set them.

`github.com/go-ldap/ldap/v3` is in the tests only: a real client against the
process is what proves the wire is right, and a second implementation of the
same encoding is a better witness than the first one reading itself.

## What is pinned

The promises, in `baseline.md`'s shape, each with its test:

| the promise | pinned by |
| --- | --- |
| an app password binds its own DN and no other; a wrong `rt_`, somebody else's, the directory's own key, and an empty password are the same `invalidCredentials` | `TestAnAppPasswordBindsItsOwnerAndNobodyElse` |
| under `--bind key` a real password does not bind; under `password` it does for somebody with no second factor and does **not** for somebody with one | `TestAPasswordBindStopsAtASecondFactor` |
| a search under one tenant's suffix answers none of another's, whatever the filter | `TestASuffixIsATenant` |
| `(uid=…)`, `(mail=…)`, a substring, a department, an employee number, `memberOf`, and the boolean shapes over them each answer the same people the corresponding roster read does, without regard to case; a person's attributes are the table above and nothing operational unasked | `TestAFilterIsARosterRead` |
| groups and teams name their members and nobody disabled; a site holds its teams and a team with no site is under the suffix; `memberOf` agrees with `member`; the whole suffix read in one search and in pages is the same tree | `TestGroupsTeamsAndSitesAreTheTree` |
| paging through everybody answers each person once, with a filter that drops rows and with one roster has no index for; the client's size limit is honoured | `TestPagingReadsEverybodyOnce` |
| no entry ever carries `userPassword`, a key, or a credential; the process's own key and every presented password are absent from every response | `TestNothingSecretIsInTheTree` |
| somebody disabled is not in the tree, from the next search on -- there is no cache | `TestTheDisabledAreNotListed` |
| every write and every SASL bind is refused with the code above, and the connection stays usable; a critical control the server does not know refuses the operation; two searches share a connection; an abandoned search answers nothing | `ldap/wire`: `TestTheDirectoryIsReadOnlyOnTheWire` · `TestASaslBindIsNotSupported` · `TestAnUnknownCriticalControlRefusesTheOperation` · `TestTwoSearchesShareAConnection` · `TestAnAbandonedSearchAnswersNothing` |
| a plain-TCP bind is refused when TLS is required, and StartTLS turns the connection; LDAPS is the listener's | `TestStartTlsTurnsTheConnection` · `TestLdapsIsTheListenersBusiness` |
| `roster ldap serve` refuses to start with no key, a malformed key, base or bind mode, TLS flags with nothing to offer, a base naming a tenant it has no key for, or a key for one tenant given as another's; told everything, a client binds with an app password and searches | `TestLdapServeIsToldEverything` · `TestLdapIsToldEverything` |

## The order

| | | |
| --- | --- | --- |
| L0 | **the wire** | `ldap/wire`: the connection loop, the five requests and their answers, StartTLS, the paging control, the refusal table. Tested against `go-ldap` as a client, with the root DSE as the one entry it serves |
| L1 | **people** | `ldap/`: bind in both modes, `ou=people` with the attributes above, search with every filter shape in the table evaluated off the BER tree, paging. `roster ldap serve`. The import check. The first six tests |
| L2 | **groups, teams, sites** | `ou=groups`, `ou=sites/…/ou=teams`, `member` and `memberOf`. The remaining tests |
| L3 | **shipped** | compose service, `operating.md`, `ways-in.md`, the account app's *App password* preset and its spec, this file rewritten as a description |

## Decisions taken

Decided once, in the open, before the code that needed them; each is also a
comment beside what it decides.

1. **The suffix is `o=<tenant alias>`**, renamable per tenant. A
   `dc=`-style default would have to invent domain components roster does
   not have.
2. **A search sees what the process's key sees**, never what the bound
   person could. The alternative is a directory that looks different to
   everybody, which no client expects and which `[Me.Get]` could not read
   anyway.
3. **`mail` is verified addresses only.** The alternative publishes a claim.
4. **Nobody is in the tree unless they can sign in**: disabled and erased
   are absent, not flagged.
5. **`--bind key` is the default.** A deployment turns password binds on by
   minting a key that reaches `Verify`, and reads the sentence above about
   second factors when it does.
6. **Password Modify is refused** until somebody names a client that needs
   it (`unwillingToPerform`, with a diagnostic saying where passwords are
   changed). Mapping it is a page of code; whether a one-factor protocol should
   change a password is the question, and the same one `Credential.Set`'s
   `current` rule was written for.

## Not here, and where it would go

- **SCIM**, the write side: an app provisioning people *into* roster. It is
  `Holder.Add`, `Email.Add`, `GroupMembership.Add` behind a JSON schema, and
  it is a second consumer with a plan of its own. Nothing in this one should
  make it harder.
- **Kerberos, certificates, nested groups**: `position.md` says why, and
  this process does not change the answer.
- **A manager edge, more profile fields**: the conversation that produced
  `Holder.Search` said these arrive as proto additions when a use names them.
  `manager` is the attribute a client will ask for first.

## Progress

| | | |
| --- | --- | --- |
| L0 | the wire | **done** — `ldap/wire`: the loop, simple bind, search, unbind, abandon, StartTLS (and LDAPS as the listener's), WhoAmI, paging, the refusal table. Proved against `go-ldap` as the client and by hand for what it has no call for (abandon); under the race detector |
| L1 | people | **done** — `ldap/`: the tree above the people, `ou=people` with every attribute in the table (`mail` verified only, disabled absent), bind in all three modes with roster's `ok` as the second-factor rule, search planned into `Holder.Get`/`Holder.Search`/`Email.Get`/`Holder.List` and evaluated off the BER tree, paging on roster's own cursor, `uid` and `mail` found without regard to case. `roster ldap serve` with `--key`/`ROSTER_LDAP_KEY_`, `--base`, `--bind`, `--tls`, `--listen-tls`, `--require-tls`. The import check learned `ldap/`. Eight tests in `ldap/`, one in `cmd/` |
| L2 | groups, teams, sites | **done** — `ou=groups` as `groupOfNames` with `member` the DNs of people in the tree (the disabled are not named), `ou=sites/ou=<site>/ou=teams/cn=<team>` and `ou=teams` under the suffix for a team with no site, `memberOf` on a person from the other end, `(memberOf=…)` planned into one membership list. A subtree search from a suffix or the root pages in stages -- people on roster's cursor, then the groups, then the sites -- carried in the cookie, so a client paging the whole server sees every entry once. `TestGroupsTeamsAndSitesAreTheTree` |
| L3 | shipped | **done** — the `ldap` compose service on `1389` with its key from `customer.sh`, `operating.md` § A directory, over LDAP, `ways-in.md` on app passwords, `baseline.md` § A directory over LDAP, the account page's *mint an app password* form and its spec, and this file as a description |
