# A directory over LDAP — the plan

roster holds what a directory holds: people, what they are called, where they
sit, which groups and teams they are in, and a way to check that somebody is
who they say. Every LDAP client in a building -- a NAS, a VPN, Jenkins, GitLab,
Grafana, a printer's address book -- asks exactly those questions, in a
protocol from 1997 that none of them will stop speaking. This is the plan for
answering them.

It is a plan and not yet a description: nothing below exists. When it does,
this file becomes the document of the thing, section by section, and the
[progress](#progress) table at the end says how far that has got.
`docs/roadmap.md` § Open points here.

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
app -- `ApiKey.Issue` on their own row, methods `[/roster.MeService/Get]`,
alias the app's name -- and pastes it where the client asks for a password.
This process notices the `rt_` prefix, dials roster bearing that token, calls
`Me.Get`, and binds if the answer's `id` is the holder the DN names. Nothing
else is checked, because nothing else is needed: a key that can read its own
owner is that owner's, the wall put it in its tenant, and the person revokes
it from the account app's key list, one app at a time. This is Google's *app
password* for IMAP, and roster already had the mechanism; the only new thing
is a one-line hint on the key screen.

**The person's password.** `Vouch.Verify{who: {tenant, alias}, secret}`.
This is what every LDAP client expects to work and what most deployments must
not let work for everybody, so it is a setting: `--bind key` (the default),
`--bind password`, `--bind either`.

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

`--base contoso=dc=contoso,dc=example` renames the suffix for a deployment
whose clients already expect one. Nothing below the suffix is configurable:
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
| `memberOf` | the DNs of their groups and teams | AD's convenience, and the one most clients ask for |
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
and substrings over the attributes above, parsed with `go-ldap`'s compiler
and evaluated here. What decides the cost is which roster read a filter
becomes:

| the filter says | this process calls |
| --- | --- |
| `(uid=kim)` | `Holder.Get` by alias |
| `(mail=kim@contoso.example)` | `Email.Get` by address at the tenant |
| a substring on `uid`, `cn`, `displayName` | `Holder.Search{q}` |
| `(departmentNumber=…)`, `(employeeNumber=…)` | `Holder.Search{department}`, `{employee_no}` |
| `(memberOf=cn=payroll,…)` | `GroupMembership.List` by group |
| anything else, or nothing | `Holder.List`, and the filter is evaluated on what comes back |

The wall is already in every one of those, so a search under `o=contoso` can
never answer a row from `o=newco` -- the two suffixes are two keys. Size
limits are the client's `sizeLimit`, and the paged results control (RFC 2696)
carries `List`'s and `Search`'s own cursor as its cookie, so a client paging
through ten thousand people reads them once each and no page is bigger than
`SearchPageLimit`.

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
| Password Modify extended op (RFC 3062) | 53 for now | it could be `Credential.Set` with `current`; the question is whether a client that cannot do a second factor should change a password -- open, below |
| SASL bind | `authMethodNotSupported` (7) | above |
| StartTLS | **supported**, and LDAPS beside it | a simple bind over plain TCP is the password in the clear; `--tls cert,key` and `--starttls-required` |

## The key this process holds

`--key contoso=rt_…`, or `ROSTER_LDAP_KEY_CONTOSO`, one per tenant, minted
for a holder in that tenant whose role names what a directory reads:

```
/roster.VouchService/Verify           # --bind password|either only
/roster.HolderService/Get
/roster.HolderService/List
/roster.HolderService/Search
/roster.EmailService/Get
/roster.EmailService/List
/roster.GroupService/List
/roster.GroupMembershipService/List
/roster.SiteService/List
/roster.TeamService/List
/roster.TeamMembershipService/List
```

`Me.Get` is not in it: a person's app password is checked bearing **their**
key, not this one. And `Verify` is left off a `--bind key` deployment on
purpose, so that flipping the mode later is a decision with a key to mint
rather than a flag to change.

## The account app's half

One preset on the keys screen: **App password** -- asks for the app's name,
mints with `[/roster.MeService/Get]`, shows the token once, and says where to
paste it. No new RPC, no new screen; `roster me issue-key --name nas --allow
/roster.MeService/Get` is the same thing from a shell. The Playwright account
spec grows one path for it.

## Where it goes

| | |
| --- | --- |
| `ldap/` | the package: the tree, the bind, the search, the filter walker. A consumer, held to it by `scripts/test.sh`'s import check, which learns the second directory |
| `cmd/ldap.go` | `roster ldap serve`: `--listen :389`, `--roster`, `--insecure`, `--key`, `--base`, `--bind`, `--tls`, `--starttls-required` |
| `docker/ldap.sh`, `compose.yaml` | an `ldap` service beside `account`, its key from the same `customer` one-shot |
| `docs/operating.md` | § A directory, over LDAP -- the operator's page |
| `docs/usage/ways-in.md` | a paragraph under the tenant key: an app password is a key |
| `docs/baseline.md` | the rows below, pinned |

**Library**: `github.com/jimlambrt/gldap` for the server (a `Mux` of bind,
search, unbind and extended handlers, StartTLS, paging controls) and
`github.com/go-ldap/ldap/v3` for the filter compiler and for the tests, which
are a real client against the process. Neither is a framework: gldap decodes
the messages and encodes the answers, and everything that decides anything
is in `ldap/`.

## What is pinned

The promises, in `baseline.md`'s shape, each to be written with the code:

| the promise | pinned by |
| --- | --- |
| an app password binds its own DN and no other; a wrong `rt_` and somebody else's are the same `invalidCredentials` | `TestAnAppPasswordBindsItsOwnerAndNobodyElse` |
| under `--bind key` a real password does not bind; under `password` it does for somebody with no second factor and does **not** for somebody with one | `TestAPasswordBindStopsAtASecondFactor` |
| a search under one tenant's suffix answers none of another's, whatever the filter | `TestASuffixIsATenant` |
| `(uid=…)`, a substring, a department, `memberOf` each answer the same people the corresponding roster read does | `TestAFilterIsARosterRead` |
| paging through everybody answers each person once | `TestPagingReadsEverybodyOnce` |
| no entry ever carries `userPassword`, a key, or a credential; the process's own key and every presented password are absent from every response | `TestNothingSecretIsInTheTree` |
| somebody disabled is not in the tree | `TestTheDisabledAreNotListed` |
| every write and every SASL bind is refused with the code above, and the connection stays usable | `TestTheDirectoryIsReadOnly` |
| a plain-TCP bind is refused when `--starttls-required` is set | `TestAPasswordNeedsTLS` |
| `roster ldap serve` refuses to start with no key, and with a base naming a tenant it has no key for | `TestLdapServeIsToldEverything` |

## The order

| | | |
| --- | --- | --- |
| L1 | **people** | `ldap/`: root DSE, StartTLS/LDAPS, bind in both modes, `ou=people` with the attributes above, search with every filter shape in the table, paging. `roster ldap serve`. The import check. The first six tests |
| L2 | **groups, teams, sites** | `ou=groups`, `ou=sites/…/ou=teams`, `member` and `memberOf`. The remaining tests |
| L3 | **shipped** | compose service, `operating.md`, `ways-in.md`, the account app's *App password* preset and its spec, this file rewritten as a description |

## Decisions to take, and the answers this plan assumes

Written here so they are decided once, in the open, before the code that
needs them. Each becomes a comment beside what it decides when it lands.

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
   it. Mapping it is a page of code; whether a one-factor protocol should
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
| L1 | people | not started |
| L2 | groups, teams, sites | not started |
| L3 | shipped | not started |
