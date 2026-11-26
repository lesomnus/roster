# An app beside roster, in three shapes

A product -- Loft, levyan, anything that keeps its own rows and asks roster who
somebody is -- runs in one of three shapes, and the shape decides which key it
holds, how it sends a request, whose name ends up in a tenant's trail, and
which tenants it may reach. This page is that decision, and what an app's own
code can and cannot hide about it.

## Four questions, and where each is answered

**What is an app, here?** A holder of the tenant, and no row of its own: the
holder its key is answered as there. A role, a group or a key that names that
holder in `app` is the app's, and none is roster's or the tenant's own. A roster
operator puts an app into a tenant with `roster app install`, which writes the
holder, a role of roster's for what the app may call at roster, a role of the
app's for what it serves, and who administers it -- § [What an app is,
here](#what-an-app-is-here).

**How is a call decided?** A call to roster is decided by roster: who the
credential is, a role of roster's naming the method, the rows that grant
reaches, the rules every caller is held to, and, for a write a deployment names,
its own hook. A call to the app is decided by
the app: it learns who the person is from the credential they came with, asks
roster what they may call **at this app** (`HolderService/Reaches`, answered of
the app's roles alone), and serves the method where one pattern covers it.
`reach` is that question as a Go package -- § [How a call is
decided](#how-a-call-is-decided).

**How does an app give its people API tokens?** It mints none itself: a key
for the app (`ApiKey.app`) is minted by `ApiKeyService/Issue` as the person,
held to what they hold at the app -- by the app's server through a delegation
the person approved at the front door, or by a terminal's sign-in they approve
there. The app lists and ends the keys that are for it, and checks one it is
handed with `TokenService/Introspect`, which answers a key to the app it is for
and to no other -- § [Giving an app's users API
tokens](#giving-an-apps-users-api-tokens).

**How does an app read its people, and what each may do?** They are the
members of its groups and the people bound its roles, read with the lists of
those rows, which answer the app its own, take `app` and bring along what a row
names (`select`); what each may call is `Reaches`, and
`SyncService/Watch` says when either may have changed. A holder labelled
`roster.app` or `roster.declared` is not a person -- § [Reading an app's users
and what each may do](#reading-an-apps-users-and-what-each-may-do).

It is about an app as a **caller of roster** and as a **caller of other apps**.
How a person's browser signs in to it is [relying-party.md](relying-party.md);
the Login App, which is the first app that took the roster-hosted shape, is
[login.md](login.md). The words are [glossary.md](glossary.md)'s: a **roster
operator** runs the deployment and is a holder of the control plane; a
**tenant administrator** is a roster user whose bindings administer their tenant.

## What an app is, here

**An app is a holder of the tenant.** There is no row of an app's own: what
roster knows of one is the holder it is answered as in a tenant -- the one its
own `rt_` hangs off (C), or the one the tenant nominated for its deployment key
(A and B, § *The three shapes*). Everything else that is the app's says so by
naming that holder:

| row | says whose it is by | and none is |
| --- | --- | --- |
| `Role` | `app`, fixed when it is made: a role of the app's names the app's methods, and is read by that app's `Reaches` and by nothing else | a role of roster's, read by roster's gate |
| `Group` | `app`, fixed when it is made: a group of the app's takes the app's roles and no other | the tenant's own |
| `Binding` | of an app's role, nothing: the role says. Of a role of roster's, `app`, which narrows it to that app's rows (§ *An app's administrators*) | across the tenant, or in a site |
| `ApiKey` | `app`: a key spent at the app, and no credential at roster | a key of roster's, answered to no app |
| a delegation | `issued_to`, the holder that presents it | -- |

So one tenant holds many apps, and each is answered its own. Loft's holder,
installed as below, lists roles and groups and is answered Loft's and not
levyan's, and asks what somebody may call and is answered of Loft's roles
alone; levyan's holder asks the same of the same person and is answered of
levyan's. What ends an app's holder ends what is its own -- its roles, its
groups, the bindings and the keys that are for it -- in one act, and a restore
of the holder brings back none of them (`server/core/takeaway.go`).

### Installing one: `roster app install`

A roster operator does it, at the box, once per tenant (#75):

```sh
roster --as admin app install --tenant acme \
  --allow '/roster.HolderService/Reaches,/roster.SyncService/Watch' \
  --serves '/loft.*/*' \
  --administer admin \
  loft
```

```
roster: @acme/loft may call 2 method(s) in acme through role loft-app.
roster: @acme/loft holds 1 method(s) of its own at itself through role loft-everything.
roster: @acme/admin may hand on loft's methods at @acme/loft through role loft-everything.
roster: acme has role app-administrator, naming the 23 method(s) of roster's that reach an app's rows.
roster: @acme/loft reads and writes what is its own at roster through role app-administrator, for itself.
roster: @acme/admin administers @acme/loft at roster through role app-administrator, for @acme/loft.
roster: loft's key is answered as @acme/loft in acme.
roster: loft has no key yet: roster control key add --narrowed loft.
```

Row by row, and the flag each comes from:

| it writes | from | what for |
| --- | --- | --- |
| the holder `@acme/loft`, made if there is none, labelled `roster.app: loft` | `APP` | who the app is in the tenant |
| `loft-app`, a role of roster's, bound to the holder | `--allow`, required: methods roster serves, and nothing else | what the app may call at roster |
| `loft-everything`, a role of the app's (`Role.app`), bound to the holder and to each person `--administer` names, by alias | `--serves`, the app's methods as patterns | the app holds what the roles it ships will name; the people hold the app's methods at the app and hand them on there |
| `app-administrator`, a role of roster's naming `core.ForAnApp`, made the first time a tenant needs it, bound **for the app** (`Binding.app`) to the holder and to the same people | `--serves` | the app reads and writes what is its own as itself; the people are its administrators at roster (§ *An app's administrators*) |
| a holder `loft` on the control plane, made if new, and a `Nomination` in the tenant answering a key on it as `@acme/loft` | `APP` | a deployment key (A, B); a C app's `rt_` never asks it |

It mints no key, and says which command does:
`roster control key add --narrowed loft` for A and B, and for C a tenant key
on the holder it made,
listing every method the app calls with it -- of `loft-app`, and of
`app-administrator` where it writes its roles or reads its keys -- since a key
narrows its holder and never widens it:

```sh
roster --as admin key add --tenant acme --holder loft --name loft \
  --allow '/roster.HolderService/Reaches,/roster.SyncService/Watch'
```

With no `--serves` there is neither `loft-everything` nor `app-administrator`,
and `--administer` without it is refused; a name that is nobody of the tenant is
refused before anything is written, and so is a `loft-everything` already there
that is roster's or another app's, a `loft-app` that is not roster's, and an
`app-administrator` of the tenant's naming more than reaches an app's rows. A
role of roster's naming what roster does not serve -- `--allow '/loft.*/*'` --
is refused by the write itself, after the holder is made, which a second run
finds holding nothing and takes.

It is the roster operator's because of the last two flags. What somebody holds is
held at an app, and nobody hands out what they do not hold there -- so nobody in
the tenant holds anything at an app that has just arrived, and nobody could give
the app's holder its first role there, or themselves the right to manage it. The
tenant's administrators' own role is not written to: binding a role of the
app's is `BindingService/Add`, which they hold, and what they bind is held to
what they hold at the app. From then on the holder's bindings, its roles and
the nomination are the tenant's. Installing again leaves a role the tenant
reshaped as they have it, and puts back a nomination or binding that is gone --
it is how an operator puts an app back after a tenant ended it, which the tenant
usually cannot do itself: nominating the app's holder needs what it holds.
`roster app uninstall --tenant acme loft` ends the nomination, and leaves the
holder, its roles and their bindings, as it always left the first; the tenant
administrator can end the nomination from the user console's *apps* tab. The Login App and the account app do this for themselves at start, in
every tenant with a name that is not being destroyed -- so for them ending it
lasts until the next start.

**A program that carries roster inside itself** -- a `cmd.Server` it built
with `cmd.Build` from a database and no `control` -- installs an app with
`cli.InstallApp`, the command without the process: a `cli.AppInstall` holds
the argument and the flags by their names, and it answers the same
sentences. Such a deployment has no control plane, so the table's last row is
not written -- no holder there, no nomination, no key listed -- and it says
so: *this deployment has no control plane, so nothing nominates shale in acme
and no key is answered as @acme/shale.* Everything else is written, read
first and refused as above.

**Roster's own front doors are declared.** The Login App and the account app
write their rows in every tenant at start -- the holder, its role, the binding
and the nomination -- because the deployment's configuration turned them on, so
those rows carry `roster.declared` and are read-only to everybody at a port: no
tenant ends them, narrows them or disables them (`server/core/declared.go`).
Turning one off is taking it out of the configuration, then erasing what is left
from the box ([operating.md](operating.md), *Declared rows*). An app installed
with `app install` is not declared: it is the operator's act, and the tenant's
from then on.

**An installed app's holder says it is an app's.** `install` labels the holder
it makes, or takes with `--adopt`, `roster.app`, the name the app was installed
under, and a second run puts it back where it is gone; `uninstall` leaves it,
since the holder is still an app's. A tenant's holders are not all people, and
**a directory built on roster skips a holder that carries either label,
`roster.declared` or `roster.app`**, as roster's own do -- the LDAP directory,
the SCIM endpoint and `roster scim adopt` ask that one test
(`rstr.Holder.NoPerson`) -- rather than leaving out itself by name. Both are
roster's to write, and a request at a port that carries either is refused.
Nothing reads `roster.app` to decide what anybody may do; `install` reads it to
know the app's own holder again.

**A holder of the app's name that is already there is not taken silently.** It
is somebody's -- a person, an earlier app holder with a key and a role -- and
the app's key would be answered with everything it holds, while whoever signs
in as it would hold the app's methods. `install` refuses it unless it is the
app's own -- it carries `roster.app` with the app's name, so a second run goes
on whatever it holds, the app's own key among it -- or holds nothing but the
app's own role, and `--adopt` says to use it anyway, printing what it held. The start-up provisioning of roster's own apps skips such a
tenant and says why, rather than failing for every tenant. A holder of the
app's name that carries another app's `roster.app`, or none, is refused as
before, whatever key it holds.

**Somebody has to hold an app's methods at the app before they can grant them.**
Nobody hands out what they do not hold, and it is asked at the app the role
names: what somebody holds at roster hands out nothing at an app, and the other
way round. A tenant's first administrator is bound `/roster.*/*`, which is
nothing at Loft. So a roster operator, at the box, starts it: `roster app
install --serves … --administer …` binds the people it names a role of the
app's naming its methods, and from there it is the tenant's to hand on, at that
app.

### The roles an app ships

An app that comes with roles -- Loft's owner, administrator, manager, member
and auditor -- writes them into the tenant itself, as the holder it is there,
and from then on they are the tenant's: bound to people in the user console,
given more methods or fewer there (`Role.Widen`, `Role.Narrow`), replaced or
erased. Nothing here is a privilege only an app has; it is three ordinary facts
lined up.

1. **The app holds what it writes.** A role is a grant when it is written, and
   nobody hands out what they do not hold at the app it names
   (`server/core/escalate.go`). So an app is installed with its own package in
   a role of its own (`--serves`), and writes and widens its roles with the
   verbs `app-administrator` names, which `install` binds it for itself:

   ```sh
   roster --as admin app install --tenant acme \
     --allow '/roster.HolderService/Reaches,/roster.SyncService/Watch' \
     --serves '/loft.*/*' \
     --administer admin \
     loft
   ```

   A pattern, and not today's list of its methods: the app widens its roles by
   methods it has not written yet, and holding the pattern is what lets it.
   It gives nobody anything at the app, which asks roster about whoever calls
   it and is never called as itself.
2. **The people it is to be administered by hold them too** (`--administer`),
   or nobody in the tenant could bind the roles the app writes: binding one is
   a grant at the app, and a tenant's first administrator holds nothing there.
   And they hold roster's verbs for the app (`app-administrator`), which is
   what binding one is at roster.
3. **The app writes with `Role.Add`, and widens with `Role.Widen`.** At its
   own setup -- `loft init` -- each role it ships is added under its alias, as a
   role of its own (`app` is the holder it is), and one already there is the
   tenant's and left as it is. At an upgrade that
   brings a method -- an RPC a member may call, a permission method -- each
   role is widened by it with `RoleService/Widen`, which adds and never takes
   away: a method the role already holds or covers is not added, nothing new
   is no write, and a role the tenant narrowed stays narrowed in everything the
   upgrade did not bring. `Patch` stays closed on the wire; a widen is a patch
   the layer makes, held to the same rule over the role's whole list
   (`proto/ext/app/role_svc.ext.proto`, `server/core/widen.go`).

What the app widens by is its own to keep: the methods each role gains with a
release, and not each role's whole list, or an upgrade puts back what a tenant
took out -- with `Role.Narrow`, in the user console, one method at a time. A
role the tenant made wider than the app holds -- a method the app does not hold
at itself -- is refused a widen, as a patch of it would be, and is the tenant
administrator's to change, who holds what is in it.

Somebody has to be bound to the first of them. An administrator of the tenant
does it, or an app's administrator (§ *An app's administrators*), who holds
`BindingService/Add` for the app and the app's methods at it; an app's setup
that names its first owner does it itself, with the `BindingService/Add` it
holds for itself -- a grant like any other, and one the app holds at itself. A role of the app's is bound to a person
or to a group of the app's (§ *An app's groups*), and held in no team. An app running roster in its own process writes the same
rows through that roster's own door, and calls `Holders.Recheck` after.

## The three shapes

| | **A** roster-hosted, shared | **B** roster-hosted, one per tenant | **C** self-hosted |
| --- | --- | --- | --- |
| who runs the instance | a roster operator | a roster operator | the tenant |
| how many tenants one instance serves | many | one | one |
| its rows | one database, partitioned by tenant | its own database, or a shared one partitioned by tenant | the tenant's own |
| the key it holds | one `rk_` | one `rk_` | one `rt_` |
| where that key's holder is | the control plane | the control plane | the tenant |
| who it acts as in a tenant | the holder that tenant **nominated** for it | the holder that tenant nominated for it | the holder its key hangs off |
| what a request carries | `authorization: Bearer rk_…` and `roster-at: <the name it arrived at>` | the same, with `roster-at` fixed in its configuration | `authorization: Bearer rt_…` |
| whose name is in the tenant's trail | the nominated holder | the nominated holder | the key's holder |
| who decides what it may do there | the tenant administrator, by that holder's bindings | the same | the same |
| may it act in another tenant | yes, wherever it was nominated | no, provided each instance has its own control-plane holder (below) | no |

The Login App is A when a roster operator runs it for everybody and C when a
tenant runs their own; glossary, *the Login App*.

## A -- one instance, many tenants

```
control plane   @owner/loft ── rk_ (one key; every live key here borrows)

tenant acme     Host  loft.acme.example           names say which tenant
                @acme/loft                        who the app is here
                Nomination  borrower=@owner/loft → @acme/loft
                Binding     role loft-app → @acme/loft

tenant beta     Host  loft.beta.example
                @beta/loft
                Nomination  borrower=@owner/loft → @beta/loft
                Binding     …
```

A request says which tenant it is about -- by a name the tenant answers at, or
by the tenant itself when the app has no name to give (a directory has a DN, a
job its own configuration):

```
authorization: Bearer rk_…
roster-at: loft.acme.example        or        roster-at: @acme
```

roster resolves the name to its tenant through the `Host` (or takes the tenant
as written), then finds the
`Nomination` in that tenant for **the control-plane holder the key hangs off**,
and answers the call as that holder: their bindings, their tenant, the wall
narrowing every read (`server/keys/at.go`). Nothing about the frame is the
key's own -- its `methods` are not consulted on a narrowed call.

What the key's `methods` are for is the call that **does not** name a tenant,
across all of them, as the key. The Login App has two: `WhoseHost`, because it
has to work out whose flow a challenge is before it can narrow, and
`SyncService/Watch`, one stream that hears every tenant's sign-outs. A product
that always knows which tenant a request is about has none, and is minted with
nothing on it:

```sh
roster --as admin control key add --narrowed loft
```

which is refused as itself and answered as whoever each tenant nominated (#74).
An app that has to learn which tenants it serves adds one method:
`--allow /roster.NominationService/List` (§ *One client for all three*).

Three things follow from the nomination being found by the key.

- **A key nobody nominated is refused.** It does not borrow whoever a name
  pointed at; that was `Host.acts_as`, and any `rk_` could (#73).
- **Two apps arrive at one name as two holders.** The Login App narrows to the
  host of a product's redirect, which is the name the product is served at; the
  product narrows to the same name. Two nominations, one per app.
- **Nominating is a way in.** Writing one hands whoever holds the key that
  holder's bindings, so nobody nominates a holder wider than themselves
  (`server/core/nomination.go`).

Setting a tenant up for an A app is `roster app install` in that tenant (§
*Installing one*), and a `Host` for its name, if it is reached by one. A roster
operator does it, once per tenant (#75).

**Every live key on the app's control-plane holder borrows through the
nomination**, not only the one just minted: `install` names them. Rotate by
minting the new key with `--name`, then revoking the old one.

## B -- one instance per tenant

The same as A, with `roster-at` written into the instance's configuration
rather than taken from the request, because there is only one answer.

**Give each instance its own control-plane holder** (`@owner/loft-acme`,
`@owner/loft-beta`). The nomination is found by the key's holder, so instances
sharing one holder share every nomination: acme's instance could name beta's
host and be answered as beta's holder. A holder per instance is what makes
*only acme nominated this one* true in roster, and it is the line that confines
the instance -- not the configuration.

**roster confines who the instance is, and not what its database holds.** Two
instances on one database with one database login are one instance as far as
the rows go: whichever of them is compromised reads every tenant's, whatever
roster would have answered. A shared database needs its own per-tenant
confinement -- a login per tenant with row-level security, or a schema per
tenant. roster cannot do that half.

## C -- the tenant runs it

One `rt_`, on a holder in the tenant, minted by the tenant (`ApiKey.Issue`) or
for them (`roster key add --tenant … --holder …`, § *Installing one*). It
resolves to that holder, in that tenant, with that holder's bindings narrowed
further by the key's own `methods`. No `roster-at`: there is nothing to narrow,
and a request carrying one with an `rt_` is refused.

The tenant sees the holder in their own directory and can revoke the key, which
is the property C is chosen for. Loft runs in this shape, and
[loft.md](loft.md) is what it asks of roster, step by step, and what each step
is on roster today.

**C, held several times.** Somebody who is not a roster operator -- a third party
integrating with several of your customers -- cannot hold an `rk_`: the control
plane is the roster operator's. It holds one `rt_` per customer that minted one,
each answering in that customer's tenant alone. That is not a fourth shape, just
C for each tenant, and it needs nothing from roster that C does not.

The account app and `ldap serve` take `--tenant-key alias=rt_…` for exactly this case,
a tenant running its own copy. Run by a roster operator for everybody they are A,
each with one deployment key (#76).

## How a call is decided

Two calls, decided in two places: one to roster, which roster decides, and one
to the app, which the app decides with what roster answers it about the person.

### A call to roster

In the order it happens ([usage/rules.md](usage/rules.md) § *In what order a
call is decided*):

1. **Who is calling**, by the credential: a console's session cookie; a key --
   an `rt_` as its holder, an `rk_` as itself or, with `roster-at`, as whoever
   that tenant nominated for it; or a delegation in `roster-as` beside the key
   that presents it, answered as the person it is about
   ([usage/ways-in.md](usage/ways-in.md) § *A delegation*). A key's own
   methods, and a delegation's, narrow what its holder holds and never widen
   it. A key for an app is no credential here at all.
2. **The gate**: a role of roster's names the method -- held through a
   binding, a place on a team, a manager's place in a group, or the tenant's
   `self-service` role. An app's roles are not read here: a role of Loft's
   naming `/roster.HolderService/Erase` gives nothing at roster.
3. **The wall**: which rows that grant reaches. Never another tenant's; inside
   the tenant, where the caller holds *the method this call is* -- across the
   tenant, in a site, in a team, in a group they manage, over their own rows
   alone (`self-service`), or **for an app** (`Binding.app`), that app's rows
   ([usage/permissions.md](usage/permissions.md) § *Held for one app*). A row
   out of reach is answered as a row nobody has.
4. **The rules** that hold whoever is asking, one line each in
   [usage/rules.md](usage/rules.md). The three an app's author meets first:
   - **A grant is at one app**: a role of an app's gives nothing at roster, a
     role of roster's names what roster serves -- `/loft.*/*` in one is refused
     where it is written -- and `Reaches` is answered of the app asking
     ([§ Nobody is given more than they were
     given](usage/rules.md#nobody-is-given-more-than-they-were-given)).
   - **Nobody hands out what they do not hold**, asked at the app the role
     names: why an app is installed with `--serves`, and why its first
     administrators are named by `--administer`
     ([§ Nobody hands out what they do not
     hold](usage/rules.md#nobody-hands-out-what-they-do-not-hold)).
   - **Nobody writes a way into an account wider than their own**: a key or a
     delegation for somebody other than the caller is one, compared at roster
     ([§ Nobody writes a way into an account wider than their
     own](usage/rules.md#nobody-writes-a-way-into-an-account-wider-than-their-own-or-takes-one-away)).
5. **The deployment's hook**, where it has one: a write its `hook:` names that
   everything above let through is asked of a service the deployment runs
   (`roster.hook.v1.HookService/Review`), which may refuse it and do nothing
   else; a read or a way in is never asked
   ([operating.md](operating.md#a-hook-a-deployment-refuses-a-write-by)).

### A person calling the app

Whatever the shape, a request from a person arrives with one of:

| credential | how the app checks it |
| --- | --- |
| a session of the app's own, made when they signed in (a password through `VouchService/Verify`, or the issuer's tokens) | the app's own store; and held to roster on every call (`standing`, [relying-app.md](relying-app.md)) |
| an access token from the issuer, `aud` = this app | the issuer's keys (`/.well-known/jwks.json`, JWT access tokens); `aud` must name **this** API; `sub` is a `Holder.id`. See *People's tokens* below |
| an `rt_` of the person's **for this app** (`ApiKey.app`; a script) | `payday.TokenService/Introspect`, which answers a key to the app it is for and to no other: the holder, tenant and the key's methods. A key of roster's, or another app's, is answered as a token that is none (§ *Giving an app's users API tokens*) |
| an `rd_` this app was given | `Introspect`, which answers only the app it was issued to |

and then asks roster what that person may call **at the app**, with
`HolderService/Reaches`, and decides with `frame.Covers` against the method being
called -- reading `everywhere` if the app has no sites of its own. An app asks
about itself, and the answer is of its own roles (`Role.app`): what somebody holds
at roster, or at another app, is not in it. Naming another app, or `roster`, is
answered only to a caller who holds `/roster.BindingService/List` across the
tenant -- or, naming an app, for that app (§ *An app's administrators*) -- since
it adds up what those lists already answer them, which an app installed with
`reach.Methods` does not. `methods` is the gate's union,
which roster's wall then narrows for a grant bound at one site or held in a
team; an app with no such wall would hand that grant everywhere. `everywhere`
is the part that needs no narrowing. And a suspended holder reaches nothing:
their credentials are refused before any of this, and an SSO token minted
before the suspension is answered with nothing to cover.

**What is read to answer it** is the bindings of the app's roles that reach the
person: to them, and to a group of the app's they are in (`cmd/policy.go`).
A role of an app's is held in no team and is no manager's role, so nothing else
carries one. There is no denial and no exclusion: what somebody may call is
the union of what reaches them, and taking it away is ending a binding or a
place in a group (§ *Reading an app's users and what each may do*).

The app's own
permissions are its own RPCs, and anything finer than one RPC is a **method of a
permission service** the app declares in its own proto package -- for example
`loft.AdminService/SeeEveryTenant` -- named in a role like any other
method. **A role says whose methods it names** (`Role.app`, the holder the app
is answered as): a role of Loft's is read by Loft and by nobody else, and gives
nothing at roster or at another app, whatever its methods are called -- a
method's name is no app's, and two apps may both serve one. A role is a bundle
of those; an app asks about the permission and never
about the role's name, so a role can be split or renamed without the app
noticing. Nor about which group or team somebody is in: a membership nothing is
bound to is guarded by nothing ([usage/permissions.md](usage/permissions.md) §
*Membership is not a permission*).

A key's own `methods` -- this app's, since the key is for it -- are not part
of that: `Reaches` answers for the person,
so a call that came with a key whose list is narrower than its holder's is the
app's to hold to that list, as `Introspect` answers it, and `reach` says yes
wherever the holder may. A delegation adds nothing either: an `rd_` the app was
given names who it is about, and what that holder may call at the app is
`Reaches` of them again.

Every shape asks the same questions in the same order, so this half of an app is
written once, here: `reach`, below.

### Asking it: `reach`

The paragraphs above, as a package an app imports beside `standing`
([relying-app.md](relying-app.md)) and wires the same way -- the same
connection, and the same `As` saying who the app is in the tenant:

```go
reaches, err := reach.New(reach.Config{Conn: conn, As: as})

// In the app's gate -- payday's gate.Policy.May -- for every call, where an
// error is the caller's answer: PermissionDenied for a method denied,
// Unavailable for a roster that could not be asked, in the package's words.
err = reaches.May(ctx, call.Actor, call.Action)

// And inside a method, for a permission finer than it:
err = reaches.May(ctx, call.Actor, "/loft.Permission/BookForOthers")

// Marked to run in every process, as standing's follower is:
s.Spin = append(s.Spin, reaches.Follower("loft.roster-reach"))
```

- **`Holders.May` is `Reaches` and `frame.Covers`**, read from `everywhere`:
  what an app with no sites of its own grants on. It is the app asking about
  itself, so the answer is of the app's own roles, and it names no app. `Holders.Reach` hands over
  the whole answer -- for an app that narrows by sites itself, and for a page
  that draws what somebody may do.
- **Kept for `standing.HeldTtl`**, half a minute, which is how long a session
  is held to roster too: a change there lands in an app no later than one
  number says. A roster that could not be asked is not asked again for
  `standing.HeldRetry`, and a call that ends while roster is asked keeps
  nothing, as `standing` learned from Loft. `Holders.Recheck` drops one holder
  at once, for a change the app made itself on the roster in its own process,
  which has no stream.
- **Dropped when roster says so.** roster's stream says when what somebody may
  call may have changed -- a role, a binding or a membership that reaches them
  was written, or a group or a team they are in was erased,
  `SYNC_REASON_REACH` (`proto/app/sync.proto`) -- and the follower
  drops what is kept of anybody it hears of, a suspension or an erasure as
  much as a role. `standing`'s follower passes those events by, so an app
  running both holds two streams, one each.
- **A roster that cannot be asked is a refusal**: `reach.ErrUnreachable`,
  which is `standing`'s own, so an app tells its caller *this is not your
  doing* the same way whichever asked. So is one that does not answer within
  `standing.HeldWait`, five seconds, and an `As` that failed, whose error is
  beside it; `ReachOf`, `Reach` and `May` all say it.
- **What it answers may be handed to the caller as it is.** Each error is
  itself a gRPC status of the package's: `ErrDenied`, and nobody there, is
  `PermissionDenied`; `ErrUnreachable` is `Unavailable`; a call that ended is
  what gRPC says of its context; `ReachOf` of nobody is `NotFound`. Its message
  is the package's sentence and nothing else -- not roster's refusal, not the
  method, not where roster is dialed -- while `err.Error()` says everything for
  the app's log, and `errors.Is` still finds the values and the app's own error
  from `As` (`TestWhatIsAnsweredCanBeHandedToACaller`). An app that wraps the
  error before it returns it has chosen its own words. A method roster does not say they may
  call is `reach.ErrDenied`; so is anybody roster has nobody by, and anybody
  suspended, who reaches nothing.
- **What its role holds**: `reach.Methods`, which is `HolderService/Reaches`
  and `SyncService/Watch`, beside `standing.Methods`.

`reach/reach.go` and `reach/follow.go` are the package, and its tests run both
rosters, as `standing`'s do.

### One request, end to end

Loft in acme, shape C, installed as above with `VouchService/Verify` and
`HolderService/Get` in its `--allow` as well, and its key listing them. erin,
Loft's administrator, has made a role of Loft's, `loft-member`, naming
`/loft.AssetService/Get` and `/loft.Permission/BookForOthers`, bound it to
`loft-users`, a group of Loft's, and put fred in the group:

```
fred   ─ POST /session {login: fred, password} ─▶ loft
loft   ─ Bearer rt_(loft) ─▶ VouchService/Verify {who: {alias: fred}, kind: password, secret}
       ◀─ {ok: true, holder: <fred>, tenant: <acme>}             # the tenant is the key's
loft   ─ HolderService/Reaches {ref: {id: <fred>}}               # at Loft: it names neither app nor roster
       ◀─ {methods: [/loft.AssetService/Get, /loft.Permission/BookForOthers],
           every_site: true, everywhere: [the same]}             # loft-member, through loft-users
loft   ─ its own session for fred ─▶ fred

fred   ─ the session ─▶ /loft.AssetService/Get
loft   ─ standing: Held(fred, when the session began)            # HolderService/Get, kept half a minute
       ─ reach: May(fred, /loft.AssetService/Get)                # the answer above, kept as long
       → one pattern of everywhere covers it → served

erin   ─ GroupMembershipService/Erase {fred's place in loft-users} ─▶ roster
roster ─ SyncService/Watch ─▶ loft: fred, SYNC_REASON_REACH
loft   ─ reach's follower drops fred; at his next call Reaches answers {methods: []}
       → reach.ErrDenied → PermissionDenied
```

Nothing in it is Loft's to work out but what a method means: who fred is, what
he may call there and when that stopped are each one call, answered by roster
as Loft's holder.

### People's tokens

A page that signs somebody in gets an **ID token**: it says to that page who
signed in, and its `aud` is the page's own client. It is not for calling an API.
Sent to two APIs that both accept `aud=<page>`, either one can replay it at the
other as that person -- a log line or a compromised container is enough.

So each API is an **audience**, a name of its own (`urn:example:api:loft`), and a
page asks the issuer for one access token per API it calls, sent only to that
API:

- the issuer issues access tokens as JWTs (`STRATEGIES_ACCESS_TOKEN=jwt`,
  `deploy/hydra.yaml`), signed by the keys it publishes;
- the page's client is allowed the APIs' audiences (`"audience": [...]` in its
  registration), and asks with `audience=` -- silently, once it is signed in,
  since the issuer remembers the session; the Login App grants what was asked;
- the API takes only an access token whose `aud` names it, which an ID token
  (aud = a client) never does.

Nothing else changes: `sub` is the holder, and what they may call is still
`HolderService/Reaches`, never a claim in the token. `docker/flow.sh` walks it
against Hydra.

### An app that cannot ask

What this page asks of an app is that it calls `HolderService/Reaches`. Some
relying parties cannot -- Dependency-Track, kube-apiserver -- and grant on a
list of names in the token instead. For them a client is registered for the
`groups` scope, and its tokens carry the person's groups that a tenant's
administrator showed to that client and nothing else of their membership
([login.md](login.md) § *A relying party that grants on groups*). It is a copy
that lives as long as the token, which is why an app that can ask still does,
and never grants on `groups` or `teams` itself.

### A station that reads badges

An app whose people walk up to something -- a line's terminal, a time clock --
and present a card and a PIN asks roster whether the PIN is the one for that
card. **The station is a holder of the tenant**, or the app its stations belong
to is: bound a role that names `BadgeService/Verify`, and bound it **in a site**
where the stations are a site's, so that a station in Seoul checks the badges of
Seoul's people and a Busan card is a card nobody has to it. It asks with its own
key, and the tenant is its own; the request names none.

```
BadgeService.Verify { code: <what the card says>, pin: <the digits typed> }
  -> { ok: true, holder: <the person>, tenant: <their tenant> }
  -> { }                  # nobody's card, a wrong PIN, somebody stopped, somebody out of reach
  -> { locked: true }     # too many wrong PINs; somebody who administers them unlocks it
```

Write the station against the three answers and nothing else: `ok` is whoever
`holder` is, `locked` is a person to send to whoever administers them, and
anything that is not an answer -- an error, a timeout -- is roster not having
answered, so the station waits. `ok` is never told apart from `{}` by anything
but itself, so a station cannot learn who a card is without its PIN.

What the station holds afterwards is **the app's own**: a presence, the row
the app writes on that answer to say this person is at this station, which
payday's `auth.Represent` reads at each call the station makes for them -- the
station the caller, the person the actor (payday's guide, *Work at a shared
device, or through an agent*). It is not a session of roster's: `Verify` makes
no session, no delegation and nothing to sign out of, and nothing of roster's
asks whether a station still has somebody at it. Who may do what at the app
once they are there is `HolderService/Reaches`, as for anybody.

Each verification that answered somebody is on the access record, under their
tenant, naming the station and the badge. Badges are written by whoever
administers the tenant's people (`BadgeService/Enrol`,
[usage/ways-in.md](usage/ways-in.md) § *A badge and a PIN, at a station*), not by
the app.

## Giving an app's users API tokens

What it is for: a page of the app's own where somebody mints a token for a
script, narrowed to some of what they may do at the app, sees the tokens they
have, and ends one; and a command line of the app's that signs in. The token
is a key **for the app** (`ApiKey.app`): spent at the app, answered there by
`Introspect`, and no credential at roster -- a script that calls Loft holds a
key for Loft, and one that calls roster a key of roster's
([usage/ways-in.md](usage/ways-in.md) § *A key for an app*).

**Minting one is the person's own**, since a key acts as them: it is
`ApiKeyService/Issue`, called as the person, with their own reference and the
app, and held to what they hold **at that app** -- each method one they hold
there, or it is refused. What it may be narrowed to is some of those methods,
with a name and, if it is to stop, an end (`date_expired`). An app's server is
not the person, and is given two roads to the call:

- **Through a delegation the person approved.** The app's page begins a
  terminal's sign-in asking to act for them (`delegate`), naming
  `/roster.ApiKeyService/Issue`; the person approves it at the front door, and
  the app's server polls and keeps the `rd_`, beside its own key and never in a
  browser. From then on, for as long as the delegation lives
  (`delegation.life`, a day at most), it mints as them, for whatever its page
  offers ([usage/ways-in.md](usage/ways-in.md) § *One somebody hands an app*).
  A delegation that lists `ApiKeyService/Issue` is a key that mints keys
  ([usage/wide-grants.md](usage/wide-grants.md) § *A key that mints keys*): each
  key it mints is held to what the person holds at the app, and not to the
  delegation's own list.
- **One key at a time, approved at a terminal's sign-in.** The app's server, or
  its command line, begins one naming the app, the key's name and what it may
  call, the person approves that key at the front door, and the poll answers it
  once to whoever holds the code ([usage/ways-in.md](usage/ways-in.md) § *For an app*).

Nothing is minted without the person's yes at the front door, and the switch
that opens both is the deployment's `account.terminal`.

**The app's own list is the keys that are for it.** `app-administrator` names
`ApiKeyService`'s `Get`, `List` and `Erase`, and `install` binds it to the app
for itself -- in C, its key lists them too -- so Loft, as itself, and Loft's
administrators list the keys that are for Loft, of one person or of everybody,
read one, and end one whoever's it is. A key of roster's, or for another app, is not seen by them and not ended,
refused naming Loft; their own keys are theirs as ever. So Loft's page asks
roster as Loft, for the person its own session says is there, with a filter
naming them, and holds that page to its own session, since roster answers Loft
every key for Loft whoever asks it to. A browser cannot ask roster itself:
roster takes no token of the issuer's, and its session's cookie is its own
host's. Ending one is on the trail as Loft's own write. A tenant installed
before `ApiKeyService`'s verbs were among these holds an `app-administrator`
naming fewer, which is used as it stands until somebody widens it.

**Checking one it is handed** is two questions: whose key it is, and what they
may call. `TokenService/Introspect`, asked as the app, answers a key for the
app with its holder, its tenant and its methods, and anybody else -- another
app, the tenant's administrator -- with `NotFound: no such token`; then
`Reaches` says what the holder may call at the app, and the call is served
where both cover it: the key's methods are the app's to hold it to. A key
ended is none at the next ask. `/payday.TokenService/Introspect` is payday's
and not under `/roster.*/*`, so an app that checks keys names it in `--allow`.

The whole of it, as Loft:

```
loft   ─ POST /device/begin {app: loft, delegate: true, allow: [/roster.ApiKeyService/Issue]} ─▶ account app   # once
erin   ─ signed in at the account app, types the code ─▶ "loft will act as you at roster", until when ─▶ yes
loft   ─ POST /device/poll {device_code} ─▶ account app
       ◀─ {token: rd_…}                                         # kept on Loft's server, beside erin's session

loft   ─ Bearer <Loft's key> + roster-as: rd_… ─▶ ApiKeyService/Issue
           {holder: {id: <erin>}, app: {id: <Loft's holder>}, alias: ci, methods: [/loft.AssetService/Get]}
       ◀─ {token: rt_…, key}                                    # shown to erin once
loft   ─ as itself ─▶ ApiKeyService/List {filters: [{holder: {id: <erin>}}]}    # her keys for Loft
loft   ─ as itself ─▶ ApiKeyService/Erase {id: <the key>}                       # and ending one

ci     ─ Bearer rt_… ─▶ loft, at /loft.AssetService/Get
loft   ─ as itself ─▶ TokenService/Introspect {token: rt_…}
       ◀─ {id: <erin>, tenant_id: <acme>, grant: {actions: [/loft.AssetService/Get]}}
loft   ─ Reaches(erin) covers it, and so does the key → served
```

**A command line of the app's** signs in one of two ways. A terminal's sign-in
naming the app is the second road above, and what it holds afterwards is an
`rt_` for the app, checked as any. Or it sends somebody's browser through the
issuer and back to a loopback address, and holds the issuer's access token for
the app's audience (§ *People's tokens*); a loopback redirect says nothing of
the tenant, so the client registers its loopback address beside a redirect at
the tenant's own host ([login.md](login.md) § *What changes when Hydra is in
front*).

## Reading an app's users and what each may do

**An app's users are the people in its groups and the people bound its
roles.** A group says whose it is (`Group.app`, the holder the app is answered
as; none is the tenant's own), fixed when it is made, and the two are kept to
each other where a binding is written: a role of Loft's is bound to a person or
to a group of Loft's, and a group of Loft's takes Loft's roles and no other
([usage/permissions.md](usage/permissions.md) § *A group*). So Loft's list of
who uses it is the members of its groups beside the people bound its roles,
and `Reaches` at Loft answers each of them what the group's roles give.

**The reads are the lists of those rows**, which the app holds for itself
through `app-administrator` and which answer it its own rows: its roles
(`RoleService/List`), its groups (`GroupService/List`), the places in a group
(`GroupMembershipService/List`, filtered by the group) and the bindings of a
role (`BindingService/List`, filtered by the role). `RoleService/List`,
`GroupService/List`, `BindingService/List` and `ApiKeyService/List` take a
filter by `app`, the rows whose `app` is that holder, so an app whose role of
roster's names one of those lists across the tenant still asks for its own;
filtered by another app, an app's administrator is answered nothing. A binding
of an app's role says no `app` -- its role does -- so the bindings filtered by
`app` are the ones **for** the app, of `app-administrator`, and the people
bound its roles are the bindings of each of its roles. Every list takes a
`select`, as a `Get` does, and brings along what a row names -- the person on a
membership, their alias and labels -- where a row it did not select is its
reference alone. As Loft, over the wire:

```sh
roster role list '{"filters": [{"app": {"slug": {"alias": "loft", "tenant": {"alias": "acme"}}}}]}'
roster group list '{"filters": [{"app": {"slug": {"alias": "loft", "tenant": {"alias": "acme"}}}}]}'
roster group-membership list -o json '{"filters": [{"group": {"slug": {"alias": "loft-users", "tenant": {"alias": "acme"}}}}],
                                       "select": {"holder": {"alias": true, "labels": true}}}'
roster binding list -o json '{"filters": [{"role": {"slug": {"alias": "loft-member", "tenant": {"alias": "acme"}}}}],
                              "select": {"holder": {"alias": true}, "group": {"alias": true}}}'
```

**Not every holder in those lists is a person.** The app's own holder is bound
its `NAME-everything`, and is the manager of every group it made, so a member
of it: whoever makes a group manages it. A holder labelled `roster.app` --
an installed app, this one among them -- or `roster.declared` -- a front door of
roster's -- is no person, and a directory or a list of people built on these
reads leaves it out (`rstr.Holder.NoPerson`), selecting the holder's `labels`
to tell:

```json
{"items": [{"id": "…", "holder": {"id": "…", "alias": "loft", "labels": {"roster.app": "loft"}}},
           {"id": "…", "holder": {"id": "…", "alias": "fred"}}]}
```

**What each may do** is `Reaches` of them, at the app (§ *A person calling the
app*), and **when that changes** is the stream: `SyncService/Watch` says
`SYNC_REASON_REACH` of anybody a role, a binding or a membership reaching them
was written for, or a group or a team they are in erased, and `standing`'s
events say a person suspended, signed out everywhere or erased and a tenant
closed ([relying-app.md](relying-app.md) § *Heard: the stream*). `reach`'s
follower and `standing`'s are the two that hear them.

Taking somebody out of Loft is ending their place in its group, or their
binding: there is no exclusion and no denial, and nothing for a reader to
subtract. *Everybody of the company may use Loft* is then not one binding to
the directory's `employees`, which somebody else keeps -- a person in it would
hold Loft's role for as long as they were in it. Somebody puts them in Loft's
group: an administrator, or the app at a first sign-in by a rule of its own,
and an app that enrols people itself is the one that remembers whom it took
out.

### An app's groups

Whoever makes a group manages it, whichever app's it is, and a group's
managers are as they are for every group: a finer hand than the app's whole.
What ends the app's holder ends its groups, in the act that ends its roles,
the bindings for it (§ *An app's administrators*) and the keys for it,
whoever's they are, and a restore of the holder brings back none of the four.

### An app's administrators

**An app's administrator holds roster's verbs for that one app**, and what they
hand out at the app. The first is `app-administrator`, a role of roster's bound
**for the app** (`Binding.app`): its roles, the bindings of them, its groups
and their memberships, and nothing else of the tenant's
([usage/permissions.md](usage/permissions.md) § *Held for one app*). The second
is a role of the app's naming its methods -- `loft-everything` -- since binding
one of the app's roles hands out what it names at the app, and nobody hands out
what they do not hold there.

With both, Loft's administrator makes Loft's roles and groups, binds the one to
the other and to people, puts people in Loft's groups and takes them out, and
lists all of it -- and none of another app's or the tenant's, which they are
refused writing and do not see. They name the people they bind or put in by
alias or identifier, as a group's manager names the people they put in: the
write reads the person through the wall as the write it is, and a grant of no
site reaches the whole tenant. What neither of them holds is a read of the
tenant's people (`HolderService/List`, `/Search`), so they name somebody they
were told of.

The first are whoever `roster app install --administer` names, with nothing
else bound to them; the app's own holder is bound `app-administrator` for
itself, so the app reads and writes what is its own as itself.

They hand both on as they hold them: `app-administrator` for Loft, and Loft's
role, to a second person, who can then do the same. What is held for Loft is
handed out for Loft and no wider, so they make nobody an administrator across
the tenant or of another app. The gate counts the binding for those verbs and
no other: whatever `app-administrator` names, it calls nothing else of
roster's for them. And it is roster's, so a helpdesk that holds no such verb
does not reset their password.

`Reaches` at Loft is answered to them, with nothing else bound to them:
`app-administrator` names `HolderService/Reaches`, and held for Loft alone it
is answered at Loft and refused at roster and at every other app
([§ A person calling the app](#a-person-calling-the-app)). What they are told
of themselves at roster says the verbs among `methods`, names Loft in `apps`,
and holds none of them in `everywhere`.

**Where they do it is the command line, over the wire, as themselves** -- with a
key of roster's in `client.auth`, or one `roster sign-in` got them
([usage/cli.md](usage/cli.md)) -- since roster's consoles have no page of
an app's administrator's yet: their *roles* and *groups* tabs make the tenant's
own, and send no `app`, which a role or a group made by somebody who holds
`Add` for one app alone is refused without. Binding a role is the same form for
everybody, and an app's role says whose it is. As erin, Loft's administrator:

```sh
roster role add @acme/loft-auditor '{"app": {"slug": {"alias": "loft", "tenant": {"alias": "acme"}}}, "methods": ["/loft.AuditService/List"]}'
echo '{"role":  {"slug": {"alias": "loft-auditor", "tenant": {"alias": "acme"}}},
       "holder": {"slug": {"alias": "fred",         "tenant": {"alias": "acme"}}}}' \
  | roster binding add -
roster holder reaches @acme/fred '{"app": {"slug": {"alias": "loft", "tenant": {"alias": "acme"}}}}'
```

and a role of the tenant's own is refused her, naming the app:

```
roster: rpc error: code = PermissionDenied desc = app: you hold /roster.RoleService/Add for @acme/loft, and this role is not @acme/loft's
```

## An app calling another app

Loft calling levyan about acme has to show levyan it is `@acme/loft`, and its
own credential cannot: an `rk_` does not introspect on the data plane, and
`roster-at` is a header only roster reads. So it exchanges one (#74):

```
loft   ─ rk_ + roster-at: @acme ─▶ DelegationService/Exchange{app: @acme/levyan, methods}
       ◀─ rd_…   (about @acme/loft, issued to @acme/levyan, fifteen minutes)
loft   ─ Bearer rd_… ─▶ levyan
levyan ─ rk_ + roster-at: @acme ─▶ TokenService/Introspect(rd_…)
       ◀─ @acme/loft, acme, the methods
levyan ─ HolderService/Reaches(@acme/loft) → covers → serves it, or not
```

- **It is a delegation turned round.** An ordinary one is about somebody else and
  issued to the caller; this is about the caller and issued to the app it names.
  Only that app is told who it names -- the sender, and every other caller, get the
  `NotFound` a string that was never a token gets.
- **Checked by roster.** It is opaque and the receiver asks, because roster
  issues nothing a third party verifies on its own (`CLAUDE.md`, *the other
  rule*).
- **One tenant.** The app is the caller's own tenant's holder; the receiver
  introspects narrowed to that tenant too, which is how the issuer binding
  matches.
- **`methods` are the receiver's.** What the token is for at levyan --
  `/levyan.InvoiceService/Get` -- which roster neither knows nor checks. levyan
  narrows by them as it would by a key's, and still decides with `Reaches`.
- **What each side's role needs.** The sender `/roster.DelegationService/Exchange`;
  the receiver `/payday.TokenService/Introspect`. Both go in the `--allow` an app is
  installed with.

### As a person

Acting **as a person** at another app -- Loft calling levyan on erin's behalf
-- is not the exchange above with a person put into it (#83). Loft signs
people in through the issuer, so what it holds of erin is the issuer's tokens
and no `rd_`: a delegation is what a login app holds, and a product never does
([relying-party.md](relying-party.md)). The proof of the person is the one the
issuer already makes for this -- an access token for the API it is spent at,
*People's tokens* above -- and the exchange goes beside it as the proof of the
app:

```
loft   ─ authorize, audience=urn:example:api:levyan ─▶ issuer      (erin's browser, signed in)
       ◀─ JWT {sub: erin, aud: levyan, client_id: loft}
loft   ─ rk_ + roster-at: @acme ─▶ DelegationService/Exchange{app: @acme/levyan, methods}
       ◀─ rd_…   (about @acme/loft, issued to @acme/levyan)
loft   ─ the JWT and the rd_ ─▶ levyan, at a method meant for an app acting for somebody
levyan ─ the issuer's keys → aud, client_id;  Introspect(rd_) → @acme/loft
levyan ─ Reaches(erin), Reaches(@acme/loft) → both cover → serves it, or not
```

- **Two proofs, and the receiver binds them.** The token's `client_id` has to
  be the client of the app the `rd_` names, and erin has to be in that app's
  tenant. A product is one client in every tenant (#36), so the receiver maps
  a client to an app once, in its own configuration -- roster holds no OAuth
  clients. Either proof alone is nothing: a token that leaked, without the
  app's key; the app's key, without somebody who signed in.
- **The app's ceiling is its holder's bindings.** What Loft may do for people
  at levyan is a permission method levyan declares --
  `levyan.AdminService/ActForPeople`, say -- in a role of levyan's, which a
  tenant administrator who holds it at levyan binds to `@acme/loft`, and levyan
  reads with `Reaches` on that holder. What
  is served is what both cover: never wider than erin, never wider than the
  app, and never a claim in either token.
- **The token is erin's whole credential at that API** -- the one erin's own
  page would present. So the receiver takes one minted for another app's client
  only at the methods meant for it, or Loft holds everything erin may do at
  levyan.
- **Minutes, not an hour.** Hydra v26.2.0 makes an access token live an hour
  unless the client says otherwise, and a refresh token makes another without
  erin. The delegating client gets a short lifespan of its own
  (`authorization_code_grant_access_token_lifespan`) and no refresh token, or
  *erin is at Loft* means *erin was, today*.
- **What ends it.** `Reaches` is live, so a suspension or a removed binding
  stops the next call, within whatever the receiver caches. Signing out
  everywhere voids roster's credentials and, through the Login App, the
  issuer's session and every client's grant -- and not a JWT already issued,
  which verifies until it expires. A receiver that must stop sooner follows `SyncService/Watch` and
  refuses a token issued before `date_invalidated`, the way `login/sync.go`
  ends sessions -- `standing` is that follower as a package
  ([relying-app.md](relying-app.md)); one holding something that lasts -- a stream, a media session
  -- ends that there too. roster down is a refusal: the JWT verifies, and
  `Reaches` does not answer.
- **A page left open renews with the receiver's session, not a refresh
  token.** The two proofs start it, once. After that the receiver keeps a
  session of its own for *this app, acting for this person* -- bound to the
  tenant, the app's holder and the person, held by the app's server and never
  its browser, with an end that renewing does not move. Each renewal brings a
  fresh `rd_`, so an app whose nomination ended is refused at the next one,
  and asks `Reaches` about both again. Signing out everywhere changes neither
  answer, so the receiver follows `SyncService/Watch` from the start and ends
  the session there; the app ending its own session ends this one too. A
  refresh token instead would mint erin's whole credential at levyan, with
  nobody present, for as long as it lived.

**What it is not.** A delegation about erin, minted from Loft's own
(`roster-as`) and issued to levyan: that is the shape for a **login app**, which
does hold an `rd_`, and it is not built. `Exchange` refuses a caller acting
through a delegation, because a token minted from one outlives whatever revokes
the first; lifting that takes an expiry capped by the first, a revocation that
reaches both, and an actor `Introspect` can name -- which payday's response has
no field for. Nor a token roster signs with an `act` claim in it: roster issues
nothing a third party verifies on its own. Hydra v26.2.0 offers no token
exchange (`grant_types_supported`), and one that did would name the same
client the access token's `client_id` already does.

### Writing for its administrator, at roster

What Loft's page **writes at roster** because somebody pressed a button --
erin, Loft's administrator, putting fred into `loft-users` -- Loft writes **as
erin**, through a delegation she approved at roster, and not as itself. So it is
held to what erin holds there, as if she had made the call, and the trail names
her as who did it, Loft as the caller, and the delegation. What Loft does for itself -- its setup, the roles it ships, a key ended
from its page -- it still does as itself.

```
loft   ─ POST /device/begin {app: loft, delegate: true, allow: [/roster.GroupMembershipService/Add]} ─▶ account app
       ◀─ device_code, user_code, verification_uri          (Loft's page sends erin there)
erin   ─ signed in at the account app, types the code ─▶ "loft will act as you at roster", the methods
erin   ─ yes ─▶ account app ─ DelegationService/Issue{holder: erin, app: @acme/loft, methods}, as erin ─▶ roster
loft   ─ POST /device/poll {device_code} ─▶ account app
       ◀─ {token: rd_…}  (once; erin's, issued to @acme/loft, a day at most: `delegation.life`)
loft   ─ Bearer <Loft's own key> + roster-as: rd_… ─▶ GroupMembershipService/Add{loft-users, fred}
       ◀─ answered as erin, held to what erin holds for Loft
trail  ─ actor erin, caller @acme/loft, presence roster-as:<the delegation>
```

- **One approval, for as long as it lives.** Loft keeps the token on its server,
  never in the browser, beside erin's session at Loft, and asks again when it
  runs out. Loft ends it with the token (`DelegationService/Revoke`, the
  app's); erin ends it from her own list (`DelegationService/Erase`, which
  `self-service` names); signing out everywhere, a suspension and an erasure
  end it at the next call.
- **It adds nothing.** It names roster's methods, each one erin may call at all;
  what is served is what she may do, never wider than her and never Loft's
  own reach.
- **Only Loft can spend it**: beside Loft's own key. Another app presenting it,
  or the token alone, is refused, and `Introspect` answers it to Loft alone.
- **The switch is `account.terminal`**, which opens a terminal's sign-in and
  this; anybody may begin one, and nothing is minted without erin's yes
  ([usage/ways-in.md](usage/ways-in.md) § *For an app* and § *One somebody hands
  an app*).

## One client for all three

An app's business logic wants to say *as this tenant, call roster* or *as this
tenant, call levyan*. Which credential and which header that takes is the shape's,
and can be decided from the key and from roster rather than from configuration:

```go
// What business logic sees.
type Tenancy interface {
	// The tenants this instance serves.
	Serves(ctx context.Context) ([]pdid.Id, error)
	// A context whose calls to roster are answered as this app's holder in tenant.
	Roster(ctx context.Context, tenant pdid.Id) (context.Context, error)
	// A token naming this app's holder in tenant, for calling app.
	Token(ctx context.Context, tenant pdid.Id, app string) (string, error)
}

var ErrNotServed = errors.New("this instance does not serve that tenant")
```

- **The prefix says how to authenticate.** `rt_` is C: one tenant, read from
  `MeService.Get`, and `Roster()` is the bearer alone. `rk_` is A or B: the
  bearer and `roster-at`.
- **roster says which tenants.** For an `rk_`, `NominationService/List` asked as
  the key answers its own nominations and nobody else's -- roster holds a key to
  its own `borrower_id` (`server/core/nomination.go`) -- and `roster-at: @<tenant>`
  names any of them with no `Host` to look up. One tenant is B, several is A --
  the same binary, a different key. A key cannot **watch** them (a watch names
  rows by reference); it lists again, which the account app does when a name
  arrives for a tenant it has not read. The account app and `ldap serve` are this
  layer, written twice.
- **`Token()`** is `DelegationService/Exchange`, asked narrowed to the tenant,
  for the app's holder there; in C, the same call with the `rt_`.

What this layer **cannot** hide, and should not try to:

- **Whether another tenant can be reached at all.** A can, B and C cannot, and
  that is a fact about the product, not a detail of authentication. The layer
  answers `ErrNotServed`; the business logic decides what a feature that crosses
  tenants does in a shape that has none.
- **Setting a tenant up.** A needs `roster app install` per tenant; C needs a
  key the tenant mints. That is operations, written down beside the shape, not
  code.
- **The database.** B on a shared database is confined in roster and nowhere
  else unless the app's database login is.

## What an app keeps here

An app asks roster who somebody is, and it may also keep one thing of its own
here: **what they agreed to** of its documents -- its terms of use, its privacy
policy, a newsletter somebody may decline. `ConsentService` is a ledger of it,
for every app that asks (`proto/app/consent.proto`).

Which documents are in force, at which version, and which may be declined stays
the app's configuration; roster stores what each account did about them. An
app records with `Agree` -- every document it asks for at once, each at the
version that was read, which writes nothing for one already agreed to -- and
`Withdraw` for an optional one, and before it serves somebody it asks `Settled`
which of its documents they owe. Each act is a row and none is changed, so the
agreement, its withdrawal and an agreement again are three rows, and the one in
force is the latest.

**An app's consents are its own.** The app is the holder the call is made as --
its key's, or the one a deployment key is answered as in the tenant -- written
onto every act and compared on every read: `List` and `Get` answer nothing of
another app's, and `Settled` counts nothing of theirs. A **scope** is the app's
word for where a consent holds, empty for the whole deployment: an app keeps its
deployment's terms at the empty scope, and a workspace's own policy later is the
same rows with the workspace as their scope.

The role an app is installed with names the methods it uses. One that records,
asks and lists names three; one that lets somebody decline an optional document
adds `Withdraw`, and one that reads a single act adds `Get`:

```sh
roster --as admin app install --tenant acme \
  --allow '/roster.ConsentService/Agree,/roster.ConsentService/Settled,/roster.ConsentService/List' \
  loft
```

Nothing writes a consent as a row: the generated writes are closed on the wire,
and the app, the act and its place in the ledger are stamped. Once the account
is erased its consents stay as proof, and the deployment destroys them
`consent.keep` later ([operating.md](operating.md) § *Destroying somebody*) --
no app sweeps a ledger it shares.

## Where each fact is

| | |
| --- | --- |
| `proto/app/nomination.proto` | the nomination, and why it left `Host` |
| `server/keys/at.go` | narrowing a deployment key, and what it does not narrow |
| `server/core/nomination.go` | the two questions a nomination is asked |
| `cli/control.go`, `narrowedFlag` | a deployment key with nothing on it |
| `cli/login.go`, `nominate` | the Login App setting itself up in every tenant |
| `cli/app.go` | `roster app install` / `uninstall`: every row it writes, and what it refuses first |
| `server/core/forapp.go` | `app-administrator` and `ForAnApp`: roster's verbs held for one app |
| `server/core/holder.go`, `reachedAt` | which app a `Reaches` is answered at, and to whom |
| `cmd/policy.go`, `HeldAt` | what is read to answer it: the bindings of the app's roles that reach somebody |
| `reach/reach.go`, `reach/follow.go` | what somebody may call, asked, kept and dropped: `Holders.May` |
| `server/sync/reach.go` | who a write to a role, a binding or a membership reaches, for the stream |
| `proto/ext/app/role_svc.ext.proto`, `server/core/widen.go` | `Role.Widen`: what an app adds to the roles it ships, and the rule it is held to |
| `server/core/narrow.go` | `Role.Narrow`: what a tenant takes out of a role, what it may take, and the administrator it may not |
| `server/core/handsover.go` | `Role.HandsOver`: which of a list of methods hand over more than they read as, for an app's own page that binds a role |
| `server/core/apikey.go`, `server/keys/service.go` | a key for an app: minted as the person, listed and ended by the app, answered to it alone by `Introspect` |
| `proto/app/consent.proto`, `server/core/consent.go` | an app's ledger of consent, and why every act is a row |
| `account/account.go`, `ldap/ldap.go` | two consumers in the A shape |
| `ts/lib/tenant/apps.tsx` | the *apps* tab, in both consoles |
| `docker/flow.sh` | an access token for one API, naming the client that asked for it |
| [login.md](login.md) § *What a front door needs* | the same arrangement from the Login App's side |
| [operating.md](operating.md) | `roster login provision`, and when a tenant's first name is nominated in |
