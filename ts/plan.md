# Two official UIs over roster — the plan

roster ships two screens today and neither covers much of it. This is the plan
for making them cover most of it: the **console**, for whoever runs a deployment
and its customers, and the **account app**, for a customer's own people. Both
are API consumers and nothing more -- the pitch to a deployment is *our UIs are
just the API, used the way you would use it* -- and this document is about
keeping that true while there is a lot more of both.

It is a working document. When a phase lands, its row goes in
`docs/roadmap.md` § Progress in the same commit, and the why of each decision
goes beside the thing it decides (a file comment, a proto comment), not here.
When every phase has landed this file is retired, the way `PLAN.md` was.

## Why two apps and not one

Settled in conversation before this was written; recorded here because every
phase below leans on it.

- **They front different listeners with different trust.** The console reads
  `control.http` (the deployment's own operators and keys) and `admin.http`
  (customers' rows, *no wall at all*, "bind it where only a console reaches").
  The account app fronts the walled data plane on the public internet. One app
  holding both credentials is an internet-facing process that can reach the
  unwalled listener.
- **They authenticate different people differently.** An operator holds a
  session cookie roster's own `AuthService` issued and reaches wide. A person
  holds the account app's cookie, and the app holds a *delegation* narrowed to
  an allow-list (`frontdoor.Config.Methods`). One binary with two auth modes is
  the smell.
- **A sign-in page is per operator.** contoso's people see contoso's providers,
  contoso's branding, contoso's `Enrol` policy. The console is one screen for
  one deployment.
- **The reference app's job survives.** `examples/sso` is *to roster what roster
  is to payday*: the consumer that keeps the interface honest. It stays as the
  thirty-line teaching example. The account app is the shippable one, and it
  keeps the same honesty by the rule in § Invariants: it reaches roster only
  over the wire.

What they share is not markup (`frontdoor/web/frontdoor.js` argues this, and
it held): `ts/gen`, `ts/src/client.ts`, the store, `covers()`, and on the Go
side `frontdoor` + payday's `authsession`.

## Where it stands

### The console (`ts/`) today

| screen | listener | reads | writes |
| --- | --- | --- | --- |
| deployment (`main.tsx`) | control | `Me.Get`, `Holder.List` (operators), `ApiKey.List` | `AuthService.SignIn`, `ApiKey.Issue` (service key) |
| customers (`customers.tsx`) | admin | `Tenant.List`, `Holder.List` per tenant | `Tenant.Add`, `Holder.Add`, `Role.Add`, `Binding.Add` |
| person (`people.tsx`) | admin | `Holder.SignsIn` (identities, credentials, keys, in one narrowed read) | `Holder.Disable/Enable/Invalidate`, `Credential.Unlock`, `Vouch.Reset` (generated password, shown once), `ApiKey.Issue` (for a person) |

Every control is gated in the page by `covers(held, want)` against the method
patterns `Me.Get` answers with, so nothing is drawn that the server would refuse.
Reads go through the store, which reconciles a `Watch` for every entity that has
one; writes go through `useCall` and re-read the lists they may have changed.
The sandbox (`sandbox.ts`) runs the server in the page for `npm run dev` with no
backend -- **it has one listener**, so the customers screen is not offered there.

### The reference app (`examples/sso`) today

Routes: `/login` `/callback` (SSO sign-in **and** linking a second provider, told
apart by which state cookie came back), `/session*` (`frontdoor`: password
sign-in, the two-step `continue`, sign-out), `GET /me` (`MeGetResponse`),
`POST/DELETE /me/ways` (`Identity.Add` own ref / `Me.Unlink`), `/me/sign-out-everywhere`,
`POST/DELETE /me/keys` (`ApiKey.Issue` own ref / `Holder.RevokeKey` own ref), `POST /me/password`
(`Credential.Set`, own ref + `current`), `POST /me/factors` (`Credential.Enrol`, own ref), and
`GET /account`, one HTML file over all of it.

Its shape has three things an official app cannot keep:

1. **One provider, from config.** `Config.Issuer/ClientID/Provider` is one
   provider for the whole app. roster already stores the per-tenant answer
   (`Connection`, `MailDomain`) and the app does not read it.
2. **One tenant's credential.** It authenticates to roster *as a Holder*, so
   the wall narrows it to one tenant. Fronting several operators needs a
   credential whose actor is not inside a tenant -- a service key -- and the
   tenant named per request from the host (`tenantOf`, which it already does).
3. **The browser talks to the Go app's own JSON.** `GET /me`, `POST /me/...` are
   hand-written per feature, so none of `ts/gen` is reused and every new
   feature is a route, a handler and a fetch.

### Coverage, entity by entity

`✓` served by a screen · `◐` partly · `✗` not drawn · `—` not that app's to draw

| entity | console | account app | note |
| --- | --- | --- | --- |
| `Tenant` | ◐ list/add | — | no edit, no labels, no erase |
| `Host` | ✗ | ◐ (resolves it, does not show it) | which names reach which tenant |
| `MailDomain` | ✗ | ✗ | `@contoso.com → entra`; the account app should *use* it for identifier-first routing |
| `Connection` | ✗ | ✗ | **the ask**: configure Entra/GitHub/Google in the UI; the app reads it instead of `Config.Issuer` |
| `Holder` | ◐ list/add/disable/enable/invalidate | ◐ `Me.Get` | no profile edit either side, no erase |
| `Identity` | ◐ read via `SignsIn` | ✓ link/unlink | operator-side unlink is a write the console does not offer (verify it should) |
| `Email` | ✗ | ✗ | list, add, verify, remove -- `date_verified` cannot be asserted, so verify is a link flow |
| `Credential` | ◐ unlock, reset | ◐ change password (`Set` + `current`), enrol TOTP (`Enrol`), own ref | no *remove a factor* (`Erase` is closed on the wire); WebAuthn enrol not drawn |
| `ApiKey` | ✓ | ✓ | -- |
| `Delegation` / `Session` | ✗ | ◐ sign out everywhere | no *where am I signed in* list; nothing reads these (closed / unserved) |
| `Site` `SiteMembership` | ✗ | — | |
| `Team` `TeamMembership` | ✗ | ◐ `Me.Get.teams` | membership carries a role; that is a grant |
| `Group` `GroupMembership` | ✗ | — | `GroupMembership.Add` grants as much as `Binding.Add` |
| `Role` `Binding` | ◐ add only | — | no list, no edit of `methods`, no remove |
| `Audit` | ✗ | ✗ | `AuditService` is registered and read-only; a trail screen is the cheapest big win |
| `Continuation` `Link` | — | — | short-lived, unserved; the flows draw them, the screens do not |
| `Outbox` | — | — | served to nobody |

And the flows the account app is missing: **recovery** (forgot password →
`Vouch.Link` mails a link → `Redeem` → set a password) has the RPCs and no
screen; **identifier-first** sign-in (type an address, be sent to the right
provider via `FrontService.WhereFrom`) has the RPC and no screen.

## Invariants

Written once so every phase can point at them.

1. **Only the wire.** The console imports `ts/gen`. The account app imports
   `rstr` (generated clients), `frontdoor`, and payday's `authsession` --
   never `server/*`, `internal/*`, `cmd/*`. `go vet`-checkable with a depguard
   rule; add it in P4 when the account app becomes a package here.
2. **Nothing drawn that would be refused.** Every control is behind `covers()`
   against `Me.Get.methods`. A new RPC is not on a screen until the screen
   checks for it.
3. **Every new write answers the two questions first**: *is this a grant?* and
   *does this write a way into somebody's account?* -- `server/core/escalate.go`.
4. **The line, as drawn on 2026-08-30 and kept here.** RBAC stays what it is:
   a role grants a **method**, tenant-wide, and the gate is not taught about
   rows. Object rules -- *this caller, this target* -- are **layers** in
   `server/core`, and the one roster has is `mayReach`: self always passes,
   anybody no wider than you passes, anybody wider is refused. Rules finer
   than that (*A may never touch B*, graphs of ownership) are the
   deployment's -- its own layer if it builds roster, its own code if it
   consumes roster, an authorization service beside roster if the relation is
   a graph (`docs/position.md`). **Therefore: no self-only variants of an
   entity's verbs.** Self-service is the existing verb with the caller's own
   reference, and *the app is the layer that passes only that reference*.
   `ChangeMine`/`EnrolMine` existed for one increment and were folded back
   into `Set`/`Enrol`; what `ChangeMine` had that mattered -- proving the
   current password before your own is replaced -- is `Set`'s rule for your
   own row (`current`), not a verb.
5. **Overlay before service, layer before overlay** (CLAUDE.md). Each new RPC
   below names which it is. A new service carries its *Why it is not XService*
   paragraph or does not exist.
6. **The browser never holds a roster token.** The account app's cookie is its
   own; the delegation stays in the Go process. This is what makes P4's proxy
   the design rather than "call roster from the browser with the delegation".
7. **Regenerate, then `./scripts/test.sh`**, `--ts` included: a screen over a
   stale `ts/gen` is a green local run and a red branch.
8. **Progress rows in `docs/roadmap.md`, in the same commit.**

## Decisions, taken

Each was a fork the code could not straddle. All nine were settled on
2026-09-03 (E and F "as recommended"; A–D by the line; G by the owner; H and I
as recommended). They stay here until the code that embodies each has landed
and carries the why in its own comment; then the entry is deleted.

**A. "Edit my profile" -- settled by the line, no new RPC.**
**`Holder.Update(ref)`**, which exists. The account app passes the session's
own reference and nothing else; that is the layer. A role naming
`Holder.Update` means *edit anybody in the tenant you reach*, and a deployment
that wants it narrower adds its own layer -- RBAC as it is. What roster adds
is a guard in `server/core`, not a verb: `Update` refuses `alias` (how others
name you; unique in the tenant), `idp_subject`, and the epoch fields to
anybody but an operator's own path -- **verify** what `Update` accepts today
before drawing the form. Neither escalation question fires: a profile is not
a grant and not a way in.

**B. Removing a second factor -- open `Credential.Erase` behind a layer.**
`Erase` is closed on the wire today only because the service was shut whole
and reopened per method; nothing about `Erase` answers with a verifier. So the
line's answer is the verb that exists, with the layer roster owes it:
`mayReach` on the row's holder (self passes; a plain user reaches other plain
users, which is RBAC as it is), **refuse the last way in** (the D42 rule
`Me.Unlink` already applies, count and write in one transaction), and refuse a
password by `Erase` at all -- a password is replaced by `Set`,
never removed. The account app passes the person's own row. No `RemoveMine`.

**C. "Where am I signed in" -- serve `Delegation` behind the stripping
layer.** The rows carry the token, which is why the service was closed -- and
the layer refactor's own answer to that (2026-08-30: *provide the layer that
keeps it from leaking, and implement there*) is `pd.Secret` on the way out,
the machinery that already strips `secret` from every other answer. So
**`Delegation.List`/`Get`** are served with `secret` stripped and narrowed by
the row's holder (`mayReach`), and **`Delegation.Erase(ref)`** is the sign-out
of one, guarded the same way; `Revoke(token)` stays for the caller that holds
a token. The account app lists and erases the person's own rows. No
`Me.Get.sessions`, no `Me.SignOut`: `MeService` is not grown when the entity's
own verbs, served properly, answer.

**D. Verifying an address I add -- decided: on the resource.** Checked:
nothing in `server/` touches `date_verified` on `Redeem`, and `VouchLinkRequest`
carries `who` and `expires` and no purpose, so today a link is a recovery link
and nothing else. Recovery stayed on `VouchService` for one reason: it is
addressed to an **address**, and there is no row to reference. Verification has
the row -- the `Email` the person just added -- so the same criterion puts it on
the resource: **`Email.Verify(ref)`** starts it (mints the link through the
`Link` table and the outbox, exactly as `Vouch.Link` does) and
**`Email.Confirm(token)`** spends it, stamps `date_verified`, and **mints no
delegation** -- a verify link is worth strictly less than a recovery link, or
adding an address to my account mints me a sign-in link. `Verify` takes the
row's reference and `mayReach` guards its holder; the account app passes the
person's own row (the line). `date_verified` stays a field no request may
assert.

**E. What the account app authenticates as -- decided: one `rt_` per tenant.**
Checked, and it changed the recommendation: a deployment key (`rk_`) resolves to a frame with **no tenant**
and the policy hands it **`frame.Everything`** (`cmd/auth.go`, *What the frame
says*). The wall does not narrow a service key at all; only its method grant
does. So an internet-facing account app holding one `rk_` is an actor that
reaches every tenant, and the only thing keeping contoso's request out of
fabrikam's rows is the app's own code -- "the wiring is the whole of the
control", which is the shape roster refuses elsewhere. And `Vouch.Accept` is
*sign in as whoever the app names*; on an `rk_` that is anyone, anywhere.

Recommend **one tenant key (`rt_`) per tenant the app fronts**, for a holder
`account` in that tenant, chosen by host: the wall narrows every request to
that tenant with no discipline asked of the app, and a key that leaks is one
tenant's. **No new server API**: this is `ApiKey.Issue` with a `holder`, which
`roster key add --tenant contoso --holder account` and the person screen
already call; it is what the reference app does for one tenant, N times. The
console's sign-in tab (P1) is a button over that same call, and the app reads
the minted keys from its own secret store keyed by tenant alias (`env:`, the
`secret_ref` convention) -- roster shows a key once and never distributes one.
Keep the app's **own** credential
small by moving pre-sign-in facts to `FrontService`, which exists for exactly
*before anybody is resolved to a tenant*: **`Front.Connections(host)`** answers
the provider buttons (every `Connection` field is public by design) so the
sign-in page needs no key at all; everything after `Accept` is the person's
delegation. Alternative: `rk_` plus per-request tenant refs -- rejected above.
**Spike first** (P0): an `rt_` for `account@contoso` calling `Identity.Get`,
`Holder.Add`, `Vouch.Accept` on the data plane, and a second tenant's rows
invisible to it. If payday is in the way, fix payday.

**F. How the account UI reaches roster -- decided: the proxy.** **`frontdoor`
grows a Connect reverse proxy**: `POST /roster.*/*` on the app's origin, which swaps the
session cookie for the held delegation, refuses anything outside
`Config.Methods`, and forwards. Then the account UI is `ts/gen` + the store with
`transport = the app's origin` -- *"the transport is the only thing that
changes"* (`client.ts`) becomes literally true for both UIs, and every feature
stops being a route + handler + fetch. Invariant 5 holds: the browser holds the
app's cookie, never the delegation. Alternative: keep hand-written JSON routes --
no, it is why `examples/sso` cannot grow.

**G. Where the account app's code lives -- decided.** **`account/`** (Go package
+ `cmd/roster account serve`) and **`ts/account/`** (Vite app), with the shared
TypeScript moved to **`ts/lib/`** (`client.ts`, `store.ts`, `page.tsx`'s
`covers`) and the console to **`ts/console/`**. One `package.json` with
workspaces, one `ts/gen`. Alternative: a second repository -- no, it would pin
roster and lag `ts/gen` the way `@main` lags.

**H. Effective permissions of somebody else -- decided: `Holder.Reaches`.** The console wants *what does
alice reach* the way `Me.Get` says it for the caller. `cmd/policy.go` answers
from three sets and no RPC exposes it for another holder. Recommend
**`Holder.Reaches`** (overlay; read-only; returns patterns, not an expansion,
for the rolling-deploy reason `covers()` gives). Escalation: a read, but it
reveals grants -- gate it on the caller reaching the target (`mayReach`).

**I. Operator-side identity unlink and email removal -- decided: draw them.** Both are reachable
today (`Identity.Erase`, `Email.Erase` through the wall) and neither is drawn.
Recommend drawing them in P3 with no new RPC. Not a decision so much as a check
that both already pass `mayWriteAWayIn` for the operator (they should: removing
a way in is not adding one) -- **verify** in the tests before the button exists.

## The order

Dependencies force most of it: the console's sign-in configuration (P1) is what
the account app reads (P4), and the account app's transport (F) is what every
account screen (P5) is built on.

### P0 · Spikes and the decisions above

Nothing shipped. Answers A–I in file comments where the code will go, and two
spikes that decide P4's shape:

- **E**: a service key acting for a host-resolved tenant on the data plane.
  Test in `cmd/` next to `customerkey_test.go`.
- **F**: a Connect proxy in `frontdoor` that forwards one `Me.Get` with the
  delegation swapped in, under the `Methods` allow-list. Test in
  `frontdoor/`.

If either spike finds payday in the way, fix payday and move the pin
(CLAUDE.md, *the one rule*).

### P1 · Console: how a customer's people sign in

The ask that started this. One new tab on a customer, three tables, all on the
admin listener, no new RPC:

- **Hosts** (`Host` list/add/erase): which names reach this tenant.
- **Mail domains** (`MailDomain` list/add/edit/erase): `@contoso.com → entra`.
  The `provider` column is a `Connection.name`; draw it as a choice, not a text
  field.
- **Connections** (`Connection` list/add/edit/erase): `issuer`, `client_id`,
  `scopes`, `secret_ref`. The screen says plainly that `secret_ref` is *where
  the account app finds the secret* (`env:CONTOSO_ENTRA_SECRET`) and roster
  never reads it -- the `Connection` proto comment is the copy.
- **Try it**: a link to the account app's `/login?tenant=…&connection=…` once
  P4 exists. roster cannot test a connection (it is not the relying party); the
  app can, by signing in.

Tests: each write through the wall from an operator's session; `covers()`
hiding each control for a session that lacks it. Progress row: *P1 · sign-in
configuration in the console*.

### P2 · Console: organisation, access, and the trail

Still no new RPC except **H**.

- **Sites** and **teams** per tenant (`Site`, `Team` list/add/edit/erase;
  `SiteMembership`, `TeamMembership` add/remove). A team membership carries a
  `role`: the screen labels it as a grant and gates it on `Binding.Add`'s
  pattern, because that is what it is (`escalate.go`).
- **Groups** (`Group`, `GroupMembership`), same treatment: *adding somebody to a
  group hands them everything bound to it*, said on the screen.
- **Roles and bindings**: `Role` list/edit (`methods` as a pattern editor with
  `covers()` previews), `Binding` list/add/remove, and per person **what they
  reach** (H).
- **Trail**: `AuditService.List` with filters on actor, object, action, time.
  Read-only; the cheapest screen with the most value. The `Audit` wall is
  three-way (`tenant_id OR actor_tenant_id OR counterpart_tenant_id`), so a
  row about a cross-tenant act appears to both -- say so on the screen.

Progress row: *P2 · organisation, access, trail*.

### P3 · Console: the rest of a person, and the deployment's own screen

- **Person**: `Holder.Update` (name, desc, labels, `profile`), `Holder.Erase`
  (soft; the row says *erased*, not gone), **emails** (`Email` list/add/erase;
  `date_verified` read-only), **unlink an identity** (I), **enrol a factor for
  somebody** (`Credential.Enrol`, operator-side -- for a hardware key issued in
  an air gap; gated by reach, which it already is).
- **Deployment**: operators (`Holder` on the control plane) get **add an
  operator** = `Holder.Add` + `IssueService.IssuePassword`, the generated
  password shown once, the way `Vouch.Reset` is on the person screen. Service
  keys get **edit methods/expiry** and **revoke** (`ApiKey.Patch/Erase` on the
  control listener, where `Keys` is on).
- **Tenant**: edit name/desc/labels; erase is hard and stays in the CLI.
- **Sandbox**: grow it a second listener so the customers tab is offered under
  `npm run dev`, or keep it as one and say so. Recommend growing it: a screen
  that can only be developed against a real deployment is a screen that gets
  less careful.

Progress row: *P3 · the console covers the entities*.

### P4 · The account app becomes official

The structural phase; no new screens yet.

1. **G**: `account/` (Go), `cmd/roster account serve`, `ts/account/` (Vite),
   `ts/lib/` extracted from `ts/src/`, console moved to `ts/console/`.
   `examples/sso` is left as it is, and its package comment gains one paragraph
   pointing here for the shippable one.
2. **E**: the app holds one `rt_` per tenant it fronts, picked by host; the
   wall does the narrowing. The console's sign-in tab (P1) mints it.
3. **Providers from roster, not config.** `Front.Connections(host)` (new, on
   `FrontService`, unwalled, public fields only) is the set of buttons on the
   sign-in page, so the page needs no credential; `MailDomain` +
   `FrontService.WhereFrom` is identifier-first (*type your address, go to your
   provider*). The client secret is resolved from `secret_ref` by the app
   (`env:` first; a second scheme is a later decision). Discovery per `issuer`,
   cached.
4. **F**: `frontdoor` grows the Connect proxy; `frontdoor.js` grows a
   transport that speaks to it, so `ts/gen` + the store work in the account UI
   unchanged.
5. **`Enrol` policy** stays pluggable; ship `Invited` (default) and `Enrolling`
   behind config, exactly the two the example has.
6. **Branding hooks**: name, logo, colours per tenant read from
   `Tenant.labels` (no new field) -- enough for a first cut; a `Branding`
   entity is a later decision if labels prove too thin.
7. **Sandbox** for the account UI: the console's `sandbox.ts` plus a fake
   provider; a person working on the account screen starts no roster and no
   Google.

Tests move with the code; `sso_test.go`'s scenarios become `account/`'s.
Progress row: *P4 · the account app, official*.

### P5 · Account app: the screens

Each is a section of one page, drawn from `Me.Get` and the entity reads
through the store, and each names the RPC it adds. Every write names the
person's own row; the app passes that reference and nothing else.

- **Sign in**: providers from `Connection`; identifier-first via `WhereFrom`;
  password + the two-step `continue` (exists); **recovery** (new screen:
  `Vouch.Link` → the mail → `Redeem` → `Credential.Set` through the flow, which
  is `Vouch.Reset`'s address form; no new RPC).
- **Profile**: `Holder.Update` with the session's own reference (A).
- **Ways in**: identities link/unlink (exists); **emails** list, add
  (`Email.Add` on self, gated in `frontdoor.Methods`), remove (`Email.Erase`),
  **verify** (**D**).
- **Credentials**: change password (exists), enrol TOTP (exists), **enrol
  WebAuthn** (`Enrol` with `attestation`; the browser half is
  `navigator.credentials.create`, in `frontdoor.js`), **remove a factor**
  (**B**), and *last way in* refused with a sentence that says why.
- **Sessions**: **C** list and sign out one; sign out everywhere (exists).
- **Keys**: mint/revoke (exists), shown once.
- **Sign out**: exists.

Progress row: *P5 · self-service, complete*.

### P6 · Serve, document, and retire this file

- `roster serve` serves the console build same-origin (`ts/console/dist`
  embedded), so a deployment needs no `origins:` for it; `roster account serve`
  serves the account UI the same way. `npm run dev` keeps the cross-origin path.
- `README.md` § *A browser* and `docs/position.md` § console gain the account
  app; `docs/entity.md` unchanged (no new entity); `docs/baseline.md` gains the
  promises the account app relies on, pinned to its tests.
- Invariant 1 becomes a `depguard` rule in `./scripts/test.sh`.
- This file is deleted; its decisions are already beside the code by then.

### Later, and not in this plan

- **Hydra.** The account app is the *Login App* Hydra calls when a second
  relying party has to trust the first one's sign-in: `/login` and `/consent`
  endpoints that complete a Hydra challenge with the session the app already
  holds. Nothing below the app changes. A phase of its own when a second app
  exists.
- **A second `secret_ref` scheme** (a secrets manager) when a deployment asks.
- **A `Branding` entity** if `Tenant.labels` proves too thin.

## Progress

Kept current in the same commit as the work, the way `docs/roadmap.md`'s table
is; that table gets the summary row when a phase closes, this one the steps.

| step | state | where |
| --- | --- | --- |
| the API settled: no self-only verbs, `MeService` = the waived three | **done** | `c22b0e2`, `1b76f89` |
| P0 · spike E: an `rt_` for `account@contoso` on the data plane, fabrikam invisible | open | |
| P0 · spike F: `frontdoor` forwards one `Me.Get` over Connect with the delegation swapped in | open | |
| P1 · console: hosts, mail domains, connections per customer | open | |
| P2 · console: sites, teams, groups, roles, bindings, `Holder.Reaches`, trail | open | |
| P3 · console: the rest of a person, the deployment screen, the sandbox's second listener | open | |
| P4 · the account app, official: `account/`, `ts/account/`, `ts/lib/`, `Front.Connections`, the proxy | open | |
| P5 · account screens: recovery, profile, emails + verify, WebAuthn, remove a factor, sessions | open | |
| P6 · serve same-origin, docs, depguard, retire this file | open | |

## What this does not change

- roster's line (`docs/position.md`): it still issues nothing a third party
  verifies. The account app's cookie is the app's; the console's cookie is
  roster's own, as it was.
- `VouchService` stays the sign-in and recovery flow; `IssueService` stays
  `IssuePassword`; `SyncService` stays a curated stream. No verbose service
  comes back.
- `examples/sso` stays, and stays small.
