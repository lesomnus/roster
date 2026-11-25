# Loft, a product on roster

Loft is an asset and booking product that keeps its own rows and asks roster
who somebody is and what they may do. It is the worked case of
[apps.md](apps.md): what one product asks of roster, step by step, and what of
roster carries each step.

What it is read from is lesomnus/loft at
[6235e51](https://github.com/lesomnus/loft/tree/6235e51) (2026-11-23), whose
documents are in Korean. That commit pins a roster of 2026-11-12
([go.mod](https://github.com/lesomnus/loft/blob/6235e51/go.mod#L42),
`36fafdadc11b`): before the review that froze roster's wire (#273) and before
a grant was at one app (#567). So each step below says what it is on roster
today -- **the same today**, or **today** with what a product does on this tree
instead. What is said of today was run on this tree; what Loft would do on it
is not, since Loft has not been built against it.

## What was remembered, and what is built

**Remembered: a portal that makes a tenant for each user.** That is
[lesomnus/loft#326](https://github.com/lesomnus/loft/issues/326), a proposal,
open: a portal at `loft.example` where somebody signs in and makes a
workspace, which is a roster tenant of its own with a host of its own. It is
the last section of this page, as proposed and not built.

**Built: one deployment, one tenant.** On 2026-11-12 the owner decided
([lesomnus/loft#327](https://github.com/lesomnus/loft/issues/327), closed that
day; the decision is quoted at the top of #326) that the portal goes in another
repository, later, and that Loft first runs self-hosted for one tenant. The code
does that: one Loft deployment serves one roster tenant, which a roster
operator makes; there is no portal, no tenant per user, no self sign-up and no
invitation ([design.md, D85 and D91](https://github.com/lesomnus/loft/blob/6235e51/design.md#L1895)).
Loft also runs with roster built into its own process, for its tests and for
trying it out; this page is the deployment people use, with roster beside it
([design.md, 9.10](https://github.com/lesomnus/loft/blob/6235e51/design.md#L1283)).

## As built, step by step

### 1. The tenant

A roster operator makes the tenant, a password for its first administrator,
and the name roster's user console is served at
([docs/dev/identity.md](https://github.com/lesomnus/loft/blob/6235e51/docs/dev/identity.md#L26)):

```sh
roster init
roster --as admin tenant add @acme '{"name": "ACME"}'
roster --as admin vouch reset @acme/admin
roster --as admin host add '{"tenant": {"alias": "acme"}, "name": "id.example.com"}'
```

What carries it: `TenantService/Add`, which makes the tenant with `@acme/admin`
and the role that administers it in one act; a password issued at the box; a
`Host` saying the name is acme's.

**Today** the same, but that `vouch reset` names who is at the shell
(`--as admin`), as every command at the box that reads the database does
([usage/cli.md](usage/cli.md)); Loft's page writes it without, and today that is
refused before anything is opened.

### 2. Loft's install, and its key

Loft is a holder of the tenant, `@acme/loft`, reached with a tenant key on it:
shape C ([apps.md](apps.md) § *C -- the tenant runs it*). Loft refuses a key
that is not an `rt_`
([internal/identity/identity.go](https://github.com/lesomnus/loft/blob/6235e51/internal/identity/identity.go#L308)).
At the pin it was installed with one list, `identity.AgentMethods` -- what
`standing` and `reach` ask, `VouchService/Verify`, `TenantService/Get`,
`MeService/Get`, the role and binding verbs it writes its roles with, and
`/loft.*/*` -- as a role of roster's, and the tenant's administrators given
Loft's methods by a pattern
([identity.go](https://github.com/lesomnus/loft/blob/6235e51/internal/identity/identity.go#L164)):

```
roster app install --tenant acme --role "$m" --administer '/loft.*/*' loft
roster key add --tenant acme --holder loft --name loft --allow "$m"
```

**Today** `--role` is `--allow`, and it names roster's methods alone: a role of
roster's naming `/loft.*/*` is refused where it is written, *names no method of
roster's; an app's methods are written in a role of that app*. Loft's own
methods are `--serves`, a role of Loft's (`loft-everything`) bound to its holder
and to the people `--administer` names, now by alias; and beside it
`app-administrator` is bound for Loft to the same, which is roster's verbs over
what is Loft's -- its roles, their bindings, its groups, the keys for it -- and
takes the place of the role and binding verbs in Loft's list. The key lists
what Loft calls, those verbs among it:

```sh
m=/roster.VouchService/Verify,/roster.HolderService/Get,/roster.TenantService/Get
m=$m,/roster.EmailService/List,/roster.EmailService/Get,/roster.SyncService/Watch,/roster.HolderService/Reaches
roster --as admin app install --tenant acme --allow "$m" --serves '/loft.*/*' --administer admin loft
roster --as admin key add --tenant acme --holder loft --name loft \
  --allow "$m,/roster.MeService/Get,/roster.RoleService/Add,/roster.RoleService/Get,/roster.RoleService/Widen,/roster.BindingService/Add"
```

The holder is labelled `roster.app: loft`, so roster's directory and SCIM
endpoint leave it out of the tenant's people (#451); `install` writes a
nomination too, which Loft's `rt_` never asks. Installed again, it goes on for
that holder whatever it holds, the key among it, since the label says it is
Loft's own ([apps.md](apps.md) § *Installing one*). A holder an earlier roster
made carries no label, so with Loft's key on it a run is refused until
`--adopt`, which writes the label.

### 3. `loft init`: the workspace, and the roles Loft ships

`loft init` asks whose the key is (`MeService.Get`, then `TenantService.Get`)
and makes Loft's own workspace row with the tenant's identifier and alias
([identity.go](https://github.com/lesomnus/loft/blob/6235e51/internal/identity/identity.go#L473)).
It writes five roles at roster -- `loft-owner`, `loft-admin`, `loft-manager`,
`loft-member`, `loft-auditor` -- reading each by alias first and leaving one
that is there as the tenant's (`RoleService/Get`, then `Add`), and binds
`loft-owner` to the first owner, the tenant's `admin` unless told another
(`BindingService/Add`;
[identity.go](https://github.com/lesomnus/loft/blob/6235e51/internal/identity/identity.go#L768),
[server/policy/policy.go](https://github.com/lesomnus/loft/blob/6235e51/server/policy/policy.go#L357)).
A later release widens each role by what that release brought, from a ledger
that only adds, so a method the tenant took out does not come back
(`RoleService/Widen`; [design.md, D90](https://github.com/lesomnus/loft/blob/6235e51/design.md#L1900)).

**Today** each `Add` says `app`, Loft's holder: the five are roles of Loft's
(`Role.app`), which is what Loft's `Reaches` is answered of, and one without it
is refused as above. Loft writes, widens and binds them as itself, by the
`app-administrator` it holds for itself, and holds what they name by
`loft-everything`. `Widen` is the same verb, adding and never taking away
([apps.md](apps.md) § *The roles an app ships*).

### 4. People

People are added by the tenant's administrator at roster, in its user console,
and bound one of Loft's roles there; somebody becomes a member of the
workspace at their first sign-in. The tenant narrows the roles Loft ships at
roster too (`RoleService/Narrow`, which Loft asked roster for)
([docs/dev/identity.md](https://github.com/lesomnus/loft/blob/6235e51/docs/dev/identity.md#L155),
[design.md, D87](https://github.com/lesomnus/loft/blob/6235e51/design.md#L1897)).

**Today** whoever binds one of Loft's roles holds Loft's methods at Loft: the
tenant's administrator does when `install` named them in `--administer`, and
otherwise holds nothing there; an app's administrator, bound `app-administrator`
for Loft and `loft-everything`, does it with nothing else of the tenant's
([apps.md](apps.md) § *An app's administrators*). A role of Loft's is bound to
a person or to a group of Loft's, and held in no team. `Narrow` is the same.

### 5. Signing in

Two ways, and both end in a holder of the tenant
([design.md, 9.10](https://github.com/lesomnus/loft/blob/6235e51/design.md#L1309)):

- **A password**, at Loft's own sign-in page: `POST /session` asks
  `VouchService/Verify` as Loft's holder, naming the person by alias or by an
  address, and no tenant -- the key says which. A `continuation` in the answer
  is a second factor owed, and the password alone signs nobody in; then
  `HolderService/Get`, refusing somebody suspended
  ([identity.go](https://github.com/lesomnus/loft/blob/6235e51/internal/identity/identity.go#L520)).
- **Through roster's issuer**: Loft is a relying party of the deployment's
  Hydra and roster's Login App, the *itself* shape of
  [relying-party.md](relying-party.md), and the ID token's `sub` is the holder
  ([server/sso/sso.go](https://github.com/lesomnus/loft/blob/6235e51/server/sso/sso.go)).

Loft uses none of `Accept`, `Link`, `Redeem`, a key or a delegation to sign
anybody in.

**Today** the same: the Login App reads a flow's tenant from the host of the
redirect it names, so the tenant has a `Host` for Loft's own name as well, as
Loft's page says ([login.md](login.md) § *What changes when Hydra is in
front*). A locked password is `date_unlocked` in
the answer; Loft reads it as `date_locked`
([identity.go](https://github.com/lesomnus/loft/blob/6235e51/internal/identity/identity.go#L545)).

### 6. Loft's own session

After either, Loft holds a session of its own -- a cookie, and a row in its
own database -- naming the member. At the first sign-in it asks `reach` whether
the person may call `/loft.PartyService/Me`, and somebody roster binds no role
of Loft's is refused, `403` ([identity.go](https://github.com/lesomnus/loft/blob/6235e51/internal/identity/identity.go#L865),
[server/session/session.go](https://github.com/lesomnus/loft/blob/6235e51/server/session/session.go#L414)).

**Today** the same call; `Reaches`, asked by Loft as itself, is answered of
Loft's roles alone, so somebody who holds only roster's roles reaches nothing
there ([apps.md](apps.md) § *A person calling the app*).

### 7. Every call

On every call a session makes, Loft asks two things, each through roster's
package for it, kept half a minute and dropped early by the stream:
`standing`'s `Held` -- is this person still good, since this session began --
and `reach`'s `May` -- may they call this method. **Loft keeps no roles**:
which role somebody has is never asked, and its permissions finer than one RPC
are methods of its own `PermissionService` (`BookForOthers`, `RunWork`, …) that
a role names and nobody serves. Its pages draw their menus from the methods the
person reaches. A roster that cannot be asked is a refusal, not a yes
([design.md, D89](https://github.com/lesomnus/loft/blob/6235e51/design.md#L1899),
[proto/loft/permission.proto](https://github.com/lesomnus/loft/blob/6235e51/proto/loft/permission.proto)).

**Today** the same: `standing.Accounts.Held`, `reach.Holders.May` and their
followers are there by those names ([relying-app.md](relying-app.md),
[apps.md](apps.md) § *Asking it: `reach`*); the methods are held in roles of
Loft's.

### 8. What arrives on the stream

Both followers run in every process (`loft.roster-stream`, `loft.roster-reach`;
[cmd/serve.go](https://github.com/lesomnus/loft/blob/6235e51/cmd/serve.go#L494)).
Somebody suspended or erased, or the tenant closed, ends every session of
theirs; signed out everywhere ends the ones from before. An erasure marks the
member leaving, and Loft pseudonymises its own records of them once roster's
grace has passed (`app.members.forget_after`, thirty days, kept equal to
roster's), asking roster again before it acts on the mark; once a day every
member is asked about again, for what the stream missed
([server/lifecycle/lifecycle.go](https://github.com/lesomnus/loft/blob/6235e51/server/lifecycle/lifecycle.go#L96),
[reconcile.go](https://github.com/lesomnus/loft/blob/6235e51/server/lifecycle/reconcile.go#L66)).
A change to what somebody may call drops what `reach` kept of them.

**Today** the same ([relying-app.md](relying-app.md) § *Heard: the stream*
and § *Looked again: what the stream cannot carry*).

### 9. What Loft keeps, and what it asks each time

Loft keeps its own copy of each member -- a row with roster's identifier,
alias and name -- and its own person records. It asks roster for one holder,
for the addresses somebody checked (only a checked address links a member to a
person record), and for what they reach. It lists neither people nor roles
from roster ([identity.go](https://github.com/lesomnus/loft/blob/6235e51/internal/identity/identity.go#L588)).

**Today** the same.

### 10. No tokens for anybody

Loft calls no `ApiKeyService` method and asks for no delegation. A terminal
signs in to Loft itself -- `loft auth login`, approved in a browser where the
person is signed in at Loft -- and holds a session of Loft's own, narrowed to
the methods it asked for and held to roster like a browser's; tokens for
automation are left for later
([server/clilogin/clilogin.go](https://github.com/lesomnus/loft/blob/6235e51/server/clilogin/clilogin.go),
[design.md, 9.11](https://github.com/lesomnus/loft/blob/6235e51/design.md#L1340)).

**Today** the same; what roster offers for it is the next section.

### 11. Ending a workspace

Closing, retiring and purging a workspace are the Loft operator's
(`loft tenant close`, `retire`, `purge`), and touch neither roster's tenant
nor its people ([design.md, D85](https://github.com/lesomnus/loft/blob/6235e51/design.md#L1895)).

**Today** the same.

## What a product like Loft would use that Loft does not

Three things roster has today, each for something Loft does another way or
leaves for later. **A key for one app** (`ApiKey.app`): a page of Loft's where
somebody mints a token for a script, narrowed to some of what they hold at
Loft, minted as them through a delegation they approved and checked with
`Introspect`, which answers it to Loft alone -- the automation tokens Loft left
for later ([apps.md](apps.md) § *Giving an app's users API tokens*). **A
delegation handed to the app** (`DelegationService/Issue`): what Loft's pages
would write at roster for its administrator -- putting somebody into a group,
binding a role -- written as that person, held to what they hold and named on
the trail, where today the tenant's people are managed in roster's user
console ([apps.md](apps.md) § *Writing for its administrator, at roster*). **An app's groups**
(`Group.app`): *everybody who may use Loft* as a group of Loft's, bound Loft's
roles once, which its administrators fill, rather than a binding per person
([apps.md](apps.md) § *Reading an app's users and what each may do*).

## The portal, as proposed and not built

[lesomnus/loft#326](https://github.com/lesomnus/loft/issues/326) would split
Loft in two: a portal, where somebody signs in, makes workspaces and pays, and
the workspace app, which is Loft as it is. **For every workspace the portal
would write, with a deployment key**: a tenant; its `Host`
(`<alias>.loft.example`); the app's install in it, with its nomination; the
first owner, a holder of the new tenant linked by the same provider's account
as the portal's; and the owner's binding. The workspace app would be shape A
of [apps.md](apps.md), one instance for every tenant, each request naming its
tenant by the host it arrived at (`roster-at`).

What it says it would need of roster, each as roster is today:

| #326 asks for | today |
| --- | --- |
| one door that makes the tenant, its `Host`, the install, the first owner and the binding together, in one transaction | **not there.** `TenantService/Add` makes a tenant with its first administrator in one act (`server/core/tenant.go`); a `Host`, another holder and a binding are each a write of their own; and the install is `roster app install`, a command at the box with no RPC (`cli/app.go`) |
| which tenants have a holder with this identity -- this provider's account -- asked with a deployment key | **not there.** An account at a provider is found within one tenant (`IdentityRefBySubject` names the tenant), and `IdentityService/List` filters by holder and tenant and not by provider and subject |
| a callback at the issuer for every workspace's host | **not there.** roster reads the issuer's clients and registers none (`login/hydra.go`); the Login App takes one client with several tenants' redirects, each flow being the tenant whose redirect it names ([login.md](login.md) § *What changes when Hydra is in front*), so whoever registers the client adds every host to it |
| invitation mail | **not there.** A front door mails the links roster mints -- a recovery link, an address to confirm, a deletion -- and nothing else (`cmd/consumers.go`, `MailConfig`). An invitation is an address somebody else entered, matched at the first sign-in ([login.md](login.md) § *Putting people in, and letting them arrive*); nothing mails it |
| the app's half of asking -- `Reaches`, `Covers`, a cache and the stream -- as a Go package beside `standing` | **there**: `reach` ([apps.md](apps.md) § *Asking it: `reach`*) |

And its own list of risks: roster holding every workspace of the service as a
tenant, thousands of them, each with the app's holder, roles, bindings,
nomination and `Host`; a redirect to register at Hydra for each workspace;
writes across the portal, roster and the app that are not one transaction,
needing a way to finish one that failed half way; and the people pages of the
app rebuilt on roster's API.

Since #326 was written: `roster app install` no longer writes the tenant
administrators' role (it binds `loft-everything` and `app-administrator` to the
people `--administer` names), an app's roles are roles of the app's, and a key
can be for one app. None of it is one of the five above.
