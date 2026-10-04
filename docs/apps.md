# An app beside roster, in three shapes

A product -- kamino, khala, anything that keeps its own rows and asks roster who
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
control plane   @owner/kamino ── rk_ (one key; every live key here borrows)

tenant acme     Host  kamino.acme.example         names say which tenant
                @acme/kamino                      who the app is here
                Nomination  borrower=@owner/kamino → @acme/kamino
                Binding     role kamino-app → @acme/kamino

tenant beta     Host  kamino.beta.example
                @beta/kamino
                Nomination  borrower=@owner/kamino → @beta/kamino
                Binding     …
```

A request says which tenant it is about -- by a name the tenant answers at, or
by the tenant itself when the app has no name to give (a directory has a DN, a
job its own configuration):

```
authorization: Bearer rk_…
roster-at: kamino.acme.example        or        roster-at: @acme
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
roster control key add --narrowed kamino
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
  --role '/hday.kamino.RobotService/*' \
  --administer '/hday.kamino.*/*' \
  kamino
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
app's methods. `roster app uninstall --tenant acme kamino` ends the nomination,
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

**Give each instance its own control-plane holder** (`@owner/kamino-acme`,
`@owner/kamino-beta`). The nomination is found by the key's holder, so instances
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
`hday.kamino.AdminService/SeeEveryTenant` -- named in a role like any other
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
written once.

### People's tokens

A page that signs somebody in gets an **ID token**: it says to that page who
signed in, and its `aud` is the page's own client. It is not for calling an API.
Sent to two APIs that both accept `aud=<page>`, either one can replay it at the
other as that person -- a log line or a compromised container is enough.

So each API is an **audience**, a name of its own (`urn:hday:api:kamino`), and a
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

## An app calling another app

kamino calling khala about acme has to show khala it is `@acme/kamino`, and its
own credential cannot: an `rk_` does not introspect on the data plane, and
`roster-at` is a header only roster reads. So it exchanges one (#74):

```
kamino ─ rk_ + roster-at: @acme ─▶ DelegationService/Exchange{audience: @acme/khala, methods}
       ◀─ rd_…   (about @acme/kamino, issued to @acme/khala, fifteen minutes)
kamino ─ Bearer rd_… ─▶ khala
khala  ─ rk_ + roster-at: @acme ─▶ TokenService/Introspect(rd_…)
       ◀─ @acme/kamino, acme, the methods
khala  ─ HolderService/Reaches(@acme/kamino) → covers → serves it, or not
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
- **`methods` are the receiver's.** What the token is for at khala --
  `/hday.khala.RobotService/Get` -- which roster neither knows nor checks. khala
  narrows by them as it would by a key's, and still decides with `Reaches`.
- **What each side's role needs.** The sender `/roster.DelegationService/Exchange`;
  the receiver `/payday.TokenService/Introspect`. Both go in the `--role` an app is
  installed with.

Acting **as a person** at another app -- kamino calling khala on erin's behalf --
is not this, and nothing does it yet: kamino holds erin's access token, not a
delegation, and the token was issued to the page.

## One client for all three

An app's business logic wants to say *as this tenant, call roster* or *as this
tenant, call khala*. Which credential and which header that takes is the shape's,
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

## Where each fact is

| | |
| --- | --- |
| `proto/app/nomination.proto` | the nomination, and why it left `Host` |
| `server/keys/at.go` | narrowing a deployment key, and what it does not narrow |
| `server/core/nomination.go` | the two questions a nomination is asked |
| `cli/control.go`, `narrowedFlag` | a deployment key with nothing on it |
| `cli/login.go`, `nominate` | the Login App setting itself up in every tenant |
| `cli/app.go` | `roster app install` / `uninstall` |
| `account/account.go`, `ldap/ldap.go` | two consumers in the A shape |
| `ts/lib/tenant/apps.tsx` | the *apps* tab, in both consoles |
| [login.md](login.md) § *What a front door needs* | the same arrangement from the Login App's side |
| [operating.md](operating.md) | `roster login provision`, and upgrading from `Host.acts_as` |
