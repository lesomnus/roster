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
| may it act in another tenant | yes, wherever it was nominated | no | no |

The Login App is A when a roster operator runs it for everybody and C when a
tenant runs their own; glossary, *the Login App*.

## A -- one instance, many tenants

```
control plane   @owner/kamino ── rk_ (one key, rotated freely)

tenant acme     Host  kamino.acme.example         names say which tenant
                @acme/kamino                      who the app is here
                Nomination  borrower=@owner/kamino → @acme/kamino
                Binding     role kamino-app → @acme/kamino

tenant beta     Host  kamino.beta.example
                @beta/kamino
                Nomination  borrower=@owner/kamino → @beta/kamino
                Binding     …
```

A request is about the tenant whose name it arrived at, and it says so:

```
authorization: Bearer rk_…
roster-at: kamino.acme.example
```

roster resolves the name to its tenant through the `Host`, then finds the
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
that tenant, and a `Host` for its name. The Login App's `roster login
provision` does that for the Login App; nothing does it for a product yet (#75).

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

## A person calling the app

Whatever the shape, a request from a person arrives with one of:

| credential | how the app checks it |
| --- | --- |
| an access token from the issuer, `aud` = this app | the issuer's keys; `sub` is a `Holder.id` |
| an `rt_` of the person's (a script, an app password) | `payday.TokenService/Introspect`; answers the holder, tenant and the key's methods |
| an `rd_` this app was given | `Introspect`, which answers only the app it was issued to |

and then asks roster what that person may call, with `HolderService/Reaches`,
and decides with `frame.Covers` against the method being called. The app's own
permissions are its own RPCs, and anything finer than one RPC is a **method of a
permission service** the app declares in its own proto package -- for example
`hday.oasys.AdminService/SeeEveryTenant` -- named in a role like any other
method. A role is a bundle of those; an app asks about the permission and never
about the role's name, so a role can be split or renamed without the app
noticing.

Every shape asks the same questions in the same order, so this half of an app is
written once.

## An app calling another app

Not yet possible with an `rk_` (#74). kamino calling khala on acme's behalf has
nothing khala can check as `@acme/kamino`: an `rk_` does not introspect on the
data plane, and `roster-at` is a header only roster reads. The direction filed is
roster exchanging `rk_` + `roster-at` for a short-lived token naming the
nominated holder, with an audience, that the receiving app checks with
`Introspect` -- **checked by roster**, because roster issues nothing a third
party verifies on its own (`CLAUDE.md`, *the other rule*). Until then, an app
that has to call another uses C's shape for that call: an `rt_` on its holder in
that tenant.

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
	// A token naming this app's holder in tenant, for calling audience. (#74)
	Token(ctx context.Context, tenant pdid.Id, audience string) (string, error)
}

var ErrNotServed = errors.New("this instance does not serve that tenant")
```

- **The prefix says how to authenticate.** `rt_` is C: one tenant, read from
  `MeService.Get`, and `Roster()` is the bearer alone. `rk_` is A or B: the
  bearer and `roster-at`.
- **roster says which tenants.** For an `rk_`, the tenants are the ones with a
  `Nomination` for the key's holder, and the name to send is one of that
  tenant's `Host` rows. One tenant is B, several is A -- the same binary, a
  different key. How an app reads its own nominations without a key that can
  read everybody's is open; a `List` filtered by `borrower_id` works today and
  needs the key to hold `NominationService/List` unnarrowed.
- **`Token()`** is #74, and is the bearer itself in C until then.

What this layer **cannot** hide, and should not try to:

- **Whether another tenant can be reached at all.** A can, B and C cannot, and
  that is a fact about the product, not a detail of authentication. The layer
  answers `ErrNotServed`; the business logic decides what a feature that crosses
  tenants does in a shape that has none.
- **Setting a tenant up.** A needs a holder, bindings, a nomination and a name
  per tenant (#75); C needs a key. That is operations, written down beside the
  shape, not code.
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
| [login.md](login.md) § *What a front door needs* | the same arrangement from the Login App's side |
| [operating.md](operating.md) | `roster login provision`, and upgrading from `Host.acts_as` |
