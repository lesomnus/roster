# A directory over LDAP

roster holds what a directory holds: people, what they are called, where they
sit, which groups and teams they are in, and a way to check that somebody is
who they say. Every LDAP client in a building -- a NAS, a VPN, Jenkins, GitLab,
Grafana, a printer's address book -- asks exactly those questions, in a
protocol from 1997 that none of them will stop speaking. `roster ldap serve`
answers them.

This page is the design **and** how to run it. The
[progress](#progress) table at the end is the record of how it was built.

## What it is, in one paragraph

**`roster ldap serve` is a consumer**, exactly as `roster account serve` is:
one deployment key, answered in each tenant as the holder that tenant nominated
for it, reaching roster over the wire and never past it, whether it runs as its
own process or as a block in the server's -- the roster-hosted shape of
[apps.md](apps.md). A directory has a DN where a front door has a host, so every
call names its tenant directly: `roster-at: @<tenant>`. It
speaks LDAPv3 on one side and `rstr` on the other, and it *translates*: a bind is
`Vouch.Verify` or a key read back through `Me.Get`, a search is `Holder.Search`,
`Holder.List`, `Email.List`, `GroupMembership.List`. It holds no data, keeps no cache and
writes nothing. roster does not change for it -- which is the test of whether
it belongs here at all (`position.md` § The line: *who checks this?* roster,
every time).

## Running it

```sh
(umask 077 && roster control key add --allow /roster.NominationService/List directory > directory.key)
roster app install --tenant contoso --role '<the methods below>' directory

roster ldap serve --roster roster:50051 \
  --deployment-key file:directory.key \
  --tls cert.pem,key.pem --require-tls \
  --listen :389 --listen-tls :636
```

The suffixes it serves are the tenants that nominated its key, read at start.
A tenant renamed while it runs is served under the suffix it had until a
restart, and under its new one from then: its people go on binding and
searching under the old one meanwhile, by password as by app password, since
every call names the tenant by its identifier.
A tenant running its own copy gives it that tenant's `rt_` instead,
`--key contoso=rt_…` or `ROSTER_LDAP_KEY_CONTOSO`.

**A bind holds a connection to one tenant.** One process serving many tenants
answers each connection as the tenant its bind DN is in: the root DSE names that
suffix and no other -- and none before a bind, since a suffix is a tenant's name --
a search from the root walks that tenant alone, and a search under another
tenant's suffix is `noSuchObject`, the answer for a name that is not there. It
was once only *whether* the connection was bound, and a person bound in contoso
searched `o=fabrikam` and read fabrikam's people.

Or as a block in `roster.yaml`, which puts it in the same process as the server
(`operating.md` § "One process, or five" is the trade -- this process holds tenant
keys and faces a network of appliances, so a deployment that cares keeps it in a
container of its own):

```yaml
ldap:
  addr: :389
  bind: key
  key: file:/run/roster/directory.key
```

Either way it is a **consumer**, reaching roster over the wire and never past it.
Install it into each tenant with the role in
§ [The key this process holds](#the-key-this-process-holds); `docker/customer.sh`
is that, as a script.

`--require-tls` refuses a bind in the clear, which is what a simple bind over
plain TCP is. Offer StartTLS with `--tls`, LDAPS with `--listen-tls`, or terminate
TLS in front and pass the plain port on a private network.

```sh
ldapsearch -H ldap://localhost:1389 -x \
  -D uid=erin,ou=people,o=contoso -w rt_… \
  -b o=contoso '(&(objectClass=inetOrgPerson)(memberOf=cn=payroll,ou=groups,o=contoso))' mail
```

The compose stack runs one on `1389`, and `LDAP_BIND=either` turns passwords on.

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
This process knows one by its shape -- the `rt_` prefix and the forty-three
characters roster mints after it, so a password that merely begins `rt_` is a
password -- dials roster bearing that token, calls `Me.Get`, and binds if the
answer names the alias and tenant the DN does, the alias read without regard to
case as roster reads a name somebody typed. Nothing
else is checked, because nothing else is needed: a key that can read its own
owner is that owner's, the wall put it in its tenant, and the person revokes
it from the account app's key list, one app at a time. This is Google's *app
password* for IMAP, and roster already had the mechanism; the only new thing
is the button, and the hint beside the token saying where to paste it.

**The person's password.** `Vouch.Verify{who: {alias}, secret}`, asked as this
process's holder in the tenant the DN's suffix names, so the tenant is the
key's to say and not a name sent beside it.
This is what every LDAP client expects to work and what most deployments must
not let work for everybody, so it is a setting: `--bind key` (the default),
`--bind password`, `--bind either`. A password refused in `key` mode says so
in its diagnostic, and an app password refused in `password` mode likewise,
so the person reading their client's log learns which the deployment wanted
-- and nothing else, because the result code is the same 49 a wrong password
gets. That sentence is decided by the mode and the shape of what was sent, and
never by the name: a bind as somebody under a suffix this process serves, under
one it does not, and as a name that is not a person's are answered alike, so
which tenants are here is not learned by trying names. An app password is
tried as one and as nothing else, where both kinds bind too: one its owner
revoked is not their wrong password, and does not count toward a lockout.
One more is said, and only to somebody holding the right password: where
a second factor is enrolled, *use an app password for this client* (below).
Every other refusal of a bind is that code and what the mode says of the kind
of secret, or no message at all: an unknown name, somebody else's key, a name
that is not a person's.

**A roster that cannot be asked is not a refusal.** A bind made while roster is
unreachable or failing is answered `unavailable` (52), and `busy` (51) where
roster said to come back later, each with a sentence of this process's own; it
was 49, which a client shows as a wrong password. A search is answered the
same two ways, and never with the sentence the transport wrote, which names
where roster is dialed.

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

**Which client sent them is in this process's log.** Roster's trail names the
directory's own holder for a wrong password, from the directory's address, so
it cannot say which appliance locked somebody out. This process writes one line
for every bind, at `info`:

```
ldap: bind from=10.0.4.17:51234 tenant=<the tenant's identifier> kind=password ended=refused
```

`from` is the client's address as the socket has it, `kind` is `key`,
`password` or `none`, and `ended` is one of these, the last three with roster's
`code` beside them:

```
bound
refused                            roster said no, or the name is not whose the key is
not tried                          roster was not asked: no password, the kind this
                                   directory does not bind, or a password for a name
                                   under no suffix. It counted toward no lockout
second factor                      the right password, of somebody who has one enrolled
locked                             roster says the account is locked, by this bind or before it
roster away                        unreachable, or failing
roster busy                        held to a rate
the directory's key was refused    the tenant closed, its holder there suspended, or a
                                   role that does not name Vouch.Verify
```

`tenant` is there when the name was somebody's under a suffix this process
serves. The name is not in it, nor the password or the key: a name is what a
stranger typed. A wrong password is found by its time and tenant beside the
trail's line for it, and the bind that locked somebody out says `locked`.

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
- **Not anonymous.** A connection that has not bound reads the root DSE, which
  a client needs before it can StartTLS and which says nothing about anybody,
  and reads nothing else: a search of anything but the root DSE is
  `insufficientAccessRights` (50), *bind first*, whether or not it named a
  suffix this process serves. The rest is answered before a bind as it is
  after one. WhoAmI succeeds and names nobody. Compare, the writes and
  password modify are `unwillingToPerform` (53), which is what they are to
  everybody ([below](#what-is-refused-and-how)). And a bind with no name and no
  password succeeds, as the protocol has it, and binds nobody: the connection
  is what it was. Under `--require-tls` that bind is refused in the clear like
  any other.
- **Not a way to reach `Ungated`.** A bind decides whether this connection
  may search. It does not decide what a search sees.

### How long a bind lasts

**For as long as whoever made it would still be let make it.** A bind is asked
about again: a search on a connection roster has not been asked about for
**five minutes** asks first, with one call, and then answers.

- A connection bound with an **app password** is asked about by bearing the
  app password again, which is what the bind asked. The key erased from the
  account page's list or run out, its holder suspended or erased, their tenant
  closed: each refuses it.
- A connection bound with a **password** is asked about by who the password
  proved, since the password is not kept and trying it again would count toward
  a lockout: suspended, erased, or signed out of everything since they bound --
  *sign out everywhere*, or a password changed or issued. Signing out
  everywhere does not end an app password, here as anywhere else; revoking the
  key does.

**What a client sees when whoever bound is stopped.** Until the next asking,
nothing: for at most five minutes a connection they had bound goes on
searching. From then on the connection is nobody's. The search that asked and
every later one is `insufficientAccessRights` (50), *bind first*, exactly as on
a connection that never bound; the root DSE names no suffix; WhoAmI names
nobody. The connection is not closed, and a bind made on it again is answered
as any bind is: 49 for somebody suspended or erased or whose key is gone, and
bound again for somebody who only signed out everywhere. This process's log has
one line for it, in the form a bind's has: `ldap: a bound connection is
nobody's now`, with `from`, `tenant` and `kind`.

**A roster that cannot say is not somebody stopped.** The search is answered
`unavailable` or `busy`, the connection stays whose it was, and the next search
asks again. So is this process's own key being refused where a password's
person is asked about, which says nothing of them: nothing is read while that
lasts.

**Five minutes** is what roster's own streams are held to where a deployment
says nothing (`stream.recheck`, [operating.md](operating.md)), and it is the
same promise -- *no credential of theirs works, and a stream they left open
ends* -- since a connection is a credential shown once and then served until
somebody hangs up. It is a constant, and `stream.recheck` does not move it:
this process is as often another process as a block of the server's. It costs
one call of roster for each five minutes a connection searches in, whichever
kind of bind, and searches that arrive together share it; a connection that
does not search is not asked about.

**What this process keeps to ask with**, in memory and for as long as the
connection is bound: who bound, in which tenant and when; and, for an app
password, the app password itself, as the client that sent it keeps it. Never a
password. Asking about an app password with less would need roster to say
which key bore a call, which `Me.Get` does not, and this process's role to name
a read of a key, which it does not. It is the one thing here believed for a
while, and it is not an entry: every search is still a read of roster.

*Since they bound* is this process's clock beside a moment roster stamped, as
it is for a session an app holds ([relying-app.md](relying-app.md)). Where the
directory is a process of its own, a clock that runs ahead of roster's misses a
sign-out made within their difference after the bind.

Held by `TestAConnectionIsWhoeverBoundItForAsLongAsTheyStillStand` and
`TestAConnectionWhoseBinderStillStandsCostsOneCallAClock`.

### Whose eyes a search uses

The process's own. Once bound, every read on that connection is made with the
tenant key this process holds for that tenant -- **one view for everybody who
binds**, as a directory is. Not the bound person's key, which is `[Me.Get]`
and reads nothing, and not a delegation, which a password bind could mint and
a key bind could not. What the tenant key's role names is the directory's
extent (see [the key](#the-key-this-process-holds)); an operator who wants a
client to see less mints it a narrower key and runs a second instance.

So it is the process's own holder that roster's access record names for every
read made here, whoever bound: the record says the directory read somebody, from
this process's address, and not which client asked it to
([operating.md](operating.md) § *Who read whose data*).

## The tree

One naming context per tenant this process fronts, and the tenant's alias is
the default suffix:

```
o=contoso
├── ou=people
│   └── uid=kim                      Holder
├── ou=groups
│   └── cn=payroll                   Group, member: the DNs of its people
├── ou=teams
│   └── cn=nomads                    Team with no site
└── ou=sites
    └── ou=seoul                     Site
        └── ou=teams
            └── cn=platform          Team, member: the DNs of its people
```

A team with no site is `cn=<team>,ou=teams,o=contoso`, under the suffix's own
`ou=teams`. Every node is listed under the one above it: the four units are
the suffix's children whether or not a tenant has anything in them, and a
subtree search of any name holds that name and everything under it.
`--base contoso=dc=contoso,dc=example` renames the suffix for a
deployment whose clients already expect one -- and ties the directory's start to
contoso: a `--base` for a tenant this directory does not front is refused, as
everything it is told and cannot do is, so a tenant that ends the directory's
nomination stops it starting until the flag goes. So are two tenants given one
suffix, naming both -- a `--base` that is another tenant's `o=<alias>`, say --
since a name under it would be read as one of them and nobody of the other
could bind. A tenant that cannot be read
for any other reason -- the holder it nominated disabled, its role narrowed --
is skipped with a warning and the rest are served. Nothing below the suffix is
configurable: a directory's shape is what clients are configured against once,
and two deployments of roster should agree on it.

### A person

`objectClass: top, person, organizationalPerson, inetOrgPerson`.

| attribute | from | |
| --- | --- | --- |
| `uid` | `Holder.alias` | the RDN |
| `cn` | what they are called now: `Profile.display_name`, else `Holder.name`, else `Holder.alias` | one value, and never missing. The display name is what every write that renames somebody writes; `Holder.name` is what they were added with, which nothing changes |
| `sn` | the same string as `cn` | `person` requires one and roster does not split names; the whole name, rather than a guess at half of it. No `givenName` is published, for the same reason |
| `displayName` | `Profile.display_name`, else `Holder.name` | absent for somebody who has neither, so it is a token's `name` exactly: what `MeService/Get` says they are called |
| `mail` | `Email.address`, **verified rows only** | an unverified address is somebody's claim, and a directory is what other systems believe. In the order they were proved, so the first value is the address a token's `email` says |
| `employeeNumber` | `Profile.employee_no` | compared as the characters it is written in: `(employeeNumber=ab07)` does not find `AB07` (§ [Search](#search)) |
| `departmentNumber` | `Profile.department` | |
| `preferredLanguage` | `Profile.locale` | |
| `labeledURI` | `Profile.picture` | |
| `telephoneNumber`, `mobile`, `facsimileTelephoneNumber`, `homePhone`, `pager` | `Phone.number`, by its `kind` | one of no kind, or of a kind with no attribute of its own, is a `telephoneNumber`, and a number that is two of those is there once; read only when asked for or filtered on, since it costs a list |
| `manager` | the DN of who they report to | a `Relation` of theirs labelled `manager`, which is what a directory provisioning them writes ([scim.md](scim.md)); read only when asked for or filtered on |
| `memberOf` | the DNs of their groups and teams | AD's convenience, and the one most clients ask for; read only when asked for or filtered on, since it costs two lists. A group or a team that has been erased is not named, and whoever was in it is in the tree as before |
| `entryUUID` | `Holder.id` | |

Never `userPassword`, never a key, never anything from `Credential`: roster
does not hand those out over its own wire and this process could not fetch
one to leak. Somebody disabled (`date_disabled`) or erased is not in the tree
at all rather than present with a flag, for the reason `Holder.date_disabled`
gives -- a flag is a thing a client has to know to read.

**What a client keys on is `entryUUID`.** It is roster's own identifier for
the person: it never changes, and it is never anybody else's. It is an
operational attribute, so it is answered only when asked for by name (or with
`+`), and `(entryUUID=…)` finds its person in one read. Everything else that
looks like a key is what somebody is called. `uid`, and so the DN, moves when
they are renamed (`HolderService/Realias`): the old DN is `noSuchObject` from
the next search. And a name comes free when somebody is erased, so the next
person to be called `lee` is `uid=lee,ou=people,o=contoso` with another
`entryUUID`: a client that keys an account on `uid` or on the DN hands the
newcomer the account of whoever had the name before. `cn` is what somebody is
called now, for showing, and changes whenever they say.

**Whose word an attribute is.** A tenant lets its people write their own
profile unless it says otherwise -- the role `self-service` names
`HolderService/Update`, and a tenant turns that off by taking it out
([usage/permissions.md](usage/permissions.md)). Where they may, these are what
the person says of themselves, checked by nobody: `displayName` and so `cn`
and `sn`, `departmentNumber`, `employeeNumber`, `preferredLanguage` and
`labeledURI`; and their numbers, which they add the same way
(`PhoneService/Add`). A filter on a department, or a rule a client keeps
about an employee number, reads a value its subject can write. `mail` is the
other kind: an address is published once somebody proved it, and not before.
`uid`, `manager` and `memberOf` are written by verbs that role may not name,
and `entryUUID` by nobody.

**A machine is not a person here.** A tenant's holders are not all people: each
front door is answered as one, and so is this directory. Two kinds are left
out, by the same test the SCIM endpoint asks ([scim.md](scim.md)): a
holder a file or a provisioner declared -- the label `roster.declared`, which
`roster login provision` and `roster account provision` write on theirs and a
`resources:` file on what it declares -- and the holder this directory's key is
answered as in the tenant, which `roster app install … directory` nominated.
Neither has an entry, by listing or by name; neither is a `member` of a group
it was put in, and neither is anybody's `manager`. A tenant's first
administrator carries no such label and is a person.

What that test cannot see stays in `ou=people`. An app installed by hand
(`roster app install`, for a Jenkins, say) is a holder like any other, and so
is the one `roster scim provision` makes for a tenant's directory. And a
tenant's own copy (`--key`) does not know which holder its key hangs off -- the
key names no read that says -- so that holder is listed too. A client that
imports users filters them out by what the tenant knows of them: a group, or
`(!(uid=jenkins))`.

### A group, a team, a site

`groupOfNames`, `cn` the alias, `description` the note or, where there is
none, the name, `member` the DNs of who is in it -- read from
`GroupMembership.List` and `TeamMembership.List`. A
team's DN carries its site, because a team is a group of people **within one
site** (`entity.md`). A site is an `organizationalUnit`. Nested groups are not
here because they are not in roster (`position.md` § What we deliberately do
not have).

**A group with nobody in it has no `member`.** `groupOfNames` requires one
(RFC 4519), and a group whose every member is suspended, erased or a machine
is the same case. This tree does not invent a member to satisfy the class: a
name that is nobody's is a name a client would grant on. A client that checks
entries against the schema reports such a group; one that reads `member`
reads none, which is the truth.

**Every group and every membership is published to whoever binds.** A
`GroupExposure` says which of somebody's groups a token carries to which
client, and it has no say here: it is about tokens, and a directory is one
view for everybody who binds
(§ [Whose eyes a search uses](#whose-eyes-a-search-uses)). A tenant that
shows a client two groups in a token and points the same client at this
directory has shown it all of them, with who is in each.

### What a client grants on, and what roster guards

`member` is the attribute the schema defines (`groupOfNames`, RFC 4519);
`memberOf` is its reverse, Active Directory's convenience, and says nothing
`member` does not. Neither means *may do something* -- but a client that maps a
group to its administrators turns membership into a permission, and roster guards
only the permissions it knows of. Joining a group is refused to anybody who does
not hold what that group's **bindings** hand out (`server/core/escalate.go`), so a
group nothing is bound to can be joined by anybody who may call
`GroupMembership.Add`, and a team membership with no role grants nothing roster
can see.

So: **map a client's permission to a group, and bind that group to a role naming
the permission** -- `/ext.jenkins.Admin/*`, a method no RPC answers to -- so that
joining it is held to the rule ([usage/permissions.md](usage/permissions.md) §
*Membership is not a permission, outside roster either*). Teams are published here,
`memberOf` included, because a directory is also an address book and an
organisation chart; they carry no binding, so nothing maps a permission to one
safely. That is a decision about what this tree shows, taken knowing it, and
revisited if a client turns out to have no way to tell the two apart.

## Search

Base, one-level and subtree; filters with `&`, `|`, `!`, equality, presence
and substrings over the attributes above, evaluated straight off the BER
tree the request arrived as -- a filter on the wire is already a tree, and
there is no string to parse. Every attribute but one matches without regard
to case, which is what the matching rules of the attributes in this tree say.
The one is `employeeNumber`, which is compared as the characters it is written
in, in an equality and in a substring: roster's own `Search` compares an
employee number exactly, and a filter means the same whether or not it is the
read. An attribute that holds a name -- `memberOf`, `member`, `manager` -- is
compared as a name: `(memberOf=cn=payroll, ou=groups, o=contoso)` and
`(memberOf=CN=Payroll,OU=Groups,O=Contoso)` are the group
`cn=payroll,ou=groups,o=contoso`, so a filter that keeps a group out with a
`!` keeps it out however the name is spelt. Ordering, approximate and
extensible matches are *undefined* in RFC 4511's sense and match nothing, so
`(|(uid=kim)(cn~=kim))` still finds kim; so is an equality on one of those
three whose value is not a name, which matches nobody and whose `!` matches
nobody either. A value with nothing in it -- `(cn=)`, or a substring whose
fragments are spaces alone, `(cn=* *)` -- matches nobody, with success: no
attribute here has an empty value. What decides the cost is which roster read
a filter becomes:

| the filter says | this process calls |
| --- | --- |
| `(uid=kim)` | `Holder.Get` by alias, once: what was asked is folded to small letters, which is how roster keeps an alias, so `(uid=KIM)` is the same read and a name nobody has is one read of nobody |
| `(entryUUID=…)` | `Holder.Get` by identifier |
| `(mail=kim@contoso.example)` | `Email.Get` by address at the tenant, normalised as `Email.Add` stores it, then `Holder.Get` for whose it is |
| `(cn=Minji)`, `(sn=…)`, `(displayName=…)` | `Holder.Search{q}`, which is a contains over the alias, the name somebody was added with and the display name -- every string those three attributes can be; the filter then keeps the exact ones |
| a substring on `uid`, `cn`, `sn`, `displayName` | `Holder.Search{q}`, for its longest fragment |
| a value with nothing in it | nothing: it matches nobody |
| `(departmentNumber=…)`, `(employeeNumber=…)` | `Holder.Search{department}`, `{employee_no}` |
| `(memberOf=cn=payroll,…)` | `GroupMembership.List` or `TeamMembership.List` by the group or team named, and `Holder.List` for its members, thirty-two a call. A member is not asked again which groups they are in |
| anything else, or nothing | `Holder.List`, and the filter is evaluated on what comes back |

In an `&`, the most selective child is the plan and the whole filter is then
evaluated over what it read. An `|` whose every arm is a row of the table is
each of those reads, one after another, and whoever two of them read is
answered once: `(&(objectClass=inetOrgPerson)(|(uid=kim)(mail=kim@contoso.example)))`,
the filter many clients ship with for a sign-in, is a `Holder.Get` and an
`Email.Get`. An `|` with an arm that is none of them -- a `!`, a presence, an
attribute roster has no index for -- is a `Holder.List`.

**What is read of a person, and when.** Their own row carries most of the
table in § [A person](#a-person). Five lists do not: their addresses, their
numbers, the groups and the teams they are in, and who they report to. The
filter is asked of the row first, and whoever it rules out is read no further,
so a filter the row alone answers reads no list of anybody it drops. A list the
filter names is then read for whoever is still undecided, and a list the client
asked to be sent for whoever the filter kept. Each is read for a page of people at once:
roster's lists take thirty-two filters a call, so a page of a hundred people is
four calls of each list it needs and not a hundred. The `member`s of a group
are read the same way, and only for a group the filter keeps.

The wall is already in every one of those reads, so a search under
`o=contoso` can never answer a row from `o=newco` -- the two suffixes are two
keys.

### A size limit, a page and its cookie

**A size limit** is the client's `sizeLimit`, and it is of the whole search: a
search that would answer more ends with `sizeLimitExceeded` (4) once that many
entries have been sent, in one answer or over however many pages it is read in.

**A page** is the paged results control's (RFC 2696). It is never bigger than
the client asked, and never bigger than a hundred, which is the most roster
reads at once (`SearchPageLimit`). Every part of the tree is read through
roster's own cursor -- the people, the members of the group a `(memberOf=…)`
filter names, the groups, the sites, the teams -- so a client paging through
ten thousand people, or a group of a thousand, reads each once. A subtree
search from a suffix (or from the root, which is the bound tenant's suffix and
no other) sends the parts one after another: the fixed nodes, the people, the
groups, the sites, the `ou=teams` under each site, the teams.

A page ends when it is full, or where a read of roster ended that put something
in it. So a page can be shorter than was asked -- the filter dropped rows, or
somebody on roster's page is not in the tree -- and a page with nothing in it
yet reads on: only the last page can be empty, which it is where the page
before ended full and nothing after it matched. A page size of zero with the
cookie of the page before ends the paging, as RFC 2696 has a client say it
wants no more: it answers nothing, and no cookie. And a page size no smaller
than the size limit is the search in one, which is RFC 2696 too: the control is
set aside, and the answer is as many entries as the limit and
`sizeLimitExceeded` where there were more.

**The cookie** is where the search stopped, and nothing is kept on the server
for it: another connection and another replica of this process read on from
one. A client treats it as RFC 2696 says, opaque. It is not secret, and anybody
who looks can read it -- JSON, unsigned:

| | |
| --- | --- |
| `s` | which part of the tree the search was in |
| `p` | which read of an `\|` it was in, among the people |
| `a` | where in that part: roster's own cursor, which is the moment the last row read was made and that row's identifier |
| `n` | how many entries the pages before held, which is what a size limit is counted from |
| `t` | the tenant's alias, for a search from the root |

The row a page ended at is the last one **read**, shown or not: the identifier
of somebody suspended, who has no entry, can be in a cookie, with the moment
their account was made. It names nobody and opens nothing -- each read it
resumes is made with the key of the tenant the connection is bound in, so a
cookie presented by somebody bound elsewhere reads their own tenant. One that
is no cursor is refused `protocolError` (2); one whose `n` a client rewrote
moves that client's own size limit and nothing else.

The reads of groups, teams, sites and people one search makes are kept for
that search -- a group of forty and a person in six teams cost a read per
row, not per mention -- and dropped with it.

**No cache of what a search answers.** A search is a read of roster, every
time: who is in the tree, what they are called and which groups they are in
are read for each answer and kept only until it is sent. A cache is exactly
where somebody `Holder.Disable`d stays visible for another hour, and the
clients that matter (a VPN) ask on every sign-in and can afford the round
trip. What this process learned as it started is another matter: which
tenants it fronts and what each is called are read then
(§ [Running it](#running-it)). If a deployment's address book hammers it, the answer is `SyncService`
feeding a cache *in that client*, which is where a stale entry is that
client's problem and not this process's.

## What is refused, and how

| operation | answer | why |
| --- | --- | --- |
| Add, Modify, ModifyDN, Delete | `unwillingToPerform` (53) | a directory front is a **read**; the write side of this is SCIM ([scim.md](scim.md)) |
| Compare | 53 | nobody's client does this, and `userPassword` compare is a bind that does not count toward a lockout |
| Password Modify extended op (RFC 3062) | 53 for now | it could be `Credential.Set` with `current`; the question is whether a client that cannot do a second factor should change a password -- decision 6, below |
| SASL bind | `authMethodNotSupported` (7) | above |
| StartTLS | **supported**, and LDAPS beside it | a simple bind over plain TCP is the password in the clear; `--tls cert,key` offers StartTLS on `--listen` and enables `--listen-tls`, and `--require-tls` refuses a bind in the clear with `confidentialityRequired` (13). The root DSE lists it where it would be taken -- a certificate was given, and the connection is not under TLS already -- and with no certificate the request is `protocolError` (2) |
| WhoAmI (RFC 4532) | **supported** | free, and what a client uses to see a bind took; it answers the name of the entry, as the tree spells it, whatever spelling the client bound with |
| an unknown critical control | `unavailableCriticalExtension` (12) | ignoring one would answer a question the client did not ask |

## The key this process holds

One deployment key, allowed as itself only `/roster.NominationService/List` --
the tenants it serves -- and in each tenant a holder it is answered as, whose role
names what a directory reads. `roster app install --tenant … --role … directory`
writes the holder, the role, the binding and the nomination:

```
/roster.TenantService/Get             # at start: which tenant this key is, and that it sees it
/roster.VouchService/Verify           # --bind password|either only
/roster.HolderService/Get
/roster.HolderService/List
/roster.HolderService/Search
/roster.EmailService/Get
/roster.EmailService/List
/roster.PhoneService/List             # a role that does not name it publishes no numbers, and nothing else changes
/roster.RelationService/List          # nor a `manager`, the same way
/roster.GroupService/Get
/roster.GroupService/List
/roster.GroupMembershipService/List
/roster.SiteService/Get
/roster.SiteService/List
/roster.TeamService/Get
/roster.TeamService/List
/roster.TeamMembershipService/List
```

`docker/customer.sh` installs exactly this for the compose stack (`Verify`
included, since `LDAP_BIND` is a switch there), and a role that
holds less answers `insufficientAccessRights` (50) with roster's message where
the missing method would have been read, rather than an empty tree.

The list is typed where a role is written, and this is the one the others are
held to: the rig's seed and the roles the tests give a directory are compared
with it by `TestTheDirectorysKeyIsTheManualsList` and
`TestTheKeyOfTheseTestsIsTheManualsList`. The Login App, the account app and
the SCIM endpoint each have a `provision` command that writes their role from
a list in the code; the directory has none, so an operator copies this one. A
command for it adds to the command line and is left for later.

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
| `cli/ldap.go` | `roster ldap serve`: `--listen` (`:389`), `--listen-tls`, `--roster`, `--insecure`, `--deployment-key` (or `--key`/`ROSTER_LDAP_KEY_<ALIAS>` for a tenant's own copy), `--base`, `--bind`, `--tls`, `--require-tls` |
| `docker/ldap.sh`, `compose.yaml` | the `ldap` service beside `account`, on `1389`, its key from the same `customer` one-shot; `LDAP_BIND=either` turns password binds on |
| `docs/operating.md` | § "One process, or five" -- where the `ldap:` block sits |
| `docs/usage/ways-in.md` | a paragraph under the tenant key: an app password is a key |
| `docs/baseline.md` | § A directory over LDAP, one row per promise below |

### The wire is ours

There is no server library in this. `ldap/wire` writes its answers with
`github.com/go-asn1-ber/asn1-ber`, which is the BER codec `go-ldap` already
brings in, so the dependency graph grows by nothing. A request it reads
itself, into that library's tree (`ldap/wire/read.go`): the library's own
reader is held to two gigabytes and a thousand levels and keeps a copy of a
value at every level above it, and what reads a stranger's bytes holds a
message to a length, a count and a depth before it makes anything of it
([rules.md](usage/rules.md) § *What a connection to the directory may cost*).

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
| `(uid=…)`, `(mail=…)`, a substring, a department, an employee number, `memberOf`, and the boolean shapes over them each answer the same people the corresponding roster read does, without regard to case but for an employee number; a person's attributes are the table above and nothing operational unasked | `TestAFilterIsARosterRead` |
| groups and teams name their members and nobody disabled; a site holds its teams and a team with no site is under the suffix; `memberOf` agrees with `member`; the whole suffix read in one search and in pages is the same tree | `TestGroupsTeamsAndSitesAreTheTree` |
| somebody who was in a group or on a team that has since been erased is in the tree as before, found by every search that found them, with the groups and teams they are still in; `memberOf` goes on agreeing with `member` | `TestSomebodyIsInTheDirectoryWhateverBecameOfAGroupTheyWereIn` |
| a filter on `memberOf`, `member` or `manager` compares a name as a name, however it is spaced or capitalised, and so does its `!`; a value that is not a name matches nobody, and neither does its `!` | `TestANameIsComparedAsAName` |
| `cn` is what somebody is called now -- the display name, else the name they were added with, else the alias -- one value and never missing; `sn` is the same string; `displayName` is a token's `name` exactly, absent where a token says none; somebody renamed is found by the new name and not the old; no `givenName` | `TestSomebodyIsCalledWhatATokenCallsThem` |
| `mail`'s values are in the order they were proved, the first being the address a token's `email` says; an address nobody proved is not published and not found | `TestTheFirstMailIsTheAddressATokenSays` |
| a holder a file or a provisioner declared, and the directory's own, are no entry by listing or by name, no `member` and nobody's `manager`; a tenant's first administrator is a person | `TestAMachineIsNotAPersonInTheDirectory` |
| `employeeNumber` minds case, alone and inside an `or`, in an equality and in a substring | `TestAnEmployeeNumberMindsCase` |
| an equality or a substring with nothing in it answers nobody, with success | `TestAFilterThatAsksForNothingAnswersNobody` |
| a lookup costs what it reads, whatever the tenant's size: the sign-in filter `(&(objectClass=inetOrgPerson)(\|(uid=…)(mail=…)))` is a `Holder.Get` and an `Email.Get`; a name nobody has is one read; `(entryUUID=…)` is one read by identifier; a filter that keeps nobody reads no list; a page of a hundred people is four calls of each list it needs; a group's members are read thirty-two a call and not asked again which groups they are in | `TestALookupCostsWhatItReads` |
| an `\|` is each of its arms and answers everybody once, in one search and in pages | `TestAnOrAnswersEverybodyOnce` |
| a role that names no read of numbers or of relations publishes none of either, asks once a search, and answers everything else | `TestARoleThatNamesNoNumbersPublishesNone` |
| paging through everybody answers each person once, with a filter that drops rows and with one roster has no index for; the client's size limit is honoured | `TestPagingReadsEverybodyOnce` |
| no page is bigger than was asked, in any part of the tree -- the suffix, the groups, the sites and teams, the members of a group, an `\|` -- and what a search answers in pages is what it answers in one, each entry once | `TestAPageIsNoBiggerThanWasAsked` |
| no page is empty but the last: `(uid=kim)` from a suffix is one page | `TestNoPageIsEmptyButTheLast` |
| a size limit is of the whole search, over however many pages; a page no smaller than the limit is the search in one | `TestASizeLimitIsOfTheWholeSearch` |
| a page of zero with a cookie ends the paging | `TestAPageOfNothingEndsThePaging` |
| every entry a search answers has its parent answered beside it and is listed under it, the suffix's own `ou=teams` among them; a subtree search of a name holds the name; a group with nobody in it is a `groupOfNames` with no `member` | `TestTheTreeListsEveryNodeItAnswers` |
| `entryUUID` is the same through a rename and never anybody else's, where `uid` and the DN move and are used again; it is answered only when asked for, and found by it | `TestAClientKeysOnEntryUUID` |
| the methods a directory's key holds are this page's list, in the rig's seed and in the tests that stand a directory up | `TestTheDirectorysKeyIsTheManualsList` · `TestTheKeyOfTheseTestsIsTheManualsList` |
| a cookie holds where roster was read to and how many entries went before, names nobody, is read on from by another connection and another process, and reads only the tenant its presenter is bound in; one that is no cursor is refused | `TestACookieIsWhereRosterWasReadTo` |
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

- **SCIM**, the write side, is [scim.md](scim.md) now: a directory
  provisioning people *into* roster, a second consumer beside this one. It
  turned out not to be `Holder.Add` and `Email.Add` behind a JSON schema --
  those reach people who already exist, and a directory's key should reach only
  the people it makes -- so it is `HolderService.Provision` and the verbs beside
  it, and the groups it keeps them in.
- **Kerberos, certificates, nested groups**: `position.md` says why, and
  this process does not change the answer.
- **More profile fields**: the conversation that produced `Holder.Search` said
  these arrive as proto additions when a use names them. The two a directory
  named first are here -- where somebody is reached, and `manager` (§ *A
  person*) -- and neither turned out to be a field of the profile.

## Progress

| | | |
| --- | --- | --- |
| L0 | the wire | **done** — `ldap/wire`: the loop, simple bind, search, unbind, abandon, StartTLS (and LDAPS as the listener's), WhoAmI, paging, the refusal table. Proved against `go-ldap` as the client and by hand for what it has no call for (abandon); under the race detector |
| L1 | people | **done** — `ldap/`: the tree above the people, `ou=people` with every attribute in the table (`mail` verified only, disabled absent), bind in all three modes with roster's `ok` as the second-factor rule, search planned into `Holder.Get`/`Holder.Search`/`Email.Get`/`Holder.List` and evaluated off the BER tree, paging on roster's own cursor, `uid` and `mail` found without regard to case. `roster ldap serve` with `--key`/`ROSTER_LDAP_KEY_`, `--base`, `--bind`, `--tls`, `--listen-tls`, `--require-tls`. The import check learned `ldap/`. Eight tests in `ldap/`, one in `cmd/` |
| L2 | groups, teams, sites | **done** — `ou=groups` as `groupOfNames` with `member` the DNs of people in the tree (the disabled are not named), `ou=sites/ou=<site>/ou=teams/cn=<team>` and `ou=teams` under the suffix for a team with no site, `memberOf` on a person from the other end, `(memberOf=…)` planned into one membership list. A subtree search from a suffix or the root pages in stages -- people on roster's cursor, then the groups, then the sites -- carried in the cookie, so a client paging from the root sees every entry of the tenant it is bound in once. `TestGroupsTeamsAndSitesAreTheTree` |
| L3 | shipped | **done** — the `ldap` compose service on `1389` with its key from `customer.sh`, `operating.md` § "One process, or five", `usage/ways-in.md` on app passwords, `baseline.md` § A directory over LDAP, the account page's *mint an app password* form and its spec, and this file as a description |
