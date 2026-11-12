# An app beside roster, in three shapes

A product -- Loft, levyan, anything that keeps its own rows and asks roster who
somebody is -- runs in one of three shapes, and the shape decides which key it
holds, how it sends a request, whose name ends up in a tenant's trail, and
which tenants it may reach. This page is that decision, and what an app's own
code can and cannot hide about it.

It is about an app as a **caller of roster** and as a **caller of other apps**.
How a person's browser signs in to it is [relying-party.md](relying-party.md);
the Login App, which is the first app that took the roster-hosted shape, is
[login.md](login.md). The words are [glossary.md](glossary.md)'s: a **roster
operator** runs the deployment and is a holder of the control plane; a
**tenant administrator** is a roster user whose bindings administer their tenant.

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
across all of them, as the key. The Login App has three (`WhoseHost`,
`TenantService/Get`, `SyncService/Watch`), because it has to work out whose
flow a challenge is before it can narrow. A product that always knows which
tenant a request is about has none, and is minted with nothing on it:

```sh
roster control key add --narrowed loft
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

Setting a tenant up for an A app is a holder, its bindings and a nomination in
that tenant -- and a `Host` for its name, if it is reached by one. A roster
operator does it, once per tenant (#75):

```sh
roster app install --tenant acme \
  --role '/loft.AssetService/*' \
  --administer '/loft.*/*' \
  loft
```

It is the roster operator's because of the last flag. A tenant's first
administrator is bound `/roster.*/*`, which covers none of an app's methods, and
nobody hands out what they do not hold -- so nobody in the tenant could give the
app's holder its role, or themselves the right to manage it. `--administer` adds
the app's methods to that administrator's role; from then on the holder's
bindings, and the nomination, are the tenant's. Installing again leaves a role
the tenant reshaped as they have it, and puts back a nomination or binding that
is gone -- it is how an operator puts an app back after a tenant ended it, which
the tenant usually cannot do itself: nominating the app's holder needs the
app's methods. `roster app uninstall --tenant acme loft` ends the nomination,
and the tenant administrator can do the same from the user console's *apps*
tab. The Login App and the account app do this for themselves at start, in
every tenant with a name -- so for them ending it lasts until the next start.

**Roster's own front doors are declared.** The Login App and the account app
write their rows in every tenant at start -- the holder, its role, the binding
and the nomination -- because the deployment's configuration turned them on, so
those rows carry `roster.declared` and are read-only to everybody at a port: no
tenant ends them, narrows them or disables them (`server/core/declared.go`).
Turning one off is taking it out of the configuration, then erasing what is left
from the box ([operating.md](operating.md), *Declared rows*). An app installed
with `app install` is not declared: it is the operator's act, and the tenant's
from then on.

**A holder of the app's name that is already there is not taken silently.** It
is somebody's -- a person, an earlier app holder with a key and a role -- and
the app's key would be answered with everything it holds, while whoever signs
in as it would hold the app's methods. `install` refuses it unless it holds
nothing but the app's own role, and `--adopt` says to use it anyway, printing
what it held. The start-up provisioning of roster's own apps skips such a
tenant and says why, rather than failing for every tenant.

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
for them (`roster key add --tenant … --holder …`). It resolves to that holder,
in that tenant, with that holder's bindings narrowed further by the key's own
`methods`. No `roster-at`: there is nothing to narrow, and a request carrying
one with an `rt_` is refused.

The tenant sees the holder in their own directory and can revoke the key, which
is the property C is chosen for.

**C, held several times.** Somebody who is not a roster operator -- a third party
integrating with several of your customers -- cannot hold an `rk_`: the control
plane is the roster operator's. It holds one `rt_` per customer that minted one,
each answering in that customer's tenant alone. That is not a fourth shape, just
C for each tenant, and it needs nothing from roster that C does not.

The account app and `ldap serve` take `--key alias=rt_…` for exactly this case,
a tenant running its own copy. Run by a roster operator for everybody they are A,
each with one deployment key (#76).

## A person calling the app

Whatever the shape, a request from a person arrives with one of:

| credential | how the app checks it |
| --- | --- |
| an access token from the issuer, `aud` = this app | the issuer's keys (`/.well-known/jwks.json`, JWT access tokens); `aud` must name **this** API; `sub` is a `Holder.id`. See *People's tokens* below |
| an `rt_` of the person's (a script, an app password) | `payday.TokenService/Introspect`; answers the holder, tenant and the key's methods |
| an `rd_` this app was given | `Introspect`, which answers only the app it was issued to |

and then asks roster what that person may call, with `HolderService/Reaches`,
and decides with `frame.Covers` against the method being called -- reading
`everywhere` if the app has no sites of its own. `methods` is the gate's union,
which roster's wall then narrows for a grant bound at one site or held in a
team; an app with no such wall would hand that grant everywhere. `everywhere`
is the part that needs no narrowing. And a suspended holder reaches nothing:
their credentials are refused before any of this, and an SSO token minted
before the suspension is answered with nothing to cover. The app's own
permissions are its own RPCs, and anything finer than one RPC is a **method of a
permission service** the app declares in its own proto package -- for example
`loft.AdminService/SeeEveryTenant` -- named in a role like any other
method. A role is a bundle of those; an app asks about the permission and never
about the role's name, so a role can be split or renamed without the app
noticing. Nor about which group or team somebody is in: a membership nothing is
bound to is guarded by nothing ([usage/permissions.md](usage/permissions.md) §
*Membership is not a permission*).

**Somebody has to hold an app's methods before they can grant them.** A tenant's
first administrator is bound `/roster.*/*`, which covers none of an app's own
methods, and nobody hands out what they do not hold. So a roster operator, on the
admin listener, starts it: binds a tenant administrator the app's methods -- or a
pattern over every app sold to that tenant -- and from there it is the tenant's to
hand on.

Every shape asks the same questions in the same order, so this half of an app is
written once, here: `reach`, below.

### Asking it: `reach`

The paragraphs above, as a package an app imports beside `standing`
([relying-app.md](relying-app.md)) and wires the same way -- the same
connection, and the same `As` saying who the app is in the tenant:

```go
reaches, err := reach.New(reach.Config{Conn: conn, As: as})

// In the app's gate -- payday's gate.Policy.May -- for every call:
err = reaches.May(ctx, call.Actor, call.Action)

// And inside a method, for a permission finer than it:
err = reaches.May(ctx, call.Actor, "/loft.Permission/BookForOthers")

// In every process, as standing's follower is:
s.Spin = append(s.Spin, spin.InEveryProcess(reaches.Follower("loft.roster-reach")))
```

- **`Holders.May` is `Reaches` and `frame.Covers`**, read from `everywhere`:
  what an app with no sites of its own grants on. `Holders.Reach` hands over
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
  was written, `SYNC_REASON_REACH` (`proto/app/sync.proto`) -- and the follower
  drops what is kept of anybody it hears of, a suspension or an erasure as
  much as a role. `standing`'s follower passes those events by, so an app
  running both holds two streams, one each.
- **A roster that cannot be asked is a refusal**: `reach.ErrUnreachable`,
  which is `standing`'s own, so an app tells its caller *this is not your
  doing* the same way whichever asked. A method roster does not say they may
  call is `reach.ErrDenied`; so is anybody roster has nobody by, and anybody
  suspended, who reaches nothing.
- **What its role holds**: `reach.Methods`, which is `HolderService/Reaches`
  and `SyncService/Watch`, beside `standing.Methods`.

`reach/reach.go` and `reach/follow.go` are the package, and its tests run both
rosters, as `standing`'s do.

### The roles an app ships

An app that comes with roles -- Loft's owner, administrator, manager, member
and auditor -- writes them into the tenant itself, as the holder it is there,
and from then on they are the tenant's: bound to people in the user console,
given more methods or fewer there (`Role.Widen`, `Role.Narrow`), replaced or
erased. Nothing here is a privilege only an app has; it is three ordinary facts
lined up.

1. **The app holds what it writes.** A role is a grant when it is written, and
   nobody hands out what they do not hold (`server/core/escalate.go`). So the
   role an app is installed with names its own package beside what it asks of
   roster, and the role writes:

   ```sh
   roster app install --tenant acme \
     --role '/roster.HolderService/Reaches,/roster.SyncService/Watch,…,/roster.RoleService/Add,/roster.RoleService/Get,/roster.RoleService/Widen,/loft.*/*' \
     --administer '/loft.*/*' \
     loft
   ```

   A pattern, and not today's list of its methods: the app widens its roles by
   methods it has not written yet, and holding the pattern is what lets it.
   It gives nobody anything at the app, which asks roster about whoever calls
   it and is never called as itself.
2. **The tenant's administrators hold them too** (`--administer`), or nobody in
   the tenant could bind the roles the app writes: binding one is a grant, and
   the first administrator holds `/roster.*/*` alone.
3. **The app writes with `Role.Add`, and widens with `Role.Widen`.** At its
   own setup -- `loft init` -- each role it ships is added under its alias, and
   one already there is the tenant's and left as it is. At an upgrade that
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
role the tenant made wider than the app -- a method of another app's in it -- is
refused a widen, as a patch of it would be, and is the tenant administrator's
to change, who holds what is in it.

Somebody has to be bound to the first of them. The tenant's administrator does
it in the user console; an app's setup that names its first owner does it
itself, with `BindingService/Add` in its role -- a grant like any other, and
one the app holds. An app running roster in its own process writes the same
rows through that roster's own door, and calls `Holders.Recheck` after.

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

## An app calling another app

Loft calling levyan about acme has to show levyan it is `@acme/loft`, and its
own credential cannot: an `rk_` does not introspect on the data plane, and
`roster-at` is a header only roster reads. So it exchanges one (#74):

```
loft   ─ rk_ + roster-at: @acme ─▶ DelegationService/Exchange{audience: @acme/levyan, methods}
       ◀─ rd_…   (about @acme/loft, issued to @acme/levyan, fifteen minutes)
loft   ─ Bearer rd_… ─▶ levyan
levyan ─ rk_ + roster-at: @acme ─▶ TokenService/Introspect(rd_…)
       ◀─ @acme/loft, acme, the methods
levyan ─ HolderService/Reaches(@acme/loft) → covers → serves it, or not
```

- **It is a delegation turned round.** An ordinary one is about somebody else and
  issued to the caller; this is about the caller and issued to the audience. Only
  the audience is told who it names -- the sender, and every other caller, get the
  `NotFound` a string that was never a token gets.
- **Checked by roster.** It is opaque and the receiver asks, because roster
  issues nothing a third party verifies on its own (`CLAUDE.md`, *the other
  rule*).
- **One tenant.** The audience is the caller's own tenant's holder; the receiver
  introspects narrowed to that tenant too, which is how the issuer binding
  matches.
- **`methods` are the receiver's.** What the token is for at levyan --
  `/levyan.InvoiceService/Get` -- which roster neither knows nor checks. levyan
  narrows by them as it would by a key's, and still decides with `Reaches`.
- **What each side's role needs.** The sender `/roster.DelegationService/Exchange`;
  the receiver `/payday.TokenService/Introspect`. Both go in the `--role` an app is
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
loft   ─ rk_ + roster-at: @acme ─▶ DelegationService/Exchange{audience: @acme/levyan, methods}
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
  `levyan.AdminService/ActForPeople`, say -- which a tenant administrator
  binds to `@acme/loft`, and levyan reads with `Reaches` on that holder. What
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
	// A token naming this app's holder in tenant, for calling audience.
	Token(ctx context.Context, tenant pdid.Id, audience string) (string, error)
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
  for the audience's holder there; in C, the same call with the `rt_`.

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
word for where a consent holds, empty for the whole deployment: Loft keeps its
deployment's terms at the empty scope, and a workspace's own policy later is the
same rows with the workspace as their scope.

The role an app is installed with names the methods it uses. Loft records,
asks and lists; an app that lets somebody decline an optional document adds
`Withdraw`, and one that reads a single act adds `Get`:

```sh
roster app install --tenant acme \
  --role '/roster.ConsentService/Agree,/roster.ConsentService/Settled,/roster.ConsentService/List' \
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
| `cli/app.go` | `roster app install` / `uninstall` |
| `reach/reach.go`, `reach/follow.go` | what somebody may call, asked, kept and dropped: `Holders.May` |
| `server/sync/reach.go` | who a write to a role, a binding or a membership reaches, for the stream |
| `proto/ext/app/role_svc.ext.proto`, `server/core/widen.go` | `Role.Widen`: what an app adds to the roles it ships, and the rule it is held to |
| `server/core/narrow.go` | `Role.Narrow`: what a tenant takes out of a role, what it may take, and the administrator it may not |
| `proto/app/consent.proto`, `server/core/consent.go` | an app's ledger of consent, and why every act is a row |
| `account/account.go`, `ldap/ldap.go` | two consumers in the A shape |
| `ts/lib/tenant/apps.tsx` | the *apps* tab, in both consoles |
| `docker/flow.sh` | an access token for one API, naming the client that asked for it |
| [login.md](login.md) § *What a front door needs* | the same arrangement from the Login App's side |
| [operating.md](operating.md) | `roster login provision`, and when a tenant's first name is nominated in |
