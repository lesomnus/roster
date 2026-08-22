# roster — plan and decision log

roster is the store that answers **who somebody is**: people, the external
identities they sign in with, the addresses they use, and the organisations,
sites and teams they belong to.

It is one layer of an identity system and not the whole of one. The protocol is
Ory Hydra's and the login flow is a Login App's; roster owns the records they
both ask about, and owns `sub`.

It is also the second app payday is tried against, and the more demanding one.

---

## Why it exists, twice

**As a product.** An identity provider can be replaced; the directory behind it
should not have to be. The metadata is ours, the schema is ours, and a customer
IdP's `/userinfo` has only what that customer put in it.

**As a proving ground.** [custody](https://github.com/lesomnus/custody) exercises
payday's domain half — the wall, entities, a public projection. It leaves four
things untouched, and they are roster's whole subject:

| | custody | roster |
| --- | --- | --- |
| the second axis (field 3) | unused | **Site is the isolation boundary** |
| `TokenStore` / `Bearer` | unused | **API keys** |
| `Outbox` / `Drain` | wired, never consumed | **the sync channel** |
| one-to-many edges | none | `Identity`, `Email`, memberships |

### The rule that makes it a proving ground

> **When payday is in the way, stop and fix payday. Do not work around it here.**

roster is a product as well, so the temptation to route around the framework is
real in a way it is not in custody. Every workaround written here is a defect
left in payday for its next user. This is repeated in `CLAUDE.md` because it is
the one rule an agent or a hurried afternoon will break first.

---

## Where roster sits

```
browser → proxy ─┬─ session cookie ↔ token
                 │
                 ├→ Hydra ──login_challenge──> Login App
                 │            (protocol)        (which provider, MFA)
                 │                                  │
                 │                                  ↓  "who is this identity?"
                 │                              ╔═══════╗
                 │                              ║ roster ║  ← owns `sub`
                 │                              ╚═══════╝
                 │                                  ↑  "/api/v1/me"
                 └→ product apps (custody, …) ──────┘
                        verify the JWT locally
```

roster is called by machines: the Login App and admin consoles. Not by end
users, and not by browsers. Its own authentication is therefore mTLS or an API
key — **not** `authoidc`, which is for the product apps that consume its
tokens.

### Not roster

Hydra, the Login App, the login UI, the session proxy, and the flows that run
over them — which provider to offer, when to ask for a second factor, whether to
remember a browser. Devices, certificate authorities, ownership transfer — those
belong to the product.

This list said `AuthProvider` implementations and MFA flows for a while, and
that was one word too wide: roster verifies a password and will verify a TOTP
code, and both read like implementing a provider. What is not roster's is the
**flow**, not the check. D19 is the line stated so that it can be applied rather
than remembered.

### Where roster sits when Hydra is in front

The diagram above is the shape, and it is worth writing out as steps, because
"Hydra does single sign-on" reads as though roster gets smaller. It does not:
Hydra's central design decision is that **it has no user database and does not
authenticate anybody**. It hands a `login_challenge` to a Login App and waits to
be told a `subject`. Where that string comes from is the hole, and the hole is
roster-shaped.

```
browser → product app → Hydra /oauth2/auth
                          │ login_challenge
                          ↓
                      Login App
                          │ Entra / GitHub → (provider, subject)
                    ①     ├──> roster · Identity → Holder.id     ← this is `sub`
                    ②     ├──> roster · VouchService.Verify      (password, link)
                    ③     ├──> roster · the tenant, and the token's other claims
                          │ acceptLoginRequest{subject: Holder.id}
                          ↓
                       Hydra ── code ──> product app
                                            │ exchange, verify, keep a session
                    ④                       └──> roster · MeService, names, teams
```

- **① is the one that cannot be moved.** Use Entra's `oid` as `sub` and the same
  human arriving through GitHub is a second person to every system downstream.
  D1 exists for this and this is where it is spent.
- **② only if this deployment has a password or a magic link at all.** A
  provider-only deployment does not call it.
- **③** because Hydra does not know what a tenant is either.
- **④** is not sign-in. It is the ordinary reading LOGIN.md already describes —
  a product app anchors a row and reads names when there is a screen to draw.

**roster is in the flow once, at sign-in, and beside it afterwards. It is not in
the per-request path.** No session check and no token check reaches it. That is
the property somebody wants when they ask for "accepted everywhere without
another call to roster", and it is had without roster signing anything.

The caller list is unchanged by any of this, which is the sign it is the right
shape: the Login App and admin consoles. A browser never sees roster.

---

## Scope

**Phase 1 — the schema.** The irreversible part, and therefore first.

**Phase 2 — whatever payday gets wrong.** Fixed upstream, not here.

**Phase 3 — the app layer.** `/api/v1/me`, the identity-linking rules, a policy
over memberships, credential verification.

**Phase 4 — later.** API keys, the sync channel, the admin console.

---

## Decisions

Recorded as they are made, with the reason, so that a later disagreement argues
with the reason rather than rediscovering the question.

### D36 · A first factor is its kind's only one, except the one being enrolled

`VouchVerifyRequest` took no name, and `VouchDelegateRequest.name` said why:
*only read with a continuation; a first factor is its kind's only one by
construction.* That is true of signing **in** -- somebody is asked for a
password and there is one -- and it is not true of the call that confirms what
was just enrolled.

`Enrol` invites a name (*"the phone", "the yubikey in the drawer"*) and writes
the row **unconfirmed**, which D29 chose deliberately: an unconfirmed factor is
left out of `available`, because a QR somebody may have mis-scanned is a form
that cannot be filled. What confirms it is verifying one code against it, and
that call is a first-factor call -- a continuation is minted only when
something is left to prove, and what was just enrolled is not.

So for a **named** first factor there was no call that could reach it. `Verify`
resolved by kind with no name, and an unset name in a `CredentialRefByKind` is
`name = ""` rather than "any", so it matched the unnamed row and not this one.
The person scanned a code, the deployment believed it had a second factor, and
nothing ever asked for one.

Two things kept it invisible. Every test enrolled the unnamed factor first --
`totp_test.go` adds "the spare phone" as a *second* one and never confirms it
-- and the two halves it needed are each correct on their own: leaving
unconfirmed factors out of `available` is D29's rule, and matching an empty
name exactly is what makes "the only one" mean something.

The fix is the field, at 5, the number `name` has everywhere. `Delegate`'s
comment was updated rather than deleted: it is right about signing in, and what
it did not cover is enrolling.

### D41 · Everything that had to be refused, asked as one question

The audit began because somebody noticed an ordinary person could make roster
answer as somebody else. Every hole since had been found by tripping over
another instance of a rule that was already written. So the question was asked
from the other end -- *what are all the ways one person becomes another?* -- and
five families were enumerated and attacked: by naming somebody, by holding a
credential that is not theirs, by acquiring their permissions, through a call
that is supposed to be about yourself, and by reading enough to become them.

Eight of the attacks were not refused. Seven were roster's and one was upstream.

#### Writing a way in for somebody else

The one the audit should have started from, and the plainest:

    Alice may call Identity.Add and Email.Add, and nothing else.
    Alice links her own GitHub account to the administrator's row.
    Alice signs in with GitHub and is served as the administrator.

`Email` had no layer in `server/core` at all, and `Identity.Add` had two
judgements about the *subject* and none about **whose row it is written on**.
The mailbox is the same move one door along: an address of hers on his row, then
`Vouch.Link` at that address.

The rule was written and applied in one place. `vouch.Enrol` says it -- *adding
a way in for somebody is not quite writing their credential, and it is close
enough* -- about a second factor, and it is true of a first. It is
[Core.mayReach] said about the other half of a sign-in, which is what
`mayWriteAWayIn` is.

`ApiKey.Add` is the third door: it checked the key's **methods** and not whose
holder it is minted on. The helpdesk minted a key on the administrator's holder
carrying only `Vouch.Set` -- a method they hold, so the methods check passed --
and the key acts as the administrator, so `mayReach` saw a caller writing their
own credential and the next call set a password they chose.

#### An address is stored as it is looked up

`vouch.byAddress` lowers and trims; the write did neither. So the unique index
on `(tenant_id, address)` -- the whole of what makes an address name one person,
and what closed F7 -- was comparing strings the lookup never compares.
`Someone@Acme.example` and `someone@acme.example` were two rows and one address.

Two failures, and the quiet one is worse than the attack: an address stored as a
provider sent it could not sign in at all. The attack is that the lowered
spelling of somebody's address, written onto your own row, is where their
address resolves from then on.

`front.Address` is the one function both sides use now, beside `Hostname`, which
learned the same lesson.

#### Two more grants that name no role

`TeamMembership.Patch` had no layer: a membership may be added naming no role,
and patching one to name the tenant's admin role is the grant `Add` is refused
for, arriving one verb later.

And the direction argument again, on the last edge that carries a role.
`Granted` reads bindings only, correctly, because a role held in a team is not a
role to bind across the tenant. `mayReach` was asking the same function what the
**target** holds -- where a missing path allows rather than refuses -- so an
administrator provisioned through a team read as holding nothing. `Holding` is
the second answer: everything somebody holds by any path, which is `policy.of`
expressed as grants. The team roles are reported across the tenant, because that
is what the gate will actually let them call.

#### A way into another tenant

`Vouch.Link` read the person through the **unwalled** server. `Verify` does that
deliberately -- there is no frame yet -- and `Link` is not that call: it is made
by an app holding a credential, about somebody it names. So a holder in acme
with `Link` and `Redeem` could name `@hooli/erlich` and be handed a real,
spendable way into another organisation.

Narrowed, that request answers what a request for a stranger answers: the token
that resolves to nobody. Which is the same answer for the same reason -- who is
in another tenant is not this caller's to learn, any more than who is here at
all is.

#### And one that was upstream

A **select** reached a parent that had been erased. `protoc-gen-orm-ent@3843c60`
made a *reference* answer among the live rows; the edge a select asks for went
through no predicate at all, so the parent of any row a caller may read came
back whole. Erasure cascades to nothing on purpose, so an erased person's
address outlives them -- and asking for `select.holder.all` on the way past it
answered their alias, their name, their profile, their provider subject.

Fixed in `protoc-gen-orm-ent@28a0a48` and pinned through payday. Only where a
select asks for the edge: the key-only load `SelectInit` falls back to is what a
recorder reads the row it has just erased through, and narrowing that would take
a trail entry away from the tenant it is about.

#### What the row that outlives them is worth now

An address still says it was once somebody's, to a caller who may already read
every address in that tenant. What it is no longer is a way back to who had it.
Destroying rather than forgetting is an erase that cascades, and that is a
decision about the schema rather than about a read.

### D40 · The third way round escalation prevention, and the shape they share

Found by asking the question the audit should have started from: *what are all
the ways one person becomes another?* -- rather than by finding another instance
of one.

`GroupMembership.Add` named no role and asked nothing. A group is a subject of a
binding exactly as a person is, which is what a group is **for**, and
`policy.of` counts a binding that names one as held by everybody in it. So a
binding written to a group is handed out to whoever joins it, and the membership
is the other half of the write `Binding.Add` already asks about:

    Alice may call GroupMembership.Add and nothing else.
    Alice puts herself in the group the deployment binds its admin role to.
    Alice may now erase anybody.

Two RPCs, and neither of them names a role. The same sentence as the other two
-- *Alice manages who is in what* -- and a permission an administrator grants
without hesitating.

#### The shape all three share

D35 called it "a set of rows, and three readers disagreed about it". With a
third instance the pattern is sharper than that, and worth naming so the fourth
is found before it ships:

> **A grant is any write that changes what the gate will answer for somebody.**

Not "any write that names a role". `Binding.Add` names one; `TeamMembership.Add`
names one; `GroupMembership.Add` names none and grants just as much, because
what it changes is which bindings reach a person. The question to ask of a new
write is not *does this mention permissions* but *does `policy.of` read
differently afterwards* -- and `policy.of` reads bindings by holder, bindings by
group, and roles held in a team. Three sets, and every write that adds a row to
any of them is a grant.

Which also says what is **not**: `SiteMembership.Add` is not, because nothing in
`policy.of` reads it. That is worth having checked rather than assumed.

#### What it needed that the other two did not

A group's grants cannot be read through the generated servers -- `BindingFilter`
carries a `ref` and nothing else, so there is no way to list the bindings of a
group. `Rules` is the seam that exists for exactly this, and it gained a third
answer: `Joining`, which is `Granted` asked from the other end of the same rows.

Each binding is checked at the scope it was made in, so a site administrator may
put somebody into a group bound inside their own site and not into one bound
across the tenant.

#### And removing somebody is still not this

Taking a permission away is a denial of service rather than an escalation, which
is where D26 left `Disable`. Somebody who can remove an administrator from a
group cannot become them.

### D39 · The grace is the process's, not each listener's

`ShutdownGrace` is five seconds and the number that justifies it is somebody
else's: `docker stop` waits ten and then sends SIGKILL, so a longer grace is one
nothing lives to use.

That argument holds for **one** listener. A deployment with a control plane and
an admin port opens five, and each was stopped by a `defer` of its own -- so
five graces ran end to end. Twenty-five seconds against a ten-second budget,
with four of the five never having been asked before the process was killed,
which is the outcome the grace exists to avoid arriving by way of the grace.

They have nothing to say to each other: one listener draining is not a reason
for the next to still be accepting. So they run together and the budget is the
grace, whatever a deployment opened.

The test is written against the loop rather than against a served deployment,
and that is worth saying rather than leaving as an oddity. A test that opens all
five can hold a stream on only one of them without a session cookie -- and only
a listener with a stream in flight waits its grace at all -- so serial and
together both answer in one grace and it discriminates nothing. The end-to-end
test is kept for what it does pin, which is that every listener is stopped and
that opening four more does not leave the process running.

### D38 · Closing a deployment closed one of its two planes

Found by running this suite the way its own CI runs it, which nothing here had
done: `postgres:17` as GitHub hands it out allows a hundred connections, and the
`cmd` package ran out part way through. What came back was `sorry, too many
clients already` against whichever tests happened to be running when the last
connection was taken -- a different set every run, which reads exactly like a
flaky suite and is not one.

`Build` is recursive, so a deployment naming a `control:` plane is two servers
with a database and a pool each. `Close` was one line closing the outer one.

Why it survived: the caller that matters in production calls it once and then
exits, so a leaked pool is a process that was ending anyway. The caller that
does care is a suite, where every test that needs keys, a console or an
operator builds both planes -- and the symptom arrives as somebody else's test
failing.

Which is the argument for the test being about `Close` rather than about the
suite: `cmd/close_test.go` asks the database whether both pools were given
back, because only the database knows and a count kept here would be a second
answer.

### F16 · The redactor was written for one of three recorders -- **fixed upstream**

F15 fixed `Audit.patch`. The declaration is `(payday.field).secret`, what it
bought was a generated `hide<E>` clearing the column out of `value`, and F15
added `hidden` so the same held for the **document** a write was compiled from.

Three recorders sit behind every write and read the same `bare.Change`. The
finding named one and the fix reached one: `watchRecorder` and `outboxRecorder`
went on marshalling `c.Patch` raw. That is fixing the instance rather than the
defect, which is the thing this project keeps deciding not to do.

Nothing carried a verifier off the box. `watchpg` deliberately sends no patch --
its package doc says so and its own test asserts it -- `memory` is this process,
and a `WatchItem` carries the row re-read through each subscriber's narrowing
rather than the document. What was at rest was an `outbox` row holding one until
something drained it, in the same database as the `credential` table it came
from, in a service that declares no methods.

So it was latent rather than open, and the reason to fix it is that none of what
made it latent is a property of **those two lines**. The first broker written
that carries a patch carries verifiers, and nothing would have said so.

`lesomnus/payday@7ff5e8f`, with the queue asked the question the trail was
asked: `internal/apptest/cmd/outboxsecret_test.go` upstream, and
`cmd/outboxsecret_test.go` here, because it is a property of the pinned payday
rather than of anything in roster.

### D48 · Minting an `rt_` over the wire, which was P5's one loose end

The roadmap carried it under P5 for as long as there was a roadmap: *not done,
minting an `rt_` over the wire*. The half that **answers** finished long ago --
a tenant key resolves to its holder, is narrowed by what that person may do, and
`TokenService/Introspect` serves it to product apps -- and the half that issues
was `roster key add`, which is a shell on the box, which is not a thing a
customer has.

#### It is `IssueService`, on the other plane

Not a new service. `IssueService` already exists for exactly this shape -- make
a secret, store what verifies it, answer with it once -- and the reason it
existed only on the control plane was that nothing had asked for the other kind
yet. So the same code is registered on the data plane, and what differs is two
things.

**The prefix**, which is not in the request and cannot be: a caller that could
name one could ask the customer-facing port for a key of the deployment's own
kind, and the prefix is exactly what tells the two apart. It is a fact about
which server answered.

**How a holder is named.** `service` is an alias in the one tenant the control
plane has, and names a holder **into existence** if there is none -- which is
right there, and wrong on a plane with many tenants, where a call that made
somebody by mentioning them is a way to write rows into another customer's
tenant by typo. So the data plane takes a `HolderRef`, the wall narrows it, and
each plane refuses the other's form. Both at once is refused too, the way
`vouch.refOf` refuses a person named two ways.

#### The rules are not written in the issuer, and that is the point

Minting goes through the **walled** server, so `core.ApiKey.Add` runs, and both
of the rules a key is held to are already there:

- *Nobody hands out a method they do not hold.* A key is the most direct grant
  there is -- whoever holds the string calls whatever the column says.
- *Nobody writes a way into an account wider than their own.* A key resolves to
  its holder, so a call made with it is made **as them**. `core/apikey.go`
  records the finding this closes: minting one on the administrator's row
  carrying only a method the minter holds is a credential for the
  administrator, and the methods check alone passes it.

That second rule is the whole reason this can be offered to customers at all,
and it is why the tests for it go over the wire rather than in process -- a
service reaching the ungated server would pass every check by having no frame.

#### What it hands out: nothing

An `rt_` resolves to a person and is narrowed by what that person may do, so a
key is at most a second copy of a credential they already hold, and less, since
it names methods. What it replaces is the shell.

#### One thing it does not do, and the reason is elsewhere

A deployment with no `control:` wires `auth.Plain`, which believes what a caller
writes and reads no token -- so a key minted there is inert. That is the caveat
`auth.Plain` carries everywhere rather than a fact about this service, and
`README.md` and `docs/OPERATING.md` both already say `auth.Plain` is not for
production. It is not refused for the reason `roster key add` refuses without a
control plane: that one has nowhere to **write** the row, and this one always
does.

#### And a duplicate registration, which failed loudly

`GrpcControl` builds on `Grpc`, so registering the customer's issuer there put
two `IssueService`s on the control plane's server and gRPC refused to start.
`Server.Keys` is the flag that tells the planes apart -- it exists because
`Control == nil` is true of the control plane *and* of a deployment that has
none -- and it now decides two services rather than one.

### D47 · The trail gets no new RPC, and the reason is not the one I gave

The plan was `AuditService.Prune` and `Status` on an overlay, and a `List` that
continued out of the archive so a console would not have to ask twice. None of
the three is being built, and the reasons are worth writing down because two of
them are not what was expected.

#### `Prune` — the layer does not stop it, the **grants** do

The argument given first was that the generated layer refuses trail writes. That
was wrong twice over: an overlay declares a new method, so the refusal does not
apply to it, and `core` sits **below** `Audit` in the stack -- so a
`coreAudit.Prune` calling `s.AuditServiceServer.Erase` reaches bare's hard
delete without meeting the refusal at all.

The real reason is `cmd/policy.go`. Methods are matched by **pattern**, through
`frame.Covers`, which honours `*` in the package, service and method positions.
So a role or key holding `/roster.*/*` -- and `roster init` writes exactly that
-- picks up a new method **the moment it is generated, with nobody deciding**.
An API key skips `May` entirely and is narrowed only by its own list.

Which turns *an API key that prunes is a stolen key that prunes* from a phrase
into a mechanism: adding the method grants it retroactively to every wildcard
already issued. A shell on the box is a credential nothing steals over the wire,
and it stays the only door.

#### `List` over the archive — there is no index, and slow is worse than absent

An archive is gzipped files, sorted by month and kind and by nothing else. Any
question narrower than *give me this month* is a full scan of the files in
range. So a `List` that continued past the retention boundary would be a call
that answers in milliseconds until the day it crosses, and then in seconds --
unpredictably, depending on how much archive a deployment has kept.

A console screen that is sometimes slow for reasons the caller cannot see is
worse than one that says *the database holds back to May; before that is in the
archive*. If this is wanted later, what it needs first is an index beside each
archive, and that is a thing to build when somebody has the screen open.

#### `Status` — nothing reads it

`ts/src` does not mention the trail; the console has never read it. An RPC with
no consumer is a shape guessed before the page that would use it, which is the
thing PLAN.md keeps deciding not to do. When the screen exists it will say what
it needs.

### F17 · A deployment key reads every tenant's whole history -- **by design, and now written down**

`cmd.Policy.Where` answers `frame.Everything` for an `rk_` key, deliberately: *a
key belongs to the deployment and the deployment is every tenant in it. What
narrows it is its methods, not its tenants.* So a key allowed
`/roster.HolderService/List` reads every customer's people, which is what a
service that manages customers is for.

`AuditService` is the same property at a different magnitude, and that is the
half nobody had noted. `Audit.value` is the row as each write left it, so one
method answers **every table's contents, in every tenant, across all time** --
including rows long since deleted, since nothing erases a trail row. It is the
single widest read this app has, and no role reaches it that way: a person is
walled to their own tenant, so the same method asked by a holder is a different
question.

Not a defect, so not fixed. What was wrong was that it was undiscoverable:
`cmd/trailkey_test.go` asserts it in both directions, so narrowing it is a
failing test somebody has to think about, and `roster key add` says it once when
a key's methods reach the trail. Said rather than refused -- a compliance
exporter is a real service and this is the method it needs; what is wrong is
granting it by reaching for `*` and not noticing.

### D46 · An erase makes somebody unreachable; forgetting them destroys something

Nothing in roster destroyed anything about a person, and *"we deleted them"* was
not true of any call it served.

`Holder.Erase` writes `date_erased` and the version beside it and stops. Nothing
cascades -- that was decided and is right -- so the row keeps the alias, the
name and the profile, the addresses and the external identities keep theirs, and
the trail holds a copy of all of it **including the copy the erase itself
wrote**, since `Audit.value` is the row as the event left it. Erasing somebody
is the one act that puts their whole record into the trail one more time.

Which is the correct shape for *this person has left*. It is the wrong one for
*destroy what you hold about them*, and there was nothing else.

#### Two triggers, one act

A **request** -- somebody exercising a right -- has no grace. They asked, and
the clock a regulator counts is already running: GDPR Article 12(3) gives a
month, and 개인정보보호법's 시행령 reads *지체 없이* as five days. A window inside
that is risk bought with nothing.

An **account closing** has one, and the window is **operational rather than
legal**: a mistaken deletion, a compromised account deleting things, a billing
dispute. Thirty days is the ordinary answer across the industry and it fits
inside the month. `holder.forget_after`.

They end in the same place and differ only in when, which is why they are one
function with two callers.

#### The window is a grace only because of `restore`

roster could not undo an erase at all. `HolderPatchRequest` carries no
`date_erased`, deliberately -- a caller who could write it could un-erase
anybody -- so there was no path back through any server.

Without one, *thirty days and then destruction* is a delay rather than a grace,
and every reason the window exists is a reason that needs the mistake to be
reversible. `roster restore` is that, through ent, for the same reason
everything else here is: a shell on the box is the credential, and it is the one
nothing steals over the wire.

It refuses somebody already forgotten, and what answers that is the row itself
-- a forgotten holder has no alias, because that is the first thing taken. Not a
column, because a column would be a second answer that a row written before it
existed gets wrong, and nothing it could read is more honest than *there is no
name here any more*.

#### What is destroyed, and the thing that pulls against it

Everything that exists to say *this person reaches here, signs in there, holds
this* goes: addresses, external identities, verifiers, keys, sessions, attempts,
links, and the rows that say what they may do. Those are meaningless without the
person, and a destroyed person holding permissions is a row waiting to be a
surprise -- one of the twelve foreign keys into `holder` is `SET NULL`, so
leaving the bindings would leave one pointing at nobody.

The `Holder` row **stays, blank**. Its identifier is `Audit.actor_id` and twelve
foreign keys point at it, and what makes it personal data is that it *resolves*.
Emptied of the columns that name somebody it is a stable pseudonym reaching
nothing, which is what a trail wants: *the same someone did these fourteen
things*, with no way to say who.

And the trail keeps its events and loses its contents. Both halves matter and
they pull against each other: a version that destroyed the rows would let
somebody erase the evidence of what was done **to** them by asking to be
forgotten. `Audit.value` and `Audit.patch` are blanked; the actor, the action,
the object and the time stay. That is what a legal-obligation exemption is an
exemption *for*.

#### It reaches the archive, and that is the one place an archive is edited

Not reaching it would destroy the copy an operator can see and leave the copy on
the disk beside it -- not a gap in a policy, an answer that is wrong in the
direction that matters. `Purge` destroys whole files and `Archive` only appends,
precisely so that nothing rewrites one; this is the exception, written beside
itself, synced, and renamed over.

#### And the set of objects is wider than the person

`Audit.value` for a write to an `Email` row is the address, and that row's
`object_id` is the email's rather than the person's. A pass that named only the
holder would blank the record of the person and leave every address they ever
had in the trail beside it. So every identifier destroyed is collected on the
way past and named to the redactor.

`server/forget`, `cmd/forget.go`, and payday's half in `lesomnus/payday@63ee846`.

### D45 · The retention is payday's, and it is per kind of thing

D44 built the retention policy in roster, and both halves of that were wrong.

#### It belonged upstream, and the question should have come first

The `Audit` entity is payday's, the recorder that fills it is payday's, and the
layer that refuses to let anybody write to it is payday's. Every app on payday
gets that table and every one of them has it grow forever -- so an answer
written here is a format the next app cannot read, a sweep with its own bugs,
and for the app that never gets round to it, the deployment's largest table and
an obligation nobody has met.

roster's own configuration says the rule: *the framework cannot own this struct,
since what an app is configured with is the app's. What it owns is the pieces:
each of these is a payday type, and what is written here is only which of them
this app has.* `cmd.AuditConfig` was a roster type, and that was the tell.

The split follows the one `internal/pdgen/outbox.go` already drew about the
drain: *generated rather than written in the runtime because `ent.Client` and the
predicates are the app's types -- and what is not generated is any judgement.*
So `pd.TrailStore` reads a batch of these kinds older than this and forgets the
ones it is told, and every decision is in `trail`. The document between the two
halves is protojson, which is the archive's format anyway; payday has no Go type
for its own `Audit`, because the schema is copied into each app and generated
there.

#### And one clock over the table was the wrong shape

A deployment's obligations are not uniform across its entities, and the two ends
pull in opposite directions. What was done to a **person** is under a privacy
regime: it has a stated limit and eventually has to stop existing. What a
**machine** did is an operating record -- who drove it, which route, when the
fault was logged -- and the requirement there is usually that it never be lost.
A single clock forces the shorter of the two onto everything, and there is no
global answer that is honest for both.

So the policy names kinds and a kind with nothing said about it gets the
default. Which required a column.

#### `Audit.domain`, and the note that said it was not needed

`object_id` carries the kind already -- a `pdid` holds its domain -- and the
note beside it said a column was therefore unnecessary. That note was right
about the question it was answering. *What kind was this row about* is answered
by reading the row. *Which rows were about robots* is a query over a set, and no
database indexes into byte nine of a `uuid`.

The second question is what a retention policy is made of, and it was the case
the note was silent about. Indexed with `date_created`, because a policy that
keeps machine records and sweeps people's would otherwise scan the whole table
on every pass.

Defaulted, too, and that was found by breaking it: roster's admin port composes
a trail row by hand through ent, and a required column is one it learns about
from a runtime refusal. `counterpart_tenant_id` predicted that in as many words,
one field down, about itself.

#### Profiles, because a number nobody can trace is a number nobody will change

`61320h` is unreadable and *seven years, because 17 CFR 210.2-06 says seven
years* is a thing a reviewer can disagree with. `trail.Profiles` carries pci,
hipaa, sox, pipa, pipa-sensitive, gdpr and forever, each with the sentence its
number comes from -- and says out loud that it is a starting point and not a
guarantee, since what a deployment must keep depends on what it processes and
for whom.

#### And the command applies the policy rather than a cutoff

`roster trail prune` with no window is one pass of what `audit:` says, per kind.
The version that insisted on a cutoff made `--older-than 1ns` the obvious thing
to type, which destroys the very kind the configuration says to keep forever --
a footgun pointed at the one thing this exists to protect.

`lesomnus/payday@607b7ac`, `@eaec815`, `@ca0e618`, `@db9e366`.

### D44 · The trail's retention is two clocks, and neither of them is an RPC

`audit.proto` asked for this in as many words and the sentence read as a caveat
rather than as a task: *the trail outlives what it names, so a softly erased
row's contents live on here. An app with an obligation to destroy data has to
reckon with the trail, and the answer is a retention policy rather than an empty
column.* There was none. So the policy roster had was **forever**, arrived at by
not deciding, on the one table that never stops growing.

#### Why one number would have been the wrong shape

*Delete after N* answers the operational question -- what the console can show,
what a query costs, how big the disk is -- and gets the obligation exactly
backwards. The window somebody wants in the hot table is months; the window they
are required to be able to produce a record over is years. A single number is
either a database nobody can afford or a record that is gone too early.

So it is two: `retain` is how long a row stays in the database, `destroy` is how
long the record exists at all, and `archive` is where a row lives out the
difference -- one gzipped file per month, protojson, readable by anything that
can read an `Audit`.

#### Forever is the default, and the blank field is refused

Both clocks are empty unless a deployment sets them, because the alternative is
a version upgrade deciding how long somebody's evidence lasts.

And `retain` with no `archive` is refused at startup rather than obeyed. That
configuration *works*: the sweep runs, the table stops growing, and every graph
an operator watches improves. What it is doing is destroying the trail, and the
day it is discovered is the day somebody asks for a record. `discard: true` is
how a deployment says it means it -- its own setting rather than an empty
string, because *I have not configured where* and *I do not want one* are two
different states that look alike.

#### Written before deleted, and deleted by identifier

The pair is one act. Two commands, or one command with the export optional, is a
deployment that exports and forgets to delete or deletes without having
exported, and only one of those two is ever noticed.

#### And the file a run writes is its own, which took a second pass

The archive was one file per month, appended to, because concatenated gzip
members are a valid stream and so a file need never be rewritten. That is true
of one writer, and there is not one writer: `trail.Sweep` takes no lock -- nor
does the generated outbox drain, whose comment says *nothing here takes a lock,
so two of these drain the same rows*. For the drain, two replicas is wasted
work. Here a `gzip.Writer` flushes to the file in chunks of its own choosing, so
what two writers interleave is not two members but the inside of one, and the
month stops being a gzip stream at all.

A run writes its own files -- `audit-2026-08.<run>.jsonl.gz` -- so writers never
share one, and each member is built whole and written in a single call beside
that. The month stays first in the name so a destructive pass still reads it
without opening anything. What it costs is more files and the duplicate rows two
writers leave, which `Read` already dropped because a crash between the sync and
the delete could always produce them -- it now drops them **across** files
rather than within one.

#### The delete, and why it is by identifier

The delete names the rows that are in the file rather than re-running
`date_created < before`. A second query matches whatever is true when it runs: a
row backdated by a clock that stepped, or written by a replica whose idea of now
is behind, is a row the second query removes and the file does not have. What is
left is a crash between the sync and the delete, which leaves rows in **both**
places -- the direction to fail in, and `Read` drops the duplicate by
identifier.

#### And why none of it is an RPC

The layer in front of `AuditService` refuses every write -- *"the trail is
written by what happened, not by anybody asking"* -- and a retention RPC beside
it would be the exception that makes that sentence false. What a trail is worth
is exactly that the credential which lets somebody act is not the credential
that lets them erase the record of having acted. An API key that prunes is a
stolen key that prunes.

So both doors need the database: `roster trail` at a shell, and `serve` applying
the policy itself. The second one matters as much as the first -- a policy that
runs when somebody remembers to run it is not a policy -- and it is the sweep in
this app that is a **mechanism** rather than a tidy-up. The other two collect
rows that are already refused, so an outage of either costs disk. An outage of
this one is a deployment keeping records it said it would not.

#### What is left, and it is a decision rather than work

Erasing **one person** from the trail. This policy is about age and reaches
everybody at once; a right-to-erasure request is about a subject, and what it
should blank is not obvious -- the contents of writes about them, their
identifier as an actor, or both. Blanking the actor destroys *who did this*,
which is what the trail is for; keeping it keeps an identifier that is itself
personal data.

The two answers in the field are to null the contents and keep the event -- the
same distinction `audit.proto` already draws for the hard erase, *the record of
the destruction and the record of the contents are different rows* -- or to
encrypt per subject and destroy the key, which is what deployments with archives
in several places do because they cannot go and find them all. Not chosen here,
because it is the app's obligation that decides and roster does not know what
that is yet.

### D43 · A second factor is not a way in

Two places asked *can this person sign in* and both counted a TOTP seed as a
yes. Neither was wrong about its own arithmetic; they were asking about the
wrong set.

`server/core` refuses to remove somebody's last way in by counting their
identities and their credentials, and a seed is a credential -- so a person with
one provider and one seed could have the provider unlinked. The count said one
was left. What was left was six digits nobody may be asked for until they have
already said who they are.

`server/vouch` reached the same state from the front. `Verify` takes a kind and
checks it, and `answer` sets `ok` when there is nothing left to prove -- so for
somebody whose only credential is a seed there was never anything left, and a
six-digit code inside a thirty-second window was a whole sign-in. The account
was one `Enrol` old, and the call that confirmed the enrolment was the call that
let them in.

The second one is the hole and it is worth saying why it hid. `ok` is set from
the **absence** of work outstanding, which is exactly the shape D21 chose to
keep an app that has never heard of second factors failing closed. The emptier
an account was, the more finished its sign-in looked.

So it is one sentence, in `vouch.Begins`, and both sides ask it: a password can
be the first thing somebody proves, and so can a link; a second factor cannot,
because it is what is asked *after* one. A sign-in is over when there is nothing
outstanding **and** something that could have begun one has been passed.

#### Why it is the kind and not the row

Nothing is written down about it. What a kind *is* is settled by what
`server/vouch` can do with it -- the same argument `settable` makes about which
kinds may be stored at all -- and a column would be a second answer, one that
every row written before the question existed gets wrong.

#### Where it deliberately does not apply

`Verify` has a branch for a caller with no frame: it cannot mint a continuation,
so it answers `ok` to a first factor without ever asking for a second. That
branch is `init` and the sandbox, and its `ok` already means *the secret
matched* rather than *the sign-in is finished* -- so a rule about which factor
finishes one has nothing to decide there. Nothing in a deployment reaches it:
every caller that gets that far holds a key or a certificate, and so carries an
actor.

It is also what keeps the enrolment ritual working. Confirming a freshly
enrolled seed is a `Verify` of kind `totp`, which for a person who has a
password answers with a continuation naming it -- and for a person who has
nothing else is now refused, which is the case this decision is about.

#### What was not done

`Enrol` still writes a seed for somebody with no first factor, and that row is
inert: nothing can finish a sign-in with it, and `server/core` no longer counts
it. Refusing it outright was considered and left alone, because the person it
would refuse is the one who signs in at an external provider -- roster never
sees that first factor, and a rule stated here would be a rule about a fact this
app does not hold.

### D42 · An optimistic lock is a write, and the last way in is closed

D37 recorded the last-way-in race as *found and not fixed*, on the reasoning
that nothing this layer can reach serialises two callers: a transaction is not
enough under READ COMMITTED, and `SELECT ... FOR UPDATE` is not something a
generated read offers. That was right about the transaction and wrong about the
conclusion.

The schema already carries the thing needed. Every entity has `date_updated` as
its version, and a version is a compare-and-swap -- what it was missing was not
a primitive but a **scope**: the count, the erase and the swap have to be one
commit, or the swap validates a moment the other two do not share.

So `coreIdentity.Erase` opens a transaction, writes the person's version, then
counts and erases inside it. The second caller blocks on that row, and by the
time it is let through the first has committed and the count it takes is the
true one. Forty people unlinked twice at once: forty kept a way in, on
PostgreSQL, where the same test lost thirty-nine before.

#### Where the first attempt went, which is worth keeping

The obvious write was the generated `Patch` with a version precondition and no
fields. It compiles to an **existence check and no write** -- which is D34's
finding, about a continuation, arriving in a second place a year later. Two
callers each validated a version, neither wrote anything, and they contended for
nothing. It passed on SQLite, which serialises writers anyway, and changed
nothing at all on PostgreSQL.

An optimistic lock is a write. A precondition with no write beside it is a read
with an opinion.

#### What it took, and what it cost

`Core` gains the driver it is built on and a `Lock` -- a write on somebody's own
row that nothing asked for. The write is `cmd`'s, against ent, for the reason
`Rules` is: `server/core` holds no client, and this file already reads ent
directly because working out what a caller may do cannot itself require
permission. Taking a lock is not a write anybody asked for either, so it is not
one the layers should record or narrow.

The cost is that a person's `date_updated` moves when an identity of theirs is
removed. It is a token rather than a fact, the trail carries the erase that
explains it and nothing for the lock, and a console holding an older version is
told to read again -- which is what a version is for.

A stack with no driver -- a batch, or anything payday rebound onto its own
transaction -- runs the rule in what it was handed, because the outer
transaction is already the serialisation point.

### F15 · `secret:` kept a column out of one of the trail's two records -- **fixed upstream**

Found by asking what rules `Audit` is under, which was the fourth of the
pending decisions and turned out not to be a decision.

`Audit` holds a write twice. `value` is the row as the write left it, and the
generated `hide<E>` clears the declared columns before it is marshalled --
which is what `TestNoVerifierReachesTheTrail` has been asserting since it was
written. `patch` is the same write from the other end, the document it was
compiled from, and nothing touched it. That test checked one column.

So `Vouch.Set` -- the RPC whose whole job is to take a secret in and never hand
one back -- wrote the argon2id string into `Audit.patch` in full. And the trail
is **served**: `AuditService` is generated like any other and the wall files a
credential's row under its person's tenant, so anybody in that tenant whose role
reaches the trail could read the password hash of everybody in it. Which is the
read D13 unregistered `CredentialService` to prevent, arriving by the road
nobody had looked down, into the one table nothing erases.

Fixed in `lesomnus/payday@312ccbf`: the recorder drops the entries naming a
declared field before the document is held. Entries rather than values, because
a patch entry with its value removed is a document that no longer applies -- and
what is kept is every other entry of the same write, with `value` saying what
the row became.

The part worth remembering is how it was nearly missed twice. A patch entry says
where it applies as either a `path` or a list of `targets`, and a generated
`Patch` writes the second even for one field -- so the first filter read `path`,
looked correct, and let everything through. The test that caught it is in
payday's reference app, which is where a claim about generated code belongs.

`cmd/trailsecret_test.go` is roster asking the same question of its own trail,
in both columns and against the encoded shape rather than only this deployment's
parameters.

#### And what it says about the pending question

`watch.outbox` was on the list as *unredacted patch documents, byte-identical to
what `Audit.patch` already holds*. That was true and the wrong way round: the
outbox is drained and deleted, and `Audit` is the table nothing erases. The
question was never the second copy. `Audit` has **no retention policy**, which
`audit.proto` says in as many words -- *an app with an obligation to destroy data
has to reckon with the trail, and the answer is a retention policy rather than an
empty column* -- and that is the decision still open.

It is open no longer; D44 is what came of it. What is still open is the half
that is genuinely a decision rather than work: erasing **one person** from the
trail, where D44 says why the obvious version is not obvious.

### F14 · A select reached a parent that had been erased -- **fixed upstream**

F9 made a **reference** answer among the live rows, because a reference to a row
is composed into the reference of whatever names one and no narrowing of the
parent is applied there. A **select** is the other way to reach a parent, and it
went through nothing at all: the edge is loaded with the target's own `Select`
and no predicate.

So the parent of any row a caller may read came back whole, whatever state it
was in. An erase cascades to nothing on purpose -- an address and an external
identity outlive the person -- and asking for `select.holder.all` on the way
past one of those answered their alias, their name, their profile, their
provider subject: everything the entity's own `Get` answers NotFound to, for the
same caller, one call later. And a list needs no name to start from.

It is the parent's **liveness** and not the caller's scope, which is why it is
the generator's and not the wall's: a wall narrows the child's path to a tenant
and has nothing to say about whether the row at the other end of an edge is
still there.

Fixed in `protoc-gen-orm-ent@28a0a48`, pinned through `lesomnus/payday@dbe36f0`.
Only where a select asks for the edge: the key-only load `SelectInit` falls back
to is what payday's recorder reads a row it has just erased through, and
narrowing that would take a trail entry away from the tenant it is about --
which is F13, immediately above.

### F13 · The record of an erase was filed where the erased side could not read it -- **fixed upstream**

The trail is filed under the tenant of the **thing that changed**, which is
what makes it a trail two parties can read: whoever holds the row can read what
was done to it. That is the reason payday's recorder pays a read on the write
path rather than filing everything under whoever asked.

It did not hold for an erase, and roster is a deployment where that is *every*
erase -- nothing here declares `hard:`. The recorder reads the row it is
recording through the bare server, and every read that server makes narrows to
the rows still here. So the row a soft erase had just stamped was NotFound to
the record of that very erase, and the record took the path meant for a row
that is really gone: the **actor's** tenant, and an empty value.

Exactly the wrong way round. An operator erasing somebody in a customer's
tenant left a record their own tenant could read and the customer's could not
-- the party with the strongest claim to know their person was removed is the
one it was hidden from.

Two more of the same kind came with it, and roster is exposed to one:

- **A nil `[]byte` in a NOT NULL column.** SQL NULL to pgx, an empty blob to
  SQLite's driver -- so a write that passes on the database the tests run on is
  refused by the one the app is deployed on. The trail has three such columns.
- **A message-typed field on an entity** stored as `{}`, because the generated
  column is `encoding/json` and an opaque-API message has no exported fields.
  roster has no such field today, which is the only reason it is not here.

Fixed in `lesomnus/payday@f1f9321`, pin moved. `cmd/erasetrail_test.go` is
roster asking whether it arrived -- worth its own file because the answer is a
property of the pin rather than of anything here, and because the shape it
asserts (a record filed away from its own tenant) is one no assertion about
counts or contents would have caught.

Beside the fix came the thing that finds this class at a desk:
`scripts/with-postgres.sh` in payday, and a CI job that runs both of its
modules on PostgreSQL. roster's own version of that sentence is in
`docs/OPERATING.md`.

### D37 · What a review of every document against the code turned up

Every document was read against the code rather than from memory, and every
finding was then argued against by somebody trying to refute it. Most of what
came back was prose that had been true when it was written. What was not is
here, because each of these is a rule that already existed and a place it did
not reach.

#### The wall around a port is not the wall around a deployment

The admin listener builds its own `VouchService` and its own chain, and three
of the differences from the data plane's were omissions rather than decisions.

It was built without `WithBreached`, so the corpus of leaked passwords -- which
`OPERATING.md` calls a deployment-wide refusal -- was enforced on the port a
customer reaches and not on the one an operator resets passwords from. The two
options are not the same kind of thing and that is why the second was forgotten:
`WithReach` reads the caller's bindings and genuinely does not cross between the
planes, while the corpus answers a question about the value before anybody is
read.

Its `Intent` recorded a write on the control plane by matching the four
generated verb suffixes, so every `VouchService` write served on that very port
-- `Set`, `Reset`, `Unlock`, `Revoke`, `Link`, `Enrol` -- and every `Holder`
overlay write left one trail instead of two. Named the **reads** instead and
everything else recorded, which is the direction `register` in `serve.go`
already fails in.

And `admin.limit` was read by nothing. Every other knob of that block is.

#### A rate that counted half the calls, twice over

Both chains wrote `LimitUnary` alone, so `Watch` was the way past whatever a
deployment configured: one call to open, nothing counted however long it ran or
however many were opened.

Fixing it found the second half. `ServerConfig.Limiter()` **builds** a bucket,
so a chain that called it once per interceptor would have had two limits with
the same numbers on them, neither counting what the other let through -- a rate
of n per second answering 2n, with nothing in the configuration to see. One
limiter, handed to both.

#### The one credential a suspension did not reach

D26's table says `date_disabled` is enforced at `cmd.Resolver`, *where every
credential that resolves to a holder arrives*. Every credential arriving **at
roster**: a product app is handed the `rt_` and asks `TokenService/Introspect`,
carrying its own key, so the person's credential is a string in a request body
that never goes near the resolver. Suspending somebody stopped them signing in
and left them working everywhere else until the token expired, which for a key
is possibly never. Read in `keys.findKey` now, where both answers this package
gives are built.

#### And the ones that were about a refusal costing what it should

`Verify` resolved an address before it decided whether it could check the kind
at all, so *a fact about the deployment* became a fact about whether the address
exists: an unknown kind answered InvalidArgument for one that does and `no()`
for one that does not, and `totp` on a keyring-less deployment answered
Unimplemented against the same nothing. The burn for a missing address was the
package argon2 rather than the kind's, which is the inversion `kind.go` exists
to close, reintroduced one branch earlier.

`Set` wrote whatever `kind` it was handed. Nothing else in the service does, and
a phantom kind is offered by `factors` to every framed sign-in from then on,
refused by `Continue`, and unremovable -- `CredentialService` is unregistered.

#### The half-session, and what its clock is about

`Config.Half` was written down and never read: the only thing ending a
half-session was the browser's own cookie expiry. Fixing that by teaching
`held.take` the clock broke the other caller -- `SignOut` takes the entry in
order to **revoke** what is in it, and an expired entry is the one that most
needs revoking, since `expires` is this app's hold on a browser and not roster's
on a credential. The clock belongs to the second form, which is spending a
string roster is holding anyway. Two functions, and the one that says which is
which is the name.

The same call also removed a signed-in browser's delegation, because both live
in one map under one key: one stray POST and somebody's session could act for
nobody, with a credential still live in roster and nothing holding the
reference.

#### Two answers to one question, three times

A `Tenant` lookup that failed for any reason answered 401, so a roster that was
down read as a wrong password -- which `frontdoor.js` warns about in as many
words for the call one further on. `ErrUnknownHost` is the only one a person is
told no for.

`Hostname` was two functions, roster's and the reference app's, and they
disagreed about a bracketed literal with no port: `[::1]` here, `::1` there, so
the lookup missed and the page said nobody is there. It was also cutting every
unbracketed literal at its last colon -- the guard for *too many colons* looked
at the part after it, which the last colon guarantees has none -- so `fe80::1`
was stored as `fe80:`. Both are `net.SplitHostPort` now, which holds every one
of those rules, and the app asks roster what a name is.

`control.watch` inherited the data plane's broker by **replacing the whole
block**, so `control.watch.outbox: true` beside an inherited broker was loaded,
listed by `roster config env`, and dropped. And `watch.outbox` with
`broker: none` wrote a row inside every transaction that nothing would ever
publish or delete, in a table `OutboxService` answers no RPC for -- refused at
startup now, in one place rather than in the drain's condition as well.

#### Stopping

`main` handled `os.Interrupt` alone. `docker stop` and every orchestrator send
SIGTERM, and the image runs `exec roster serve`, which makes roster PID 1 --
where SIGTERM has no default handler at all. So the graceful path was written,
wired, and never once executed in the way this app is deployed, and every
routine restart was a crash.

`GracefulStop` waits for every RPC in flight and a `Watch` never ends on its
own, so one product app holding the sync channel -- which is what item 4 tells
a product app to do -- meant the process had to be killed. Five seconds and then
`Stop`, which is what unblocks the graceful call rather than racing it.

And `serve` checked one plane's schema. `control.db.migrate` was listed by
`config env`, set by `compose.yaml`, promised by `OPERATING.md` and read by
nothing -- so an upgrade past a release that adds a control-plane table started,
said nothing, and was first reported as an operator who could not sign in.

#### What was found and not fixed

Two callers unlinking a person's last two identities at once both count before
either writes, and the person is left with no way in. Nothing this layer can
reach serialises them: a transaction is necessary and not sufficient under READ
COMMITTED, and the lock that would be -- `SELECT ... FOR UPDATE` -- is not
something a generated read offers. Writing to the `Holder` inside the
transaction would take the same lock and is worse than the disease. It takes two
calls at once about one account from a caller who is almost always the person
themselves, and an operator's `Vouch.Reset` is a way back in.

#### And how the fixes themselves were reviewed

Each group of fixes was written by one reader and then attacked by another,
whose only job was to refute it. That found a security regression before it
landed -- a `Reset` by address that changed the password and skipped D26's
invalidation, leaving every session a takeover had opened alive -- a test
asserting a count that a soft erase does not change, a skipped test standing in
for a fix, and a timing assertion that would have flaked against a remote
database. None of those would have been found by running the suite.

### D35 · Escalation prevention is a set of rows, and three readers disagreed about it

`escalate.go` states one rule -- *what you grant must be a subset of what you
hold* -- and the four writes that name a role were the surface it was applied
to. Two ways round it survived, and neither is a hole in the rule: both are a
disagreement about **what somebody holds**, between readers of the same rows.

#### Attaching a role is granting it

`TeamMembership.Add` names a role and never asked. It looked like the wrong
place to ask, because a team is not a scope this rule compares -- and the gate
had already decided otherwise: `policy.of` unions the methods of a role held in
a team into the set it answers from, deliberately, because the gate is
outermost and never sees which team a call is about (D17).

So the two RPCs `escalate.go` opens with, one service along:

    Alice may call TeamMembership.Add and nothing else.
    Alice attaches the tenant's admin role to herself, in any team.
    Alice may now erase anybody.

From "Alice manages who is in what" -- the same sentence that made
`Binding.Add` dangerous. The scope it is granted at is the team's **site**,
which is a scope the rule already compares; a team with no site is the tenant's.

#### A permission held through a group was a permission held by nobody

`Granted` queried the holder edge alone. `policy.of` walks group memberships
too, and `cmd/policy_test.go` has pinned that a group carries a binding since
it was written -- so the gate and this rule answered differently about the same
person.

Which direction that breaks in is the whole of why it is here. In `mayGrant` it
reads what the *caller* holds, and missing a path only refuses a grant somebody
could have made -- the conversation `escalate.go` says it is willing to have.
In `mayReach` it reads what the **target** holds and allows the write when that
is nothing:

    Ops may call Holder.List and nothing else.
    Ops resets the password of an administrator provisioned by a group.
    Ops signs in as them.

The conservative failure and the silent one are the same blindness read in two
directions, which is the argument for the fix being *one query* rather than a
second answer: `bindingsReaching` is now what the gate, `Holds` and `Granted`
all read.

#### Why it was not found by the tests that exist

Every escalation test binds directly. `TestAGroupCarriesItToo` proves a group
carries a binding **to the gate**, and nothing asked the same question of the
rule that guards credentials -- so both halves were tested and the disagreement
between them was not. `cmd/escalate_test.go` is that question, in both
directions.

### D34 · Single-use is what the row says, and the row has to be able to say it

`Continuation` and `Link` are spent by erasing them, and both places said so:
*single use, and used is not there*; *it is safe here because a continuation is
single-use*. Neither was true under concurrency.

#### What happened, measured rather than reasoned about

Thirty-two callers presenting one continuation with the same correct code: all
read the live row, all compared against the same credential, and all went on to
mint. **Up to twenty-four independently revocable credentials from one proof**,
each surviving revocation of the others. Links the same, and the interleaving
there is not exotic -- a mail client fetching a link to preview it, while the
person clicks it.

It reproduced on Postgres and never on SQLite, where a second writer gets
`database is locked` and dies leaving exactly one winner. The suite is SQLite
unless `PDTEST_POSTGRES` says otherwise, and `pdtest.DB`'s own sibling comment
names that as *the direction that hides a mistake*.

#### Why no amount of care here could have fixed it

Spending is an `Erase`, and `Erase` answered `Empty`. The database was already
deciding correctly -- one UPDATE narrowed by `date_erased IS NULL` matches once
-- and then threw the answer away. Every loser was told exactly what the winner
was told.

Nor could roster have compared-and-swapped instead: a `Patch` carrying only a
version test compiles to an `Exist` check and no write, so two callers both see
the row and both proceed. `Continuation` has no mutable column to swap on, and
adding one would have been a second way of saying erased.

So payday was in the way, and the fix is upstream in two parts
(`protoc-gen-orm-service@efff3ac`, `protoc-gen-orm-ent@f892843`):

- **`Erase` reports what it did.** `<E>EraseResponse{bool erased = 1}`, from the
  same `n` the trail is written from. It cannot be said by failing, because
  erasing what is not there has to succeed -- `keys.Undelegate` depends on that,
  and so does anybody cancelling something that may already be cancelled.
- **`Erase` stopped dropping its narrowing.** It read the row's id to name it in
  the trail and then *replaced* the predicate with `IDEQ(v)`, discarding both
  liveness and scope -- so two concurrent erases of one row both matched and both
  recorded a Change saying they had erased it. One row, two entries in a trail.

Wire-compatible: `Empty` has no fields, so an older client decodes the new
response as an `Empty` carrying one unknown field.

#### And here, the losers get `no()`

Not an error. It is what every other unusable handle already gets -- one that
expired, one that was spent, one somebody else was issued -- and telling them
apart would say whether a string was ever a real attempt.

Only the mint is guarded. Everything above it in `step` is a refusal, where
losing the race changes nothing; below it, two winners are two credentials.

### D33 · roster is stateless, and the one exception now has an answer

Asked directly -- can this be scaled horizontally, and is the state
externalised? -- and answered by reading every place a process could be holding
something, rather than from memory.

#### The answer is yes, with one exception, and the exception is `Watch`

Everything durable is a row, re-read on the request that needs it. Sessions
(`server/session`, behind `authsession.Store`), API keys and bearer tokens
(`keys.Store`, behind `auth.TokenStore`), delegations, failure counts and
lockouts (columns, with `DateUpdated` as a CAS precondition), the TOTP replay
window (`last_step`), continuations and links. Nothing in `cmd/` or `server/`
writes to local disk. The sweeps are idempotent deletes, so every replica
running them is harmless.

What a process holds is: the watch broker, the rate limiter's buckets, and
read-only material derived from configuration -- the TOTP keyring, the path to
the breached-password corpus. Only the first of those is authoritative.

**With `broker: memory`, a client watching one replica never hears about a write
that landed on another**, and nothing reports it: the stream stays open and
looks healthy. That is the whole of what does not scale.

#### Which is a payday seam that was half open, and is now open

`watch.Broker` was always an interface, and that was called the seam. An app
could **implement** one and could not **select** it: the configuration was a
closed switch over the two names payday ships. So `config.RegisterBroker`, in
`RegisterDriver`'s shape, because a broker is a client for something that has to
be linked in.

And `broker: none` -- documented as *serves no Watch at all* -- served one that
sent a snapshot and then never spoke, because `watch.New` answers with a non-nil
`*Watch` whichever broker it was given and the generated `if s.w == nil` guard
therefore never fired. It refuses outright now. Both in
`lesomnus/payday@9d614cb`.

#### And one of roster's own, which is the one worth remembering

The control plane's broker was **`memory` written into the code**, not read from
anywhere. payday goes to some trouble to stop exactly this: `watch.broker` has
no default and a configuration that omits it is refused, so that scaling to two
replicas means reading a line and deciding about it. A literal is that line
deleted -- and it made the console the one screen a second replica broke
silently, since a key issued on one process would never reach an operator
watching on another.

It is `control.watch` now, empty taking the data plane's broker name. Still two
brokers: one publishing into the other would have a key being issued look like a
person changing, to every client watching, and a client cannot tell them apart.

#### An outbox is not the answer, and looks like one

`pd.Drain` publishes into the broker it was **given** and then deletes the rows
it read. So with several replicas draining one queue, whichever gets there first
tells its own subscribers and nobody else's. It buys durability across a crash
between the commit and the publish. It does not carry an event to another
process, and reaching for it to solve this would leave the same silence with a
table underneath it.

#### And then the broker was written, because it needed nothing

`watch.broker: postgres` -- `config/brokerpg`, `LISTEN`/`NOTIFY` on **the
database the rows are already in**. Nothing to store, since a notification
reaches whoever is listening and is then forgotten; no address, since it is the
DSN the app already has; nothing to run, since this deployment is on PostgreSQL.

Which is why it was the one to write first. Every other broker worth having is a
message bus somebody has to stand up, and the seam is open for those. This one
turns *scaling out* from a piece of infrastructure into a line of configuration,
for exactly the deployments roster is aimed at.

Three decisions inside it, all in `watch/watchpg`:

- **What travels is what changed, not the row.** Not a size compromise, though
  PostgreSQL's 8000-byte payload makes the decision easy: what a subscriber may
  see is decided per subscriber, by re-reading each row through their own
  narrowing, and a broker carrying content would answer that once, in the wrong
  place, for everybody. A call that wrote more rows than fit arrives as several
  notifications, which `watch.Next` cannot tell from several calls.
- **Losing the connection cuts every subscriber.** There is no backlog to catch
  up from, so resuming quietly would leave a stream open, healthy-looking and
  permanently behind -- the failure this seam exists to prevent, arriving through
  the thing meant to fix it.
- **Publishing is asynchronous and bounded.** `Publish` must not block the call
  that produced it, so a full queue drops -- loudly, and cutting this replica's
  subscribers, which is the only recovery available from there. A deployment
  that cannot lose an event pairs it with `watch.outbox`, and that composition
  now works: the drainer publishes into this broker, so it crosses replicas,
  which it does not with `memory`.

`RegisterBroker` had to change to make it possible: the build is handed the
app's `DbConfig`, because a broker riding the database had no way to learn which
one. That is the shape the registry should have had -- the first broker anybody
writes is this one.

#### What is left

Ordinary deployment work, in `docs/OPERATING.md`: one database, migration as its
own step, seeding out of band, a maximum connection age so a new replica gets
traffic, and the same keyring and corpus everywhere.

### D32 · A screen somebody draws about themselves takes no subject

D24 §4 and §5, and the rule both screens turned out to be about.

#### The two screens are two ports and two credentials

**§5, the operator's**, is the console's customers tab, on the admin listener,
holding a control-plane session. **§4, a person's own**, is a page in the
reference app, holding that app's cookie and reaching roster with a delegation.
Neither is the other with a different stylesheet, and the split is D24's own:
one is somebody administering a deployment and the other is somebody looking at
themselves.

#### And the second one needed two writes that take nothing

`MeService.Get` is safe to read through no wall because it takes nothing: it
cannot be pointed at anybody else, which is why `cmd.Policy` waives a binding
for it. Removing your own way in and signing yourself out have the same
property, and needed it for the same reason -- **the alternative is a role, and
a role is the wrong shape twice over.**

`Identity` narrows by the tenant, so "may remove their own way in" would have to
be granted as "may remove anybody's": the leak D17 named, arriving on the one
screen it is most tempting on. And requiring a role at all means somebody who
has just been given an account cannot sign themselves out of a session they no
longer trust, which is the moment they most want to.

So `MeService.Unlink` and `MeService.SignOutEverywhere`, both waived like `Get`
and both refusing anything that is not the caller's own. The argument on
`Unlink` is a **which** and never a **whose**: one that belongs to somebody else
is `NotFound`, told apart from nothing.

What keeps them from being a hole is the same absence that keeps `Get` safe,
plus the rules already in the layer -- `server/core` refuses the removal of a
last way in, so the button cannot lock somebody out of their own account.

#### The operator screen found a port with a hole in it

Which is what D24 says a reference consumer is for. The admin listener served
the entity services and not `VouchService`, so an operator could suspend
somebody and could not give them a new password -- in the deployment shape that
whole surface exists for.

It is served there **without** D28's rule, and that is a decision: D28 reads
what the caller holds through their bindings, and a session on that port names a
control-plane holder whose bindings are in the other database. The rule would
refuse every reset of anybody with a role, which is not conservatism but an
accident of which database the actor is in.

#### The screen is one file of HTML, and that is D24 §6 waiting

*Extracting first means guessing what to extract, and what 4 and 5 turn out to
need is the specification.* A page with a build step and a component library
would be that guess with three more moving parts. One form, one table, three
buttons, and the calls written out -- whoever extracts the components has this
to read.

### D31 · A link is a way in, and it goes where a password goes

Item 3, and the half of it P5 did not already answer.

#### Half of this was done before it was reached

Item 3 said recovery is *the same machine as a magic link -- a single-use opaque
nonce roster mints and roster checks, delivered by somebody else*, and that **in
an air gap the somebody else is a person**.

That half is `Vouch.Reset` (D28): the operator generates a password and reads it
out. So what is left is the channel that is not a person -- a link -- and the
two are not one mechanism after all. D16's third leg is why: *the kind selects
the cost.* A code a **person transcribes** is short and needs argon2, a counter
and a lockout, which is `Credential`'s machinery; a link is machine-made,
machine-carried and machine-read, which is `ApiKey`'s. Recording that they are
two is the correction this entry makes to item 3.

#### It is a first factor, not a way around them

Redeeming one proves the person and nothing more. `Vouch.Redeem` answers in
`Delegate`'s shape, so if they have a second factor it is asked for exactly as
it would have been after a password -- because a link that skipped one would be
a way to turn a mailbox into an account, which is most of what a second factor
is for.

**And it stands where a password stands.** After a link, the password is no
longer in `available`: asking for it as well would make a link a third factor
rather than a way round the first, which is not what anybody sends one for.
That is the only substitution roster knows, and it is not sufficiency -- D21
puts *what is left to prove* on roster's side and *how many are enough* on the
caller's, and this is the first.

#### Minting says nothing about whether anybody is there

The property easiest to lose and hardest to notice. A form that asks for an
address and is filled in by strangers is exactly where an account-existence
oracle is most useful to whoever is looking for one -- and every other refusal
here was made equal-cost to avoid answering that question.

So a request for nobody answers **the same**: a token, and an expiry. It
resolves to nothing, and redeeming it fails the way every bad token does. No row
is written either, so the table is not a list of every address anybody has ever
typed.

A caller may ask for **less** than the default lifetime and not for more: how
long the channel takes is theirs to know, and how long a way into somebody's
account may lie around is not.

#### And a reset voids what came before it

D26 left this deliberately -- *a password reset that leaves old sessions alive
is not a reset* is true, and coupling it to `Set` would mean somebody changing
their own password signs themselves out of everything with nothing having said
so. This is the other act: somebody **else** giving them a new one, which is
where recovery from a takeover happens, so the sessions the takeover opened go
with it.

Best effort after the fact, because the password is already changed and failing
the whole call would leave the caller unsure which half happened.

### D30 · The attempt is roster's, and one new RPC is what it costs

D21 written down, after a stress test found that the obvious shape breaks four
things. `docs/ROADMAP.md` P7 has the four; these are the answers.

#### One new RPC, and two that grow

`Continue(continuation, kind, secret)` is new, because proving a second factor is
a distinct thing for a role to name and it takes a continuation rather than a
`who`. That is D26's argument, and it lands here.

It does **not** land on starting. A `Begin` would move every sign-in in every
deployment onto a new method for a feature most of them do not use, so `Verify`
and `Delegate` grow the answer instead -- and mint a continuation **only when
there is more to prove**, so a deployment with one factor gets byte for byte the
answer it got before and pays no row for a handle nobody would spend.

Minting stays on `Delegate`, which takes a continuation in place of a `who` and
a secret. So a two-step sign-in is `Delegate` -> `Continue` -> `Delegate`, and
there is exactly one method in the service that mints.

#### `ok` and a continuation are mutually exclusive

Not tidiness. D21 forbids roster deciding sufficiency, so `ok` on a passed first
factor would have to mean *this factor was proved* -- and every caller in the
tree reads it as *signed in* and mints a session on it. That version signs
people in on one factor, silently, in the open direction.

So `ok` is set only when there is nothing left, and an app that has never heard
of second factors goes on gating on it and fails **closed**. Better still, an
app that gates on the **token** gates on the thing that is actually a
credential.

#### The count really is one count, and it took two fixes

D21's fourth condition is *one count across the steps, or the second factor is
an unmetered guessing surface reached by passing the first*. Two things were in
the way, and the second was only found by a test.

- **The counters are per credential.** `Credential` is unique on
  `(holder, kind, name)`, so a password row and a TOTP row carry two. So a
  continuation records **which row the attempt is metered on**, and a failed
  second step counts against the row the first step used: exhausting the second
  factor closes the door the attempt came through.
- **A successful first factor cleared it.** `passed` clears the count because
  *somebody who has just proved they can sign in is not the person the lockout
  was protecting against* -- and somebody who has proved one of two things has
  not proved that yet. So the counter is cleared only when the sign-in
  **finished**. Without it, every wrong code was paid for by a fresh first
  factor that settled the bill, and the test that guessed ten codes watched the
  password stay open.

#### Its lifetime is roster's, which is not the answer D25 gave

D25 let a caller name a delegation's expiry, with no cap, because D21's *barely
alive* was an argument about a **standalone** bearer and a delegation is half of
a pair. A continuation is exactly the standalone bearer that argument was carved
out from, so it does not inherit it: minutes, fixed, and no field to say
otherwise.

#### And single use has one mechanism

Spending it is an erase. *Used* is *not there* -- `<Entity>Pick` narrows to the
live rows -- which is `Undelegate`'s answer, and two concurrent spends resolve
to one winner through the ordinary compare-and-swap. A `date_used` beside it
would be a second column recording one fact.

#### And the app's half of it, which payday had already anticipated

D21 splits at *which browser* versus *what was proved*, and the reference app is
where the first half had to be written. `authsession.Session.Expires` may be set
by a `Verify`, and payday's own comment says why: *which is how an app gives a
short session to somebody who has not finished a second factor.*

So `POST /session` answers a **session with an empty grant** and what the second
form needs to draw itself; `POST /session/continue` spends the continuation and
re-mints the cookie as a real session. The browser holds one cookie throughout
and it names nobody it may act as until the second form is answered.

Two things fall out that are worth naming:

- **A wrong code costs the first form again.** The half-session is ended with
  the attempt, so a browser cannot keep guessing -- and starting over is where
  the lockout counts, which is what makes the second factor a metered surface.
- **The half one is ended rather than upgraded.** A session's grant is written
  when it is minted and nothing widens one, which is the right direction for the
  one thing a session carries.

`available` is a **message** and not a list of kinds, for `MeCredential`'s
reason: a person may be locked out of their second factor with their password
fine, and a page told only the kind draws a form that is already dead. Both
fields are facts; a display name or a "required" flag is where D21's warning
about Kratos bites.

### D29 · A kind is checked its own way, and one of them roster must read back

P7's first two increments, and the finding that would have been hardest to see
without looking for it.

#### Every refusal costs the same, **per kind**

D14 built the equal-cost refusal: *an unknown person, a person with no
credential of this kind and a wrong secret are one response*, and the first two
`Burn` an argon2 comparison so they take as long as the third.

That works because every kind stored an argon2 hash. A TOTP comparison is three
HMAC-SHA1s and a decrypt -- microseconds -- so the moment a second kind exists,
*this person has no second factor* costs forty milliseconds and *wrong code*
costs nothing, and **the sign of the difference is inverted from what D14
built**. It is a cleaner oracle than the one D14 closed, and it answers exactly
the question D21 built its whole shape around.

So the cost belongs to the kind. Each verifier burns what its own comparison
would have cost, and the seam is one switch that was needed for the decrypt
anyway.

A kind this deployment cannot check at all is refused **before anybody is
read**, and as `Unimplemented` rather than as a `no`: it is a fact about the
deployment rather than about the person, so it must not depend on whether they
exist -- and a deployment answering "wrong code" to every attempt is one where
nobody can tell a misconfiguration from a mistake.

#### The first secret roster has to read back

Everything else here is a **verifier**. A password is compared against a hash
and the store never learns it; a key and a delegation are found by a digest of
themselves. In each case a copy of the database is a copy of things nobody can
use.

A TOTP seed is not that. Computing the code somebody is about to type means
holding the seed, so the row **is** the secret.

So it is wrapped, with a key the deployment keeps where the database is not, and
it is worth being exact about what that buys: **not** protection against a
compromised process, which has the key in memory by construction, but against a
copy of the rows. A deployment with no key refuses to hold a second factor at
all rather than storing one in the clear.

Ciphertext carries the **name** of the key that made it, authenticated, so a
deployment may hold several and roll forward. Nothing re-wraps in the
background: a sweep that rewrites every credential row is a sweep whose
half-finished state is a deployment that cannot verify anybody. A key that is
gone is every second factor gone with it, and there should be no recovery from
that -- a wrapped seed a deployment can unwrap without its key is one that was
not wrapped.

#### Replay, which D20 required and nothing recorded

*A TOTP step that has been used must not work twice, and the only place that can
be recorded is the row.* `Credential.last_step` is that place, written by the
same compare-and-swap the failure count already uses. Without it a code watched
over a shoulder is good for the rest of its thirty seconds, which is most of
what somebody holding one needs.

#### `name`, and the alias that could not be one

The index was `(holder, kind)` and the comment said *one of each per person*.
Right for a password, defensible for a seed, and wrong for the factor most
likely to be added next: registering a **second** security key is the standard
advice for WebAuthn, and a passkey lands one per device.

So `(holder, kind, name)`, with empty meaning the only one. It is `name` at
field 5 and not `alias` at field 4, and finding that out cost a broken test
suite: payday **makes an alias up** when a caller gives none, seven characters
so that every row has a slug, and this wanted the opposite -- an empty value
meaning *the only one*. Every existing lookup stopped matching at once, because
the rows had aliases nobody had asked for.

#### And enrolment, which P7 was written as already having

`Set` argon2-hashes whatever it is handed, so a seed through it is a seed nobody
can read back. `Reset` refused a non-password kind in a sentence that was wrong:
*there is nothing sensible to generate for a TOTP seed that the person could
then read out.* A seed **is** the sensible thing to generate, and it **is** read
out -- as a QR code.

So `Vouch.Enrol`: `crypto/rand` on the server for `IssueService`'s reason, the
base32 and an `otpauth://` URI answered exactly once, and the same escalation
rule D28 put on the other credential writes -- an operator who could enrol a
factor on an administrator's account would hold one of the two things that
person signs in with.

It goes in **unconfirmed**, which for this kind is `last_step` at zero. One code
has to verify before the column moves, and a factor that counted the moment it
was written would make a mis-scanned QR something somebody discovers when they
are already half signed in and cannot finish. An unconfirmed factor still
verifies -- that is how it gets confirmed -- and does not appear in what a
person *has*.

### D28 · You may write the credential of somebody no wider than you

Items 11 and 10, in that order, which is the only pair in the list where the
order is a **correctness** question rather than a convenience.

#### The rule, and the one it is not

> **You may only write the credential of somebody whose permissions are a subset
> of yours.**

Resetting a password is a way to **become** somebody, so an operator who may
reset anybody in their tenant effectively holds every permission in it -- two
operations, and it is exactly the shape `escalate.go` exists to close, arriving
through a door nobody had put a lock on because the door did not exist yet.

It is the same comparison `mayGrant` makes, in the other direction: `mayGrant`
asks whether the caller covers what they are handing out, this asks whether they
cover what the person they are becoming already holds. Same conservatism, same
reading of a missing frame as the deployment's own work, and the same "on its
own" rule about patterns.

**Not the same source, and that took two more findings to see.** This entry said
*bindings, through `rules.Granted`*, and the two directions cannot read the same
answer: `mayGrant` is right to leave a role held in a team out, because a team
role is not one to bind across the tenant, and this is wrong to -- an
administrator provisioned through a team, or through a group, read as holding
nothing at all and could be reset by anybody. A missing path refuses in one
direction and allows in the other. `rules.Holding` is what this asks now; see
D35 and D41.

Changing your own is exempt, and has to be: without it nobody could change their
own password unless they held everything they held, which is true and is a
strange way to write it.

**The alternative, named because it is defensible**: accept it, and say plainly
that a tenant operator is a tenant administrator. That is honest and probably
true of most deployments -- and it makes "operator" a smaller word than the
permission it carries, which is what gets forgotten when somebody hands the role
out. This took the conservative one for `escalate.go`'s stated reason: the
failure it produces is a conversation, and the other direction is silent.

**Where it does not reach**: suspending somebody (D26) is a denial of service
rather than an escalation, and is not covered. Somebody who may `Disable` an
administrator cannot become them, only stop them. A real gap and a different
one.

#### It arrives as a seam, because the service is not a layer

`VouchService` is hand-written and not part of `app.Server`, so no layer wraps
it -- and it is the service that writes credentials. Rather than a second
implementation of the rule, it is handed this one: `core.Reaching` over the
rules `gate.Policy` already reads, the way `me.Held` is handed the union.

The generated `CredentialService` is not covered and does not need to be. It is
unregistered and in `closed`, so nothing on the wire or in a batch reaches it;
what does is this process through `Ungated`, where there is no frame.

#### And then the surface

D13 closed `CredentialService` entirely -- unregistered, closed to the batch --
because its generated `Get` answers with the verifier. That is right for the
read and it took the write with it: nothing on the wire could set a password,
and `init` plus a shell was the only way in.

An air-gapped deployment cannot live with that. There is no mail, so the
somebody else who delivers a recovery code is a **person**, which makes recovery
and an operator-initiated reset the same mechanism reached two ways. So:

- **`Vouch.Reset`** generates a password and answers with it once. The operator
  does not choose it, which is `IssueService`'s argument about a key unchanged:
  a secret the caller chose is a secret the caller knows, and one generated in a
  console is only as good as that page's `crypto`.
- **`Vouch.Unlock`** opens an account too many wrong answers closed. A
  convenience -- a lockout releases itself after fifteen minutes -- and also the
  answer to the limitation D14 recorded and could not close from where it was:
  *an account can still be held closed by somebody else*, ten wrong guesses
  every fifteen minutes, for as long as somebody cares to. A person on site can
  simply open it.
- **`Vouch.Set`** already existed and was already on the wire, so the rule
  closes a hole that was open rather than one this opened.

The shape is the one D13 named when it shut the door: not reopening
`CredentialService`, but a narrow service that takes secrets in and never
answers with one it was holding.

### D27 · A name is a row, and the tenant it names is what F7 was missing

Items 1 and 2 of the list, and F7 closed with them, because all three turned out
to be the same sentence read in three places.

#### `Host` and `MailDomain` are entities, not fields on `Tenant`

They have to be **looked up**, and `holder.proto` already wrote the rule that
decides this: *anything that has to be looked up goes flat beside it*, which is
the reason `Identity` is an entity and `Profile` is not. A repeated field on
`Tenant` is one value to the database, with no index, so a front door resolving
a name would read every tenant there is.

**Two entities and not one**, because LOGIN.md is explicit that they answer
different questions and because they differ in the two ways that decide a
schema. A host is a public name somebody owns, unique across the deployment; a
mail domain is one operator's routing hint about their own people, unique within
their tenant, and two operators saying something about `@gmail.com` are two
facts.

`Host.name` being unique across the deployment is one of the few constraints
here that crosses the wall, and it costs a small oracle: the second operator to
claim a name is told it is taken, by somebody they cannot see. A hostname is a
public fact, so that is the cheap side of the trade -- and `Email` went the
other way for the reason D3 gives.

#### `FrontService` reads through no wall, and answers with no row

Both questions are asked **before anybody has been resolved to a tenant** --
that is what they are for. So they read the server the wall was never installed
on, which is `vouch.Verify`'s situation and its stated reason: *working out who
somebody is cannot require knowing who they are.*

What keeps that from being a hole is that neither RPC answers with a **row**.
One tenant identifier, or one provider name. There is no `Select` to get wrong
and nothing that could be pointed at anything but a name somebody typed into
DNS.

The two refusals differ, deliberately. An unserved host is `NotFound`, because a
front door that carried on with no tenant would look somebody up in whichever
one it happened to reach. An unrouted domain is an **empty answer**, because
there is nothing to carry on wrongly with -- and because `NotFound` there would
answer *does this domain exist here*.

#### A name is stored as it is compared, and it is refused rather than fixed

Normalising on the way in is kinder and is the wrong direction: a caller that
wrote `Acme.Example.com:8443` and read back `acme.example.com` has had its value
changed without being told, and the next thing it does is disagree with itself.
Worse, the caller most likely to write one is a console reading it back to the
person who typed it.

So `server/core` refuses one that is not already normalised, and says what it
should have been. What goes wrong without it is nothing, for a long time: the
row is written, a console lists it, and the only thing that never happens is a
match -- a sign-in page saying nobody is there, on a tenant that is plainly
configured.

#### And F7 closes, which needed both halves

F7 said an address could not name anybody, and left `VouchWho` field 4 empty
with the reason: `Email` is unique **per holder** so that a consultant can be
one person in two tenants under one address, so one address could name two
people.

The way out was the second half of F7's own sentence -- *or a tenant that
arrives from somewhere the form did not type*. So:

- **`Email` gains a stamped `tenant_id`**, which is what a unique index across a
  tenant can be written over at all; the wall reaches this entity through the
  holder and an index cannot follow an edge. Immutable, because a stamp lands
  in the generated `Patch` otherwise and a caller who may patch could move a row
  behind another tenant's wall.
- **`(tenant, address)` is unique**, so within one tenant an address names one
  person. D3's consultant is untouched: that case is *across* tenants and this
  constrains *within* one.
- **`VouchWho.address`** is field 4, always beside the tenant. There is no form
  that takes an address alone, and that is the constraint rather than an
  omission: a lookup that could be made without naming a tenant is one a front
  door that forgot to think about which one compiles a wrong answer for.

The real cost is a **migration**: two people in one tenant sharing an address is
a shape this schema used to permit, and the index cannot be taken while one
exists.

### D26 · Two timestamps on a Holder, and the refusals are the feature

Items 6 and 12 of the list below, taken together because they are two columns at
the two numbers payday leaves this app free, and because everything hard about
them is the same thing: **nothing generated reads a new timestamp.** Not the
wall, not the gate, not the erasure machinery. The app compiles and serves with
both columns in place and neither meaning anything, so the schema is the small
half and the refusals are the decision.

#### Both are timestamps, and a flag would lose the argument

`date_invalidated` says *everything issued before this moment is void*, and its
whole correctness case is monotonicity: the value travels, a duplicate is a
no-op, a stale one cannot un-revoke, and a message that never arrives costs
latency rather than correctness. A boolean that flips has none of that.

`date_disabled` rides the same stream, so it takes the same shape -- and *since
when* is a question an operator asks about a suspension anyway.

#### Three methods, and not a field on `Update`

`HolderService.Update` is the narrow write a person makes **about themselves**.
These are somebody else's decision about them, so they are their own methods --
and roles here are lists of methods, which means a separate name is the only way
a deployment can grant one without granting the other. As a field on `Update`,
suspending somebody would be something anybody who may edit a profile may do,
with nothing to ask for instead.

`Disable` and `Enable` rather than one method taking a value, for the same
reason one notch down. `Invalidate` has no opposite, by construction: it takes
no time from anybody and the server stamps `now`, so an older value cannot be
written and there is nothing to undo. A credential that can be brought back is
not revoked.

#### They decline the version, and `Update` cannot

payday refuses a `Patch` with no version rather than assuming one, *because an
unset field cannot be told apart from a caller who never considered locking at
all*. That is right for `Update`, which replaces a value the caller read.

It is wrong here, and not slightly: requiring a version means a suspension that
**fails because somebody edited a profile**, which makes editing a profile in a
loop a way to avoid being suspended. A security action that can lose a race is
one that can be prevented. So a caller who has read the row may send the version
and get the check, and one who has not gets the write.

#### Where each is enforced, and why neither place covers the other

| | where | why it cannot be the other place |
| --- | --- | --- |
| `date_disabled` | `cmd.Resolver`, `vouch.Verify`, and `keys.findKey` | the resolver is where every credential that resolves to a holder arrives **at roster** -- a session, an `rt_`, a delegation -- and it never sees a password. `vouch` is where somebody signs in and there is no frame yet. The third was missing: a product app's token never reaches the resolver, because the app asks `TokenService/Introspect` instead. See below |
| `date_invalidated` | the credential's own lookup | the resolver sees the holder and not the credential, so it cannot know **when** the thing in front of it was issued. Only the row does |

That split is the whole of why this is two changes and not one.

#### And the row this table did not have

The first row said `cmd.Resolver` covers *every credential that resolves to a
holder*, which is true of every credential arriving **here** -- and a product
app's is not one of them. custody is handed an `rt_` or an `rd_` and asks
`TokenService/Introspect` what it stands for; that call carries the *app's* own
key, so the person's credential is a string in the request body and never
reaches the resolver at all.

Which made this act do half of what it says. Somebody suspended could not sign
in and could not call roster, and went on working in every app in front of it
until their token expired -- possibly never, since `ApiKey.date_expires` is
nullable on purpose. The one act whose whole point is *this person, not now*
did not reach where the person actually was.

It is read in `keys.findKey` now, which is where both answers this package
gives are built -- `Store`'s, which the resolver would have caught a moment
later anyway, and `Service`'s, which nothing was going to catch. One place
rather than two that have to agree, and it is the same `NotFound` every other
refusal about a token uses, so telling them apart says nothing.

#### What the epoch voids, and what it deliberately does not

A `Delegation`, which is a sign-in in miniature. **Not** an `ApiKey`.

The other reading is defensible -- somebody signing out everywhere after a
compromise wants everything dead -- and this is what was chosen instead: a key
is named, listed and revoked one at a time, and killing somebody's scripts
silently under "sign out everywhere" is an outage with nothing anywhere saying
why. Revoking a key is a second act and it has a second name. It is what every
provider that has both does, and the day it is wrong here is the day to write
`InvalidateKeys` rather than to widen this.

#### And item 4's first increment came free

`Holder` already declares `watch: {}`, justified in `holder.proto` because *the
one fact about somebody that has to travel is that they are **gone***. Two more
facts about somebody that have to travel are now on the same row, so they arrive
on a stream that already exists. The event stream item 4 argues for is a second
increment, worth taking when the noise is measured rather than predicted.

#### What is not here

**Escalation.** Suspending an administrator is a denial of service, and
`server/core/escalate.go` covers roles, bindings and an API key's methods and
not this. It is the same subject as item 11 and it is taken there, where the
rule is chosen rather than assumed.

**A password change does not invalidate.** *A password reset that leaves old
sessions alive is not a reset* is true and belongs with the recovery work that
has the reset in it. Wired here it would mean somebody changing their own
password signs themselves out of everything, with nothing having said so.

### D25 · A delegation is its own row, and the prefix is what reaches it

D23 said what this is and left every question about where it lives. This is
those answers, and the first of them reverses something `docs/ROADMAP.md` had
written down.

#### It is not one table with the continuation and the nonce

The three of them are described with the same four words -- opaque,
short-lived, single-use, bound to the caller -- and D19's question has one
answer for all three, so they read as one entity with a `kind`. **D16 refutes
it**, in the words it used about `ApiKey` and `Credential`:

- **What is proved is not what is granted.** This carries `methods`, read by the
  interceptor before the handler. D21's continuation carries `satisfied`, grants
  nothing, and must never be a bearer at all. One table means a `methods` column
  that is load-bearing for one kind and must be empty for the others, which is
  an invariant no schema states -- so it becomes a hand-written check in every
  place that reads the table, and the one that forgets serves a half-proven
  identity as a caller.
- **The kind selects the cost.** This and a continuation are 256 bits from
  `crypto/rand`, so the hash is fast and unsalted. An air-gapped recovery code
  is *read out or written down by a local operator*: short, and therefore
  argon2id with an attempt counter and a lockout. That is `Credential`'s
  machinery and not `ApiKey`'s.

D16's first leg -- uniqueness -- does not transfer, and saying so is part of the
decision: all three are many-per-holder and found by their own verifier.

#### And it is not an `ApiKey`

The nearer comparison, and D23 makes it itself: *practically it is an `rt_` key
with a short life, minted for the person an app just authenticated.* Every
column is `ApiKey`'s except one.

What separates them is **who names the row**. An `ApiKey`'s `alias` is unique
per holder and is *what somebody calls this key when deciding whether to revoke
it*; the screen that lists them is one of the reasons D23 exists. A delegation
is minted once per sign-in by an app the person never sees. One table makes that
screen a list of rows nobody named, arriving faster than anybody reads them,
with the two or three a person actually made lost in it.

So `Delegation` leaves 4 to 7 empty, which is that difference written in the
schema rather than in a comment.

#### `rd_`, and the prefix now decides a table as well

OPERATING.md already says the prefix *decides which database holds the row and
who the token is served as*. This is that rule's next entry: same database as
`rt_`, different table, and the same answer about who -- the **holder**, with
their wall, their bindings and their sites, narrowed further by the row's
`methods` and never widened by them.

It is one prefix for one kind of thing, which is what keeps it an entry rather
than an exception. A continuation is not a bearer credential and does not get
one; D21 already gave it `vc_` and a request body.

#### The issuer is a column, and a delegation is not a bearer credential

D21 and D23 both require *bound to the caller it was issued to*. Three things
follow, and the third took a correction.

- **It cannot be an edge.** The caller is a control-plane row and this is a data
  plane row, and D15 put a database between them with no query across it. So it
  is an identifier written down and compared.
- **It cannot be checked in the token store.** `auth.TokenStore.Lookup` is handed
  the token and nothing else -- no caller, no peer, no frame. A comparison
  written there compiles, runs, and binds nothing.
- **So it does not travel in `authorization` at all.** This entry first put it
  there and checked the binding in `TokenService/Introspect`, which is where an
  app asks *about* a token. That left the path where an app *spends* one -- the
  path the whole feature is for -- with no check, because a credential in
  `authorization` arrives alone and there is nothing on the request saying who
  presented it. Anything that came by the string could spend it, as the person,
  for its whole life. The condition was written down and was false.

  What is true instead: the app goes on authenticating as itself, and says who
  it is acting for in a **second header**. Both are on the request, so the
  comparison has something to compare, and a delegation that leaks is worth
  nothing without the key it was minted for. `keys.Acting` is the handler and
  `keys.HeaderActing` is the header.

  It is the honest shape of what was always happening. A delegation is not a
  second identity for an app; it is an attenuation of the app's own call down to
  one person. The app stays the caller `grpcx.Limit` counts and the connection
  roster trusts, and what changes is who the request is about.

And **empty is not a state the column may hold**, because
`subtle.ConstantTimeCompare` answers 1 for two empty slices: a delegation bound
to nobody would match a caller whose own identifier failed to resolve. `Delegate`
refuses to write one, and the comparison refuses an unresolved caller, so
neither is left to the compare.

#### What the issuer names, and what that costs

The **key row**, not the service holder it hangs off, because that is what
`cmd.Resolver` makes the actor of a deployment key. So rotating an app's key
invalidates the delegations it issued.

Taken deliberately rather than inherited: these live for minutes, and a caller
whose credential has been replaced is not obviously the same caller. Resolving
to the holder instead needs a select change in `keyed` and a read on every
request, and it would make a rotation invisible where an invalidation is
arguably the honest answer.

#### What mints one, and what ends it

`VouchService.Delegate`: `Verify` plus a token, in one call. A **method** and
not a field on `Verify`, for the reason D26 gave one entry earlier -- a role is
a list of methods, so what a deployment can grant is exactly what it can name,
and a Login App that checks passwords and must never mint is a different grant
from a product app that needs the token. On a field it would also be invisible
in a role, in an `Audit` row and in `roster key add --allow`.

The alternative it is measured against is not verify-then-something: that is two
round trips, two argon2 comparisons and two entries in one lockout count -- or,
if the second call takes no secret, a credential for somebody nobody just
proved. It is `Delegate` **instead of** `Verify`, sharing the verification path
verbatim.

Two things about it are the design rather than the implementation:

- **What may be minted is checked before the secret is compared.** Run after, a
  caller that over-asks gets `PermissionDenied` for a right password and
  `ok:false` for a wrong one -- D14's equal-cost refusal undone as a status
  code, which is worse than a timing leak because it is exact. There is a test,
  and moving the check three lines down fails it.
- **No wider than the caller.** Each method asked for has to be covered by the
  calling key's own grant, or an app allowed only to check passwords could mint
  a token carrying `HolderService/Erase` and spend it through somebody who may.
  It is **not** `escalate.go`'s rule: that one reads bindings, because it guards
  a row that hands permissions to a person; this reads `f.Grant`, because the
  caller is a machine whose bindings are in another database. And it bites
  exactly as hard as that credential is narrow -- under `auth.Plain` or mTLS a
  caller carries `frame.Whole` and this refuses nothing, correctly, because
  there is nothing there to be wider than.

`VouchService.Revoke` is the delete D23 promised and did not have. Without it,
signing out of an app left that app holding a credential that went on working:
the generated service is unregistered and closed, and `Invalidate` is the wrong
instrument -- it voids every delegation a person has and touches nobody's
session. Every answer is the same answer, for `Erase`'s reason and for a second
one: it says nothing to whoever is holding a string they found.

#### The lifetime, and it moved

D25 first said 15 minutes, *provisional*, with D24's rule that a page decides.
The page did not decide it -- the **binding fix** did.

D21's *barely alive* was the argument for a standalone bearer credential. A
delegation is not one: it is half of a pair whose other half is the app's own
key, so one that leaks is worth nothing to anybody who does not already hold
that key, and anybody who does is already past every wall this has.

So the caller names when it stops, roster defaults, and there is **no cap**.
roster does not hold the truth of anybody's session and has no opinion to
impose; what bounds a delegation is that it is never wider than the person and
worth nothing without the key. A time in the past is refused rather than
clamped, and an absent expiry on the row is still refused at every read.

#### And the table is collected

Expiry is enforced on read and never by a sweep -- *a sweep that is the
mechanism is a sweep whose outage is a security incident*. That sentence has a
second half which was not built: nothing removed a row, so the table grew by one
per sign-in since the deployment started. `keys.Sweep` is the collector, hard
deleting rather than erasing because a soft erase leaves the row, and running
beside the outbox drain.

Which matters more than it sounds, because `Delegate` **writes on the sign-in
path** -- a row, a version, an audit entry and a watch event per delegated
sign-in. `passed` goes out of its way to avoid exactly that, and this is a
reason to call `Verify` where no delegation is wanted.

### D19 · The line is issuance, not authentication

This was written down after a conversation that went round twice, because the
boundary had been stated two ways and only one of them was true.

The wrong statement was a **list of things roster does not implement** —
providers, MFA, magic links. It is falsified by the code: `VouchService` checks
a password, which is exactly what a provider does, and D5 has always planned for
a TOTP secret. Read that way roster looks like it has already overrun its own
boundary, and the next feature has to be argued from scratch.

The true statement is one sentence:

> **roster stores facts and verifies claims about them. It never issues anything
> a third party verifies.**

The test is a question with one answer: **who checks this?** If roster is the
only thing that can, roster may hold it and hand it out. If anybody else has to
be able to check it without asking, it is not roster's to make.

What that admits, and each of these is already true or is simply consistent:

| | why it is inside |
| --- | --- |
| a password (`Credential`, D14) | roster holds the verifier, so roster compares |
| a magic link | a single-use opaque nonce roster mints and roster checks. **Sending** the mail is not roster's |
| a second factor (D20) | the same argument one factor along |
| an `rt_` API key (D16) | opaque. A product app learns what it means by asking `TokenService/Introspect` |
| the console's own cookie | there roster **is** the server the browser is talking to |

What it refuses:

| | why it is outside |
| --- | --- |
| a signed token — JWT — for other systems | issuer, JWKS, rotation, expiry against revocation, audience, front-channel logout. That list is Hydra's feature list, and writing it is writing Hydra |
| a session cookie for a product app | a cookie is set by the server the browser is talking to, and roster has no browser, no cookie domain and no CSRF story. See LOGIN.md |
| deciding *when* a second factor is needed | a flow, and flows are the Login App's |

**The two are not the same size, and that is the point.** Verifying is a
question answered in one place, now. Issuing is a credential that outlives the
answer and has to be believed by people who cannot ask.

#### What replaced the wrong version of this

- **`auth.Issuer` was deleted from payday**, and `payday/auth/authsession` is
  what stands in its place: an opaque key from `crypto/rand`, a row in a store
  the serving app owns, no claims and no signing key. Its own package comment
  makes the same distinction this decision does.
- **Cross-app single sign-on is Hydra's**, and roster does not shrink for it —
  see "Where roster sits when Hydra is in front", above. Hydra has no user
  database at all, so the `subject` it writes into every token is somebody
  else's answer. That somebody is roster, and D1 is why.

#### The name is a check on the design

`roster` means a list of people and what each is assigned to. Everything above
that is inside the line — identities, addresses, memberships, roles, and the
checks that say somebody is who they claim — is still that list. The one change
that would make the name wrong is roster signing something for the world, and
that is the same change the rule already refuses. When the name stops fitting,
look at the design before looking at the name.

### D20 · A second factor is roster's; the flow over it is not

D19 applied to 2FA, and it splits in the same place everything else does.

**roster's half.** The factor is a `Credential` row — D5 said "the password
hash, and later a TOTP secret" from the beginning — and roster verifies it, for
D14's reason: a secret that leaves the store puts the comparison, the attempt
counter and the lockout in two places that will disagree. Replay is the same
argument again: a TOTP step that has been used must not work twice, and the only
place that can be recorded is the row.

WebAuthn is the interesting case because a public key **is not a secret**, so
D14's "it must not travel" does not apply to it. What still keeps verification
here is the **signature counter**: it is state that has to move forward exactly
once per assertion, and state belongs to whoever holds the row. So roster
verifies, taking the relying-party id, origin and challenge as arguments — they
are the browser-facing half and roster does not know them.

**Not roster's.** Whether this deployment requires a second factor, whether this
person is exempt, whether this browser is remembered, what order the prompts
come in, and what `amr`/`acr` the Login App reports to Hydra. Those are the
flow, and the flow is where the browser is.

The seam already exists on both sides. `authsession.Session.Expires` may be set
by a `Verify`, and payday's comment says why: *"which is how an app gives a
short session to somebody who has not finished a second factor."* And roster can
answer **what factors somebody has** without deciding anything — that is a fact
about a person, which is the thing this app is.

**One thing this entry got wrong** by saying "the flow is where the browser is"
and stopping there: *which browser* is mid-flow is the app's, but *what has been
proven so far about this person* is not. D21 is the correction, and it matters
because the difference is invisible until somebody tries to write the second
form.

### D21 · What was proven is roster's; which browser proved it is not

D20 drew this line one notch too coarse. Both halves of a two-step sign-in look
like "flow state" and only one of them is.

The question that found it: **an app that shows a second form has to remember
who passed the first one.** An app developer wants to know who somebody is and
does not want to be in the sign-in business at all, so making them carry that is
handing them the one part of the process they were trying to avoid.

#### The split

| | whose | why |
| --- | --- | --- |
| which browser is in the middle of a sign-in | the **app's** cookie | it is bound to a browser, and the browser is the app's |
| holder H satisfied `password` at T, and has `totp` available | **roster's** row | it is bound to an *attempt*, and roster is the only thing that can say either half |

The second one never made roster see a browser, which is what the earlier
version assumed. The app carries an opaque string in a request body; no cookie
is set by roster, nothing about CSRF changes, and the caller list is still
machines.

#### It passes D19's own test

**Who checks it?** Only roster: the continuation is opaque, resolves nowhere
else, and revoking it is a delete. That is the same category as a magic-link
nonce, and it is inside the line for the same reason.

#### Three answers and three refusals

The line inside the answer, since "tell the app about the flow" is a request
that keeps arriving one field at a time:

> roster answers **what has been satisfied, what this person has, and what it
> is waiting for.** It does not answer **how many are needed, which one to
> offer, or what to call them.**

The first three are facts — about an attempt, about a person, about a challenge
roster itself minted. The last three are policy and presentation, and each has
an owner that is not roster.

| asked for | answered by | |
| --- | --- | --- |
| how far along am I | `satisfied` | its length is "how many so far" |
| what may they use next | `available` | the credentials this person has registered |
| what is outstanding | `pending` | a challenge, where the method has one |
| how many steps in total | **nothing here** | "two is enough" is sufficiency, and D20 leaves that to the caller |
| which to put on the screen | **nothing here** | a product rule — "TOTP is not enough for an admin" is the app's to hold |
| what the step is called | **nothing here** | the screen, which is where D21 stops |

Note what falls out: **"step 2 of 2" is not answerable and does not need to
be.** `len(satisfied)` and whether `pending` is set draw the same screen without
roster claiming a total it does not own.

#### The shape

    Vouch.Begin(who, method, secret)
      → {ok, holder, tenant,
         satisfied:    [password],
         available:    [totp, webauthn],
         pending:      {method: webauthn, challenge: "…"},
         continuation: "vc_…"}

    Vouch.Continue(continuation, method, secret)
      → the same shape, with `satisfied` grown

`pending` is there because a challenge-response factor needs one, and the
challenge has to be minted and spent by whoever verifies the assertion — which
D20 already said is roster, on account of the signature counter. It is a nonce
and takes a nonce's rules: single-use, short-lived, bound to this continuation.
A factor with no challenge, TOTP being the obvious one, leaves it empty.

What the app writes is two forms and one string passed back. It never holds an
identity that is half proven, and it never learns anything about the process.

**D20 survives intact**, and that is the check that this is the right shape:
roster answers what was **satisfied** and what **exists**, both of which are
facts about a person. Whether two were required is still the caller's policy,
and roster does not read it.

#### Five things it has to do, and each is a way to get it wrong

- **`available` is answered only once something is satisfied.** Otherwise it is
  an account-enumeration oracle: anybody could ask which factors a stranger has
  registered, and D14 spent real effort making every refusal cost the same so
  that nothing answers "does this account exist". The shape enforces it rather
  than a rule doing so — a continuation exists only after a factor has passed,
  so putting `available` on the responses that carry one leaves nowhere to ask
  it early.
- **Short-lived and single-use.** Minutes. A continuation is a bearer credential
  for a half-proven identity, and the only thing that makes that acceptable is
  that it is barely alive.
- **Bound to the caller that was issued it.** Resolvable by that key and no
  other. Without this, one product app can pick up an authentication another one
  started.
- **The lockout counter spans the steps.** D14's accounting has to be one count
  across `Begin` and `Continue`, or the second factor is an unmetered guessing
  surface reached by passing the first.
- **`Vouch` becomes a small state machine.** It is a pure question today. This
  adds a table and an expiry sweep, and that is a real cost to a service whose
  simplicity was a feature.

The stateless version — sign the continuation so no row is needed — is the thing
D19 refuses. The row is the answer.

#### Where this stops, and the precedent for stopping there

Ory Kratos does exactly this: a self-service flow is a server-side object with
an id, and the UI carries the id. That much is proven.

**Kratos' flow also carries the UI — which fields to draw, which messages to
show — and that is the part not to copy.** It is why using Kratos means the
shape of your form belongs to Kratos. So:

> **The flow's identity state is roster's. The flow's screen is not.**

roster answers "`password` is satisfied, `totp` is available". What that looks
like, what it is called and what colour it is are the app's — which is the whole
point of roster not being the login app, and it would be given away by the one
extra field that seemed helpful.

### D22 · The login flow ships as a package, and never as a service

The wish behind this is right: somebody building an app wants to put up their
brand and write their business logic, not to learn what a second factor costs.
Handing them a store and a list of RPCs leaves the hardest, most security-shaped
part of the job on their desk.

The answer is to write that part **and ship it as something they import.** The
answer is not to serve it.

#### Why the package does not move the line

D19 is about what roster **the service** issues. A library running in the app's
own process issues nothing on roster's behalf — it is the same people writing
the other side of the same seam. `examples/sso` is already this one size down,
and the exported `authsession.Verify` in the "after it says yes" section is its
first proper step.

D21 is what makes the package thin enough to be worth having. The attempt state
— what has been proven about this person — is roster's, so the package does
not carry it. What the package carries is the browser binding and the screens,
which is the half that has to be where the browser is.

#### Why the service does

A hosted login page takes on a browser, a cookie domain, CSRF, template
rendering and an XSS surface. That is the cost, and it is not what decides it.
What decides it is already written down in LOGIN.md:

> Which tenant it is. That is what a tenant *is*: the same service under a
> different operator's own domain.

A multi-tenant front door lives on the **operator's** domain. roster serving it
means roster serving many domains, with their certificates and their branding,
which is a hosting product and not a store. And F7's way out — read the tenant
from the hostname — assumes the hostname belongs to the front door.

Then everything leaning on *roster never sees a browser* has to be re-argued:
D13, D14 and D19 all rest on it.

One thing genuinely would improve. D14 records that roster cannot count failures
by origin, *because it only ever sees the Login App*. A front door can. But so
can a package running in the front door, so this buys nothing that costs a
boundary.

#### What it is

- **A Go package** — an `authsession.Verify` and the step machine over
  `Vouch.Begin` / `Continue`, mounted on the app's mux. The app's domain, the
  app's cookie, the app's CSRF.
- **A TS package** — headless components and a default theme. Components rather
  than a hosted page, because that is what makes the brand actually the app's,
  which was the whole motivation.

#### The failure mode to watch

It will be tempting to add "one endpoint" to roster to make the package simpler.
D21 draws where that is allowed: the **attempt** may live here, the **browser**
may not, and the screen never. A field that describes what to render is the one
to refuse, however small it looks.

#### It cannot be called roster

A login flow is not a list of people. Needing a second name is the signal that
this is a second product in one repository rather than roster growing — which is
the honest description and the one that keeps each of them arguable on its own.

The shape is settled elsewhere too: Ory ships Kratos as the API and its UI
separately, and Clerk sells drop-in components. Flow as a library, store as a
service.

### D23 · A product app calls roster as one of its users

There is no way for custody to ask roster a question **as** somebody it has
signed in. Nothing in the tree does it; the gap was found by trying to design a
screen that needs it.

Every screen that shows a person their own record needs it — my identities, my
addresses, sign me out everywhere — and so does an operator listing the people
in their tenant. So this is the prerequisite for all of that rather than one
feature among them.

#### Why the two obvious ways are wrong

- **custody's own `rk_` key.** It belongs to the deployment and sees every
  tenant there is (D16). Drawing one page with the widest credential in the
  system is a habit that gets copied.
- **custody filtering in app code.** D17 already named this and named the cost:
  *"that is the kind of thing that leaks by being forgotten."* On a self-service
  screen it is worse than a leak of rows — one bug in one app exposes
  everybody's identities, and roster answered every one of those reads
  correctly.

#### The shape

`VouchService.Verify` has already proven the person to roster's satisfaction, so
the answer rides back with the yes: a short-lived opaque **delegation token**
beside `{ok, holder, tenant}`. custody calls with it, and roster applies that
person's wall, bindings and sites — which is D16's `rt_` rule, on a token that
lives for minutes instead of until it is revoked.

It is inside D19 for the reason the continuation is: opaque, resolvable only
here, and revoking it is a delete. Practically it is *an `rt_` key with a short
life, minted for the person an app just authenticated.*

Conditions, and they are the same family as D21's:

- **Short-lived**, and refreshed by signing in rather than by extending.
- **Bound to the caller it was issued to.** One product app must not be able to
  use another's.
- **Never wider than the person.** Its narrowing is theirs; a method they cannot
  call is refused through it too, exactly as with an `rt_` key.
- **The trail says the person, not the token** — D16's note about what an `rt_`
  costs applies here unchanged, and it is worth knowing before it is used for
  writes.

#### What is not answered

A deployment with Hydra in front does not call `Vouch` at all, so there is
nothing for the token to ride back on. Exchanging an `id_token` for one is the
obvious route and it is not designed. Anything built on this should assume the
`Vouch` case first and leave the seam.

### D24 · A reference app, and it is to roster what roster is to payday

D22 says the login flow ships as a package. A package with no consumer is
guesswork about what a consumer needs, so the consumer gets written first and in
this repository.

`examples/sso` is already the seed: a relying party that signs somebody in with
Google, Entra or GitHub and finds out who they are here.

#### What it exists to find

Not to demonstrate — to **specify**. Four things on the current list cannot be
designed without an app that wants them:

- **The delegation token** (D23). Its lifetime, its scope and where it is
  refreshed are decided by a page that uses it, not by reasoning.
- **A tenant from a hostname** (1, in the list below). Untestable without a
  front door. It is theory until something is actually served at a customer's
  name.
- **A read that answers which methods somebody has** (7 below). D13 leaves
  nothing that can, and the screen is what says which fields it needs.
- **Refusing to remove a last login method** (8 below). The rule only becomes
  necessary once there is a button that would.

#### The rule that keeps it honest

The same one, one layer down:

> **When roster is in the way, stop and fix roster. Do not work around it in the
> reference app.**

Without it the app quietly fills in whatever roster lacks, which is precisely
what this project forbids itself against payday, and the finding is lost. With
it, the app is an instrument.

#### Where it stops

**Running it as a service for other people's customers is a different
decision.** Forked or embedded, it is a reference. Hosted by us on our domain
for a customer's users, it is "roster serves browsers" arriving under another
name — and D22's argument applies to it in full.

#### The order, and why components are last

1. the delegation token (D23) — everything else is built wrong without it
2. the app's spine: sign-in and a session, grown from `examples/sso`
3. a tenant from a hostname, now that something can prove it
4. self-service: my record, add and remove an SSO method, sign out everywhere
5. the operator screen: who is in my tenant, and how they sign in
6. ~~extract the components~~ — **done**, and see below

Six is last because extracting first means guessing what to extract. What 4 and
5 turn out to need is the specification, and it is not knowable in advance.

##### And it answered smaller than the guess it was protecting against

The Go half is real and is the whole of what D22 described: `frontdoor` -- two
forms, a half session, the delegation held beside it, and the header a call to
roster rides on. It came out of `examples/sso` and is imported back into it, so
the reference app is the proof of the shape rather than a second copy of it.

The browser half is **one module and not a component library**, and that is the
finding. Two screens now exist -- a person's own page in plain HTML served by a
Go app, and an operator's in React over Connect -- and they share **none of
their markup**. Different framework, different transport, different subject, and
a component that fitted both would fit by having no opinion left. What they do
share is the part that is easy to get wrong: three answers where a page expects
two. So `frontdoor/web/frontdoor.js` is that, in plain JavaScript with a `.d.ts`
beside it, so the page with no toolchain can import it as it is.

The default theme is not written and should not be. It was a guess about what
apps would want, made before either screen existed, and both screens answered
that what they want is their own -- which is what "put your brand on it" meant
in the first place.

Two notes on 4, since it is the screen that splits: **adding** an SSO method is
half a login flow — the OIDC round trip that produces `(provider, subject)` is
the package's, and the linking rules that accept or refuse it are D9's, here.
**Removing** the last one locks somebody out of their own account, which is an
invariant no deployment would want configured differently — so it belongs in a
layer, the way D17 put the team rules there.

### D1 · `sub` is `Holder.id`

roster owns the identifier the whole system knows a person by. Hydra puts it in
tokens; roster issues it.

The alternative — a provider's own subject as `sub` — makes one person look like
two when they sign in by a second route. That is the thing this design most
wants to avoid.

Consequence: payday's `Holder` **is** the user record. No parallel `users`
table.

### D2 · `Identity` is an entity, not a column on Holder

One person, many external identities: Entra at work and GitHub for the same
human is the stated case. A column is one-to-one and cannot hold it.

`subject` is whatever that provider calls immutable — a numeric ID for GitHub,
`objectGUID`/`entryUUID` for LDAP. Never a username and never an email.

The unique key is `(tenant, provider, subject)`. It was written here as the pair
alone, and the tenant was added to it deliberately: without it one account at a
provider would belong to exactly one tenant across the whole deployment, and the
second operator a person signed up to would be told the identity was taken, by
somebody they cannot see. LOGIN.md, "A person who uses two operators' services",
is the long form.

### D3 · Email is an entity, and is not unique

Three reasons from the design notes, and they compound:

- an address is not a key: people change them, and organisations reuse them
- verification state has to live somewhere, and `email_verified` decides whether
  an address may be trusted at all
- one person has several

Not unique across the deployment either. A consultant may legitimately be in two
organisations under one address, and a uniqueness constraint would make the
second one an error nobody can resolve. What must be unique is `(holder,
address)` — nobody lists the same address twice.

### D4 · Site is field 3, and only where a row belongs to exactly one

payday reserves field 3 for an edge to a set smaller than a tenant, and Site is
exactly that: the isolation boundary the design names.

But it works only for rows that belong to **one** site. `Team` does. `Holder`
does not — a person can be in several, which is a membership table, so Holder
carries no site edge and is narrowed by tenant alone.

This is a real limit of the second axis rather than a modelling mistake, and it
is written here so the next person does not try to force it.

### D5 · Credential is separate from Holder

The password hash, and later a TOTP secret, are their own row.

Most people have none — they arrive through an external provider — so a column
would be empty on nearly every row. And credentials have a lifetime of their
own: rotation, history, lockout. That is a row that changes when the person does
not.

roster **verifies** rather than handing the hash out. A comparison done
elsewhere is a hash that has left the store, and it puts timing-safe comparison
and lockout in two places.

### D8 · A membership is a row on the second axis, and a role hangs off a team

`SiteMembership` is many-to-many, which is the reason `Holder` carries no site
edge (D4). It sits on field 3 itself, so "who is in Seoul" is answerable without
also answering "who is anywhere else".

`TeamMembership` carries the role, and **not** a site of its own: the site is
the team's, and saying it twice is two facts that can disagree. That makes its
tenancy path three hops — `via: "team.site.tenant"` — which payday generates as
one predicate rather than three reads.

A role therefore means something *in a site*: operator in Seoul, reader in
Frankfurt, one person. A role held on the person could not say that.

### D18 · The wall goes through field 2, and field 3 only narrows

`Team` reached its tenant through `site.tenant`, and the memberships through
`site.tenant` and `team.site.tenant`. So the **wall ran along the optional
axis**, and that is one mistake with two symptoms.

A team with no site reached no tenant: written, invisible to everybody
including the tenant that made it, with nothing saying so. That was read as an
argument for making the site required, which is backwards -- a namespace is
optional, and what was wrong is that the wall depended on one.

And a row naming two things reached two tenants while only one was checked:

    SiteMembership{holder: somebody in acme, site: a site of hooli's}

written, accepted, and visible to whichever tenant the wall's path happened to
land on. One tenant read a row naming the other's, which is the single thing the
wall exists to prevent. It was found by writing it.

So the rule, and it is worth stating as one:

> **Field 2 is how a row reaches its tenant. Field 3 only ever narrows.** A row
> that names two things has to be checked that they agree, and no schema can say
> that.

`Team` has a tenant edge now and its site is optional again. The memberships go
through `holder.tenant` -- one hop instead of two and three. And `server/core`
refuses a write whose references disagree, which is where the judgements no
schema can state already lived.

The agreement check is written out per entity rather than derived. An entity
added tomorrow is unchecked until somebody adds a case, and for a rule about
writes that is the direction to fail in: a generic walk would quietly stop
covering a shape nobody wrote it for.

### D17 · Roles, bound at a scope, in the shape Kubernetes settled on

roster sells access control and has none of its own beyond "your tenant".
`TeamMembership.role` is a string nothing reads, which is the gap stated as a
column.

**Site is a namespace.** That analogy is the design, not a comparison made
afterwards:

| Kubernetes | here |
| --- | --- |
| cluster | `Tenant` — the boundary that never leaks |
| namespace | `Site` — optional, and payday's field 3 is literally "narrow to a set smaller than a tenant" |
| cluster-scoped resource | `Holder`, `Identity`, `Credential` |
| namespace-scoped resource | `Team` |
| `ClusterRole` | `Role` with no site |
| `Role` | `Role` in a site |
| `RoleBinding` | `Binding` with a site |

And it predicts a defect roster already has: **a namespaced thing with no
namespace is not a thing.** `Team.site` is nullable and the wall reaches a
tenant through it, so a team with no site belongs to nobody and nobody can see
it -- it is written, it is invisible, and nothing says so. Kubernetes answers
this with a `default` namespace; here the edge becomes required.

#### The entities

    Role     tenant=2  site=3?  alias=4  methods=8[]
    Group    tenant=2  site=3?  alias=4
    GroupMembership  holder=2  group=8
    Binding  role=2  site=3?  holder=8?  group=9?
    TeamMembership   holder=2  team=8  role=9      <- already here; role becomes an edge

**`Role` is referenced from two places, and the scope is wherever the
reference lives.** On a `Binding` it is the tenant or a site. On a
`TeamMembership` it is that team -- which is what that row already says, and
what its `role` string was reaching for.

**Roles carry methods and nothing else.** A rule set is the list of RPCs it
allows, written out. Not a resource-and-verb pair, because a gRPC method name
already is one -- `/roster.HolderService/Get` says both -- and not a name to be
looked up somewhere, because the somewhere is the thing this does not have.

**A binding names a subject and a scope.** Either a holder or a group, refused
in a layer when it names both or neither, the way `server/core` already refuses
the two link mistakes no schema can state. The scope is nothing, meaning the
whole tenant, or a site.

**A binding does not name a team**, and that was in this design until somebody
asked whether team rules should be built in instead. They should. "The admin of
a team manages its members" is a **product invariant** -- true of every
deployment there will ever be -- and a configurable invariant is one that every
deployment configures identically until one of them gets it wrong. So it is
roster's rule, in roster's layer, tested once. The schema got smaller for it.

**A role defined in a site may only be bound in that site.** Kubernetes'
rule, and it is what keeps somebody who administers one site from writing a
rule that applies outside it.

#### What is deliberately not copied

- **No deny rules.** Permissions are a union, so order does not matter and a
  question has one answer arrived at one way.
- **No aggregation.** It is a convenience that makes "where did this rule come
  from" unanswerable by reading.
- **No `resourceNames` yet.** See below; it needs a seam that does not exist.
- ~~**Escalation prevention later.**~~ Done, and wider than this entry
  expected: `server/core/escalate.go`, on `Role.Add`, `Role.Patch`,
  `Binding.Add`, `TeamMembership.Add` and an API key's methods, plus the rule
  that a role scoped to a site is bound only in that site. The fourth of those
  and what counts as *held* were both got wrong first; see D35.

#### Team-scoped permission, and where it has to live

"Somebody may add and remove members of the team they are in" is an
**object-scoped** rule, and `gate.Policy` cannot express it. `gate.Call` carries
the actor, their tenant, **the actor's own row** and the method -- and never
what the call is about. A policy knows who is asking and not what they are
asking for.

So it splits, and the split is not a workaround:

- **Reads** narrow through the wall, which means the scope axis. `Site` is that
  axis, so "the teams in my site" is expressible and "this one team" is not --
  payday has one second axis and Site is it. A team administrator listing their
  own team is therefore the app filtering, not the wall narrowing, and that is
  the kind of thing that leaks by being forgotten. It is written here for that
  reason.
- **Writes** are refused in a layer that reads the request. `server/core` is
  already where the judgements no schema can state live, and this is one:
  `TeamMembership.Add` looks at `req.team` and asks whether the caller holds a
  binding scoped to it.

That is worth knowing before designing more of this: **`gate.Policy` is not the
authorization seam it looks like.** It answers "may this actor call this
method", which is most of the question and not the interesting part of it. The
rest is a layer, and payday says so by giving layers the request.

#### It is not Zanzibar, and knowing why is what keeps it small

Zanzibar -- SpiceDB, OpenFGA -- exists for **transitivity**: `viewer = editor +
parent.viewer`, answered across a graph, at a scale where consistency needs its
own protocol. "Alice may manage team A because she is an admin of team A" is one
hop and a join.

What would make it Zanzibar's problem is nesting: teams inside teams, a site
administrator implying a team administrator, resources that hang off any of
them. None of that is here, and if it arrives, the graph is the reason to
reconsider rather than the number of rules.

The built-in also has a limit worth naming: it covers rules roster ships. "May
manage the teams labelled X" is not one, and the day that is wanted is the day
to look at a general mechanism again.

#### What this finally uses

`Binding.site` **is** `frame.Sets`. `pd.Grouped` has been generated since the
first week and PLAN.md has said since then that `Sets` is handed in by a test
rather than by the app. A binding scoped to a site is the answer that function
was waiting for, and it is where `Site` stops being a demonstration of payday's
second axis and starts being a feature.

### D15 · roster's own access control is a second roster

roster sells access control. If it cannot express its own, that is evidence
about the product rather than a detail of the deployment. So it runs **twice in
one process**, on two databases:

```
  data plane      Tenant = K's customers      Holder = end users
                  Credential = passwords

  control plane   Tenant = one, K's own       Holder = K's services
                  ApiKey  = what each may call
```

custody is a `Holder` in the control plane, and that is the whole of what
dissolves the question this went round three times. A `Holder` in the data plane
is a person; a `Holder` in the control plane is a caller. The schema does not
change meaning -- the *instance* does, and they are separate instances.

**Why one process.** The control plane is consulted by the auth interceptor, on
every request. A separate deployment would need a credential to reach it, and
that credential would need checking somewhere. In one process the innermost
lookup is a Go call against `Ungated` rather than an RPC, and the recursion
terminates there.

**Why two databases.** A key must not live in the same tables as the data it
protects. Separate, a fault in the wall cannot reach the keys at all -- there is
no query from one to the other.

Two instances of one app in one process was checked before any of this was
written, on SQLite and on PostgreSQL: they register their protos and their
domains once, because they are the same app, and neither sees the other's rows.
What collided earlier today was two **different** apps, which is F8.

**What replaces `auth.Plain`.** A deployment that names no control plane serves
`Plain` and says so in the log, the way custody does with no issuer -- easy and
loud, because an app that cannot be run until a control plane exists is an app
nobody runs.

### D16 · An ApiKey is its own entity, and carries the grant

Not a `Credential` of `kind: "api-key"`, and three things say so.

`(holder, kind)` is unique, so a credential is **one per kind per person** --
which is right for a password and fatal for a key, because rotating without
downtime needs two live at once. An `ApiKey` hangs off a `Holder` 1:N.

A credential proves **who**; a key grants **what**. There is nowhere on a
credential to write the second, and `frame.Grant` is the shape it needs: a set
of methods, checked by the interceptor before the handler, and an attenuation
that a resolver cannot widen.

And the cost is wrong. argon2id at 19 MiB is right for a password, where the
attack is a dictionary. A 256-bit random key has no dictionary, and every API
call from every service would pay 19 MiB to prove it. The kind selects the cost.

**The actor on the frame is the key** -- for a control-plane key; see below,
because the other plane answers differently -- not the Holder it hangs off and
not `pdid.Nil`. The trail then names which key asked, revoking is a delete, and
no person-row is involved -- which is what `frame.Everything` warns about: *a
privilege granted by being a particular row cannot be revoked, cannot be
narrowed, and belongs to whoever finds the row.* A key row is the opposite case:
it exists to be revoked.

`Id.Domain()` is how the resolver tells the two apart before it reads anything.
An identifier says what kind of thing it names, so a caller that is a key and a
caller that is a person are distinguishable without a lookup.

#### Which actor, and it is not the same answer in both planes

The paragraph above says "the actor is the key" without qualifying it, and that
is true of a **control-plane** key and false of a **data-plane** one. They are
two cases with two right answers, and stating one of them as the rule is how the
other comes to look like a bug.

| | `rk_` · the deployment's | `rt_` · a person's |
| --- | --- | --- |
| hangs off | a control-plane `Holder`, which is a service | a data-plane `Holder`, which is a person |
| the actor is | the **key** | the **holder** |
| tenant | none; it is every tenant there is | the person's, with the wall, the bindings and the sites applying exactly as when they call |
| who asks about it | nobody — it is presented *to* roster | a product app, over `TokenService/Introspect` |

The asymmetry is the point rather than an inconsistency. An `rk_` caller is not
a person, so resolving it to one would invent a `sub`; the trail should say
which key asked, and revoking is a delete. An `rt_` caller **is** a person
acting through a key, and the whole guarantee is that it is never wider than
they are — so it resolves to them, and its `methods` only narrow that further.

Each answer costs the other's benefit, and the `rt_` side of that is worth
knowing before enabling it: **its writes are recorded as the person's**, so
`Audit` says who and not which of their keys. Revoking still works, since the
row is what the token resolves through.

**A product app sees the same pair either way.** `Introspect` answers with the
holder and the tenant, which is what `VouchService.Verify` answers with, so an
app that already resolves a signed-in person resolves a token-bearing one with
the same code. What it must not do is give it a session: there is no browser
here, and nothing to keep.

### D13 · A credential never travels, and it is registration that says so

`CredentialService` is generated like every other entity's, and its `Get`
answers with whatever columns it was asked for. One of those columns is the
password verifier. `credential.proto` had already written *"nothing reads
`secret` back"* — as an intention, which the generated code did not share.

Two doors, closed by two lines that are the same line:

- **gRPC.** `cmd.register` writes the services out and leaves that one off.
  `app.RegisterServer` is all-or-nothing, so this is a hand-written list.
- **The batch.** A batch arrives as one method carrying many, so "not
  registered" never reaches it — `pd.Batch` dispatches through the app's own
  table. `cmd.closed` names the service, and the *same function* is given to
  the chain and to `batch.Guard`, because `ServerConfig.Guard` fills `Closed`
  from configuration alone and a guard left as it came would serve what the
  wire will not.

Writing the list out means an entity added tomorrow is not served until somebody
adds a line. That is the direction to fail in: the other arrangement — serve
everything, then take one away — fails by publishing, and fails silently.

What replaces it is `VouchService`, which takes secrets in and never answers
with one.

### D14 · roster hashes, because roster compares

`Credential` already argued that comparison belongs to whoever holds the row: a
hash that has left the store puts timing-safe comparison, attempt counting and
lockout in two places that will disagree. Hashing is the same argument one step
earlier — a caller that hashes has chosen the parameters, and a store cannot
tell a good choice from a bad one, since what arrives is bytes either way.

argon2id at OWASP's first choice (19 MiB, t=2, p=1), stored as the PHC string so
the cost travels with the hash. That is what makes `vouch.Default` changeable:
`Compare` reads the parameters from the row in front of it, so raising the cost
does not lock out everybody who was hashed at the old one.

Three things that are decisions rather than implementation:

- **Every refusal costs the same.** An unknown person, a person with no
  password and a wrong password are one response, and the first two call
  `vouch.Burn` so they take as long as the third. Otherwise the response time
  answers "does this account exist".
- **A lockout is reported and the rest is not.** It tells a caller that the
  person exists, which every other refusal avoids. The alternative is somebody
  locked out being told nothing and trying forever.
- **An attempt during a lockout is not counted.** Ten consecutive wrong answers
  close an account for fifteen minutes; while it is closed nothing is compared
  and nothing is written, so the expiry does not move. Counting those attempts
  would mean one continuous stream of guesses holds the account closed for as
  long as it lasts.

  **What this does not fix**, and it is worth being exact because the name of
  the test that covers it originally overclaimed: an account can still be held
  closed by somebody else. Ten wrong guesses every fifteen minutes will do it.
  That is what locking **by name** costs — the thing being counted is the
  account, and the account is what an attacker names. The ways out are all
  somewhere else: count by origin rather than by name, ask for something a
  script will not answer, or rely on `grpcx.Limit` and drop the lockout
  entirely. None of them is roster's to choose alone, since the origin of a
  request is the Login App's to know and roster only ever sees the Login App.

  It is a lockout and not a permanent close for the same family of reasons: an
  account that a stranger can disable until a helpdesk call is a denial of
  service with a login form in front of it.

The failure counter is a compare-and-swap, so two attempts at once are one
recorded failure. It defends against *sustained* guessing; the burst is
`grpcx.Limit`'s, which counts calls without reading a row. Two mechanisms
because they are two attacks, and one counter that did both would need an
atomic increment the schema cannot ask for.

### D10 · The generated messages are package `rstr`, in `rstr/`

Rather than at the module root, which is what `pd new` writes, and rather than
the `api/` that custody uses.

The reason is that roster's types are **meant to be imported by other apps** —
it is a service others call, so its messages travel. `api` is what every app
calls its own generated package, so a product importing roster's would be
aliasing one of the two on every file. `rstr` collides with nothing.

Inside roster the import keeps the alias `app`, which is the template's
convention and custody's: locally these are "this app's types", and the package
name is doing its work at the other end.

Changing it is one line per proto — `option go_package` — and `pd gen` follows,
including rewriting the copies of payday's own entities. Nothing else moved:
`internal/ent`, `server/bare` and `server/pd` are named from the module root
rather than from the messages.

### D9 · The linking rules are a layer, and they apply without the wall too

Two judgements no schema can state, in `server/core`:

- **A subject containing `@` is refused.** It has to be what the provider treats
  as immutable, and what gets written by mistake is the username or the address —
  both are in the same claims and both read like a name. Nothing fails at link
  time; it fails months later when the address is reassigned and the new joiner
  is served as the person who left.
- **A second identity at a provider somebody already has one at is refused.**
  Uniqueness of the pair stops two people sharing a subject; this is the other
  direction, and it is what a link that found the wrong row looks like. A person
  has one account per provider. If they have two, one belongs to somebody else.

The layer is stacked on **both** servers. `Ungated` is a way around the wall and
not a way around what this app means: an identity linked by an admin console is
still an identity, and a subject that is an address is still wrong.

### D6 · A timestamp that means "or never" says `nullable: true`

`Email.date_verified` is a `google.protobuf.Timestamp`, and a message field has
presence in the generated API — `HasDateVerified` exists. A NOT NULL column
cannot keep that: an address nobody ever verified reads back as verified at the
zero time, and `Has` says yes.

So it is declared nullable, which is the honest declaration and not a
workaround. **What payday should do about the dishonest one is logged below.**

### D7 · Two databases from the first day

Every test runs on SQLite by default and on PostgreSQL when `PDTEST_POSTGRES`
names one. SQLite needs no server, so the fast loop stays fast; PostgreSQL is
what anybody deploys on, and the two disagree exactly where mistakes hide.

roster is the first app to use `pdtest.DB`, and it found a defect in it on the
first run — see F2.

---

## What roster found in payday

The point of the exercise. Each was fixed upstream rather than worked around
here, which is the rule.

### F1 · `pd new` wrote a scaffold that was not gofmt-clean

Two imports the wrong way round in `cmd/serve.go`, so every app payday
scaffolds began life failing its own first format check. Fixed in the template,
and payday's CI now runs gofmt over a fresh scaffold — the formatting is a
property an app inherits rather than chooses.

*payday `0e78d88`.*

### F2 · `pdtest.DB` answered a driver an app could not open

It handed back `"pgx"` without registering it with `config`, and a
`config.DbConfig` can only name a registered driver. An app building its harness
the obvious way got `unknown driver "pgx"` the moment it pointed at PostgreSQL.

payday's own apptest never saw it, because the `dbpgx` import had been added
there while wiring it up — the app carried the fix and the helper stayed broken
for the next one. That import is now gone, since its being there is what made
this invisible.

*payday `02fc7c9`.*

### F4 · A `via` path whose first hop was absent failed the write

payday asks apps to make a field-3 edge nullable — a schema gains one after it
already has rows, and a required edge could never be added to one. `Team` is
that shape, and it is also its own tenancy path (`via: "site.tenant"`).

The trail walks that path to file itself under a tenant. Finding no edge it
parsed no bytes as an identifier and failed — and it runs inside the transaction
that makes the write, so the **write** failed, with an `Internal`, from a layer
the caller never asked for. A team in no site could not be created at all.

It answers `uuid.Nil` now, which the recorder already knew what to do with. The
wall is unchanged and asserted separately: a row that reaches no tenant is
behind none.

Why it survived is worth keeping. payday's own apptest had entities with `via`
paths in one package and the trail in another, so no test had both at once.

*payday `e09af48`.*

### F5 · `go get @main` silently kept an old pin — **operational**

Not a defect in payday, but it cost an hour of chasing a fixed bug. The module
proxy caches what `@main` resolves to, so `go get …@main` right after a push
reports success and moves nothing. Twice the tests here failed against a payday
that already had the fix.

Use the commit, or `GOPROXY=direct`. Written in `CLAUDE.md`.

### F3 · A non-nullable message field lied about presence — **fixed**

The one above (D6) as a payday question rather than a roster one. A
`google.protobuf.Timestamp` with no `nullable`, no `default` and no marker
generated a NOT NULL column, while the API generated beside it had `Has…`. The
two cannot both be true, and the caller is the one told the lie: they ask
whether a value is set, are told yes, and read a zero somebody wrote because the
column would not take null. It is not a failure anywhere -- it is a row that
says a thing happened at the beginning of the epoch.

It is a generation failure now -- `pdgen.checkPresence` -- naming the field, the
`Has` that lies, and the two ways to say what was meant. Refused rather than
picked between, because `nullable: true` and a default mean different things and
a generator choosing one would be deciding what an app meant.

#### The boundary, which is why this one waited

The rule has to leave `date_created` (`default: ""`), `date_updated`
(`version: {}`) and `date_erased` (`erased: {}`) alone, and getting that wrong
breaks every existing schema. What makes it safe is that the exemption is stated
as those three **declarations** and not those three **names**: each is stamped
by the server rather than given by a caller, so its presence is not a claim
about what somebody sent -- and an app whose version field is spelled
differently is not caught by a rule about spelling. Edges are out for the same
reason: an edge is a message field too, and its presence is the foreign key
being there.

Confirmed against this app: a bare `google.protobuf.Timestamp date_seen = 8` on
`Host` is refused, by name, with both fixes offered. And it surfaces through
`pd doctor` now as well as `pd gen`, which is F12.

### F6 · A schema cannot say "written, never read" — **fixed, and adopted**

payday has `(payday.field).secret` now, and `pd.Secret` is the generated layer
that clears a marked field on the way out. apptest declares one and the test
fails with the layer removed.

The wait was the registry rather than the code: the extension is in payday's
**buf module**, and this app depends on the published
`buf.build/payday/payday:dev`, so `(payday.field)` was an unknown extension here
until that was pushed. It has been, and two fields declare it --
`Credential.secret` (proto/app/credential.proto:58) and `ApiKey.secret`
(proto/app/apikey.proto:99). What it took was one line per field and one in the
day payday publishes.

What follows below is what roster does in the meantime, and it stays either way:
registration is a stronger statement than a cleared field, since a service that
is not on the wire cannot answer at all.

#### The original finding

`payday.proto` extends `MessageOptions` only. There is no field-level payday
option at all, so there is nowhere to declare that `Credential.secret` is
assigned and never returned — the generated `Select` has a `secret` bool like
every other column, and `Get` honours it.

roster says it at registration instead (D13), which works and is checkable, but
it is said in the app that happens to hold the field rather than beside the
field. Any other app that stores a verifier has to rediscover the whole
argument.

What it would take: a `payday.field` extension in a new number block, a case in
`internal/pdgen` that leaves the field out of `Select` and out of every response
message, and `pd gen --check` across apptest. The write side stays as it is —
the field is still in `Add` and `Patch`, since that is the half that works.

Worth doing only if a second app needs it. One app closing a door it can see is
not obviously worse than a schema option that every app has to remember to use.

### F7 · Signing in by address has no answer yet — **closed, by D27**

The way out it named -- *a tenant from somewhere the form did not type* -- is
`Host` and `FrontService.WhoseHost`, and the constraint that makes it answer
with one person is a stamped `Email.tenant_id` with a unique `(tenant,
address)`. `VouchWho` field 4 is spent. What it costs a deployment is the
migration D27 names.

#### What it said while it was open

`Email` decided an address is unique **per holder**, deliberately, so that a
consultant can be one person in two tenants under one address. One address may
therefore name two people, and `email.proto` says it outright: *"nothing here
resolves anybody by address."*

Which means `VouchService` cannot take one, and it does not — `VouchWho` names
somebody by identifier or by `@tenant/alias`, and field 4 is a comment saying
why it is empty. Most sign-in forms collect an address, so this is a gap in the
product rather than a tidy boundary.

The two ways out are a decision, not a field: make addresses globally unique and
give up the consultant case, or take the tenant from somewhere the form did not
type — a hostname, a selector, the URL a Login App was reached at. The second
keeps the schema and is what a multi-tenant product does anyway.

### F9 · A reference through an edge reached rows that were erased — **fixed**

Not payday's, and the rule reads the same: this is `protoc-gen-orm-ent`, which
writes `server/bare/`.

`<Entity>Narrow` is documented as *"the one place a read of this entity is
narrowed, so that every query narrows the same way and none of them can be the
one that forgot"*. A **reference** was the one that forgot. A unique index over
an edge generates `Has<Target>With(<Target>Pick(...))`, and that composition
never reaches a Narrow of the target -- what the read narrows is the child.

Two symptoms, and the first is why it was looked for at all:

- **`VouchService.Verify` answered `ok: true` for an erased holder.** A
  credential is found by `CredentialRefByKind{holder, kind}`, so the holder is
  named through an edge. `holder.proto` says in as many words that an erased
  holder *cannot authenticate*, and gives the wall as the reason. The wall was
  never in that path.
- **`Email.Get` by `(erased holder, address)` answered through the walled
  server.** Erase somebody and their address stays readable by anybody who may
  read that tenant's mail, while the person themselves answers NotFound. The
  incoherence is what gives it away: a tenancy path narrows the child, and
  nothing narrows the parent's liveness.

Fixed there rather than here -- `<Entity>Pick` answers among the live rows and
the switch it used to be moves to `pick<Entity>` -- with a test that fails
without it. Narrow keeps its own copy: it is still the one place a read that
names the row directly is narrowed, and a predicate that holds twice costs a
pair of parentheses.

**What is left**, and it is a decision rather than an oversight: `<Entity>Id`
answers a reference that carries a key without a query at all, so an `Add` can
still point an edge at an erased row by naming its id. Closing it costs a read
on every write that names an edge.

The pin is moved -- `protoc-gen-orm-ent@3843c60`, by sha, for F5's reason -- and
both symptoms are closed here. Each has a test in this repository as well as
upstream, because what is pinned can be un-pinned:
`TestAnErasedHolderCannotAuthenticate` and
`TestNothingOfAnErasedHolderIsReadableByNamingThem`.

`server/vouch` keeps its own refusal even though the generator now makes it
redundant, and that is deliberate: a guarantee that holds only because of how
somebody else composes a predicate is a guarantee that stops holding without
anything here changing. It is also where `date_disabled` is refused (D26), so
the two live states of a person are decided in one place.

### F8 · Two payday apps could not be linked into one process — **fixed**

Found by trying it: custody imported this module to call `VouchService`, and its
own binary stopped starting. `--help` panicked.

Three collisions, one after another, each hidden behind the last:

- **`app.Holder` twice.** `pd new` writes `package app;` and roster kept it, so
  every message here had the name custody's did. Fixed here — `package roster;`
  — and `Layout.ProtoPkg` is read from the schema, so payday's copied entities
  followed.
- **`payday/holder.proto` twice.** The package was rewritten and the **file**
  never was, and a protobuf registry is per process and keys files by path.
  Fixed in payday: the copies land at `<pkg>/payday/` now, so an app's schema
  imports `roster/payday/holder.proto`.
- **`pdid` domain 7.** custody's asset, roster's site. The registry is global
  and panicked from an init. Fixed in payday: a number two apps mean
  differently keeps working and loses its name.

The common cause is worth keeping: **payday had process-global registries that
assumed one app per process.** None of the three was reachable by reading; each
appeared only when the one before it was fixed.

A fourth was in the harness. `pdtest.DB` named a schema after the test, so two
calls from one test got one schema and the second's `DROP SCHEMA` removed the
first app's tables. It passed on SQLite and failed only under
`PDTEST_POSTGRES`, which is the direction that hides a mistake and the reason
that variable exists.

The rule left behind: **two payday apps can share a process when their proto
packages differ.** Two instances of the *same* app always could, which is what
D15 relies on.

### F10 · `pd.Secret` did not cover `Watch`, and nothing closed could be a stream — **fixed**

Two gaps that composed, and together they meant **a watchable entity with a
verifier streamed the verifier**.

- payday's `Secret` layer wrote wrappers for `Add`, `Get`, `Patch`, `Apply` and
  `List`. There was no `Watch` override, so a `WatchItem` carried the whole
  message -- and a `WatchRequest` has no `select` to leave a column out of.
- roster installed `grpcx.ClosedUnary` and never `ClosedStream`, so `closed`
  structurally could not shut a streaming method.

Which left **not registering the service** as the only control that covered a
stream at all. `Credential` declares `watch: {}` and would have streamed
password hashes over `CredentialService/Watch`; the one thing stopping it was
D13 having taken the service off the wire for a different reason.

#### Both fixed, and the payday half was not hypothetical

In payday, `emitSecretOf` (`internal/pdgen/layers.go`) now writes a `Watch` wrapper that clears the column on the way out. `Robot`, in
payday's **own** reference app, declares both `secret:` and `watch:`, and the
test added there fails on the old generator with the secret in plaintext on the
wire. `lesomnus/payday@b57f9a1`, and the pin here moved with it.

roster installs both interceptors, in the pair `auth` is installed as two lines
above it in the same chain. Nothing registered is closed today, so this closes
no live hole -- it makes `closed` mean what its name says, for the service added
tomorrow that is both.

Pinned here by `cmd/watch_test.go`, because this app is the one with a password
hash in the column.

#### Why hidden and not refused

Refusing `watch:` on an entity with a `secret:` field was the other fix, and
this document previously said it was probably right, *since a stream that
silently omits a column is its own surprise*.

That was wrong, and item 4 is why. The first real subject of a sync channel is
**this person's credentials changed, stop trusting what you were told**, and the
row that changed is exactly the one holding a verifier. Refusing would make the
one thing worth watching the one thing that cannot be watched. And the surprise
argument does not survive contact: `Get` omits the column too, so a stream that
omits it is the consistent answer rather than the odd one.

`Delegation` still declares no `watch:`. Not because it could not now, but
because a schema that says so is better than a wiring that has to be remembered.

### F11 · A `secret:` field with no `list:` generated code that did not compile — **fixed**

`emitSecretOf` emitted the `List` wrapper unconditionally, with no `if e.List !=
nil` guard, so the generated `Secret` layer named `<E>ListRequest` and
`s.<E>ServiceServer.List` for an entity that declared no list. `pd gen`
succeeded and said nothing; `go build` failed inside a generated file nobody is
allowed to edit.

Fixed in `lesomnus/payday@b57f9a1`, in the same pass as F10. Its regression test
is an entity of that shape -- `Seal`, in payday's reference app -- and the test
is that the package **compiles**: there is nothing to assert, and it fails at
the only moment the bug ever showed itself.

It never blocked `Delegation`, which wants a `list:` anyway -- a page of them is
what a sweep reads and what an operator asking "what is live for this person"
reads.

### F12 · `pd doctor` did not read the schema — **fixed**

`CLAUDE.md` sells it as *what would go wrong, before it does*, and doctor's own
comment said it *reads the schema the way the generator does, so that everything
`pd gen` refuses is refused here too*. `doctorSchema` globbed payday's shipped
entity files and checked that the overlay **filenames** matched, and returned.
It never opened the app's own protos.

Checked: with an entity that `pd gen` refuses outright in place, `pd doctor`
printed *looks like an app that generates* and exited 0.

#### Fixed by making it true, not by deleting the sentence

The finding said either would do. Making it true is the one worth the work: the
sentence is why somebody trusts the exit code, and an app finding out from CI
what a local command could have said is the whole reason doctor exists.

`lesomnus/payday@9a252e5`. It takes the path the plugin takes -- one `buf build`
for a descriptor set, then `protogen`, the orm graph, and `pdgen.Read` -- rather
than parsing the files, which would be a second and worse compiler whose
findings were about its own gaps.

Two states it stays quiet in, and both are states where a finding would be
noise. An app with no `buf.lock` has never generated, so its imports do not
resolve and every app in that state has the same useless list -- and the next
thing they type is the `pd gen` that writes the lock. And a schema that does not
compile is buf's to report, in buf's own words with a line and a column.

Confirmed here: `proto/app/host.proto` moved onto `MailDomain`'s domain, and
`pd doctor .` said the same sentence `pd gen` says, before `pd gen` ran.


---

## Progress

| Phase | State |
| --- | --- |
| 0 · repo, plan, rules | **done** |
| 1 · schema — Site, Identity, Email | **done**, 15 tests, both databases |
| 1b · Team, on the second axis | **done**, 21 tests, both databases |
| 1c · memberships, Credential | **done**, 27 tests, both databases |
| 2 · payday fixes | **all closed** — F1, F2, F3, F4, F6, F8, F9, F10, F11, F12, F13, F14 fixed · F7 by D27 · F5 is operational and written down |
| 3 · app layer | linking rules, credential verification, roles and the second axis, `MeService`, escalation prevention, the console · **done** |
| 4 · keys, sync, console | **keys done** (both planes; no wire surface to mint an `rt_`) · **delegation done** (D25; `Vouch.Delegate` mints one over the wire and `Vouch.Revoke` ends it — what has no wire surface is `DelegationService`) · **console done** · sync channel — |
| 5 · the line, written down | **done** — D19, D20, and POSITION.md rewritten around them |

The phases above are how this was built. What is built **next** is
[docs/ROADMAP.md](docs/ROADMAP.md), which carries its own progress table.

### What is still roster's to build for a sign-in

D19 through D24 say where the line is. This is what is on the near side of it
and not written yet. None of these is decided; each takes a `D` when it is.
Where an entry names a preferred answer, that is a recommendation carried over
from the discussion that found it, not a decision taken.

These are **subjects and not pieces of work**, and reading them together shows
why: three of them are one table, two of them are one field each, and one of
them does not close what it says it closes. The order they are built in, what
forces it, and how far it has got are [docs/ROADMAP.md](docs/ROADMAP.md) --
kept there so that this list stays what it is, which is the question rather than
the schedule.

1. ~~**A tenant from a hostname.**~~ **Done**, D27. `Host`, and
   `FrontService.WhoseHost` to resolve one before anybody is anybody.

   The original entry: A multi-tenant app served at
   `acme.example.com` has to turn that into a tenant, and roster has no way to
   answer. It is an overlay on `Tenant` in `proto/ext/payday/`, since that
   entity is payday's.

   **This closes F7.** With the tenant known from the host, an address resolves
   to one person again, which is what "sign in with your email" and a magic link
   both need. Half of this list is waiting on it.

2. ~~**Home-realm discovery, by domain.**~~ **Done**, D27. `MailDomain` and
   `FrontService.WhereFrom`, hanging off the domain for the reason this entry
   already gave. What it names is a **provider**, not a connection -- a
   connection carries a secret and that is item 9, still undecided.

   The original entry: "Addresses at `@acme.com` go to Entra."
   Identifier-first sign-in is the thing every multi-tenant front door rewrites,
   and it is a fact about a tenant's domains.

   **It must hang off the domain and not off a person.** Answered per person it
   is the enumeration oracle D21 spent a condition avoiding; answered per
   domain it says nothing about any individual.

3. ~~**Recovery.**~~ **Done**, in two halves that turned out not to be one
   machine: `Vouch.Reset` for the air gap (D28) and `Vouch.Link`/`Redeem` for a
   channel (D31). D16's third leg is why they are two -- a transcribed code and
   a machine-carried token want different costs.

   Air gap still costs what this entry said it would: nothing sets
   `Email.date_verified`, so an address is unverified forever unless an operator
   asserts it. Nothing here changes that.

   The original entry:
   The same machine as a magic link — a single-use opaque nonce
   roster mints and roster checks, delivered by somebody else. Inside the line
   for the same reason, and it is where account takeover lives, so the rules
   belong beside the row rather than in each app.

   **In an air-gapped deployment the somebody else is a person.** There is no
   mail, so the code is read out or written down by a local operator — which
   makes recovery and an operator-initiated reset (10) the *same mechanism* with
   two ways of reaching somebody. D19 already put the delivery outside, and this
   is why that was worth separating.

   Air gap costs something else on the way past: nothing can set
   `Email.date_verified`, so an address is unverified forever unless an operator
   asserts it. D3 gave that field the job of deciding whether an address may be
   trusted at all, and here it can only ever say no.

4. **The sync channel, as an invalidation signal.** `Outbox`/`Drain` has been
   Phase 4 as "the sync channel" from the start. Its first real subject is
   *this person's credentials changed, stop trusting what you were told* — see
   6 below, which is the same feature.

   **The app dials roster and holds the stream**, rather than roster calling
   out. roster already speaks server streaming, and the direction is what keeps
   this small: no subscription URLs to manage, no outbound requests from roster
   to wherever an app said, and a reconnect in place of a retry-and-dead-letter
   machine. What is wanted is an event stream rather than an entity `Watch`,
   since a `Holder` changes for reasons nobody needs to hear about.

5. ~~**A breached-password check.**~~ **Done.** `vouch.Breached`, refused
   before anybody is read -- it is a fact about the secret rather than about the
   person, so the refusal must not depend on whether they exist.

   **A file and not a service**, because the deployment this app is most
   careful about has no network at all: the corpus is the format the well-known
   one is published in, and the lookup halves the file until a window is small
   enough to read. `sort -u` is enough to make one, and the order is checked at
   startup rather than trusted -- an unsorted file answers *no* to things that
   are in it, which is the quiet direction in the one feature whose whole job is
   to say yes.

   The original entry:
   roster is the only thing that sees the
   plaintext, so it is the only thing that can. Length and complexity rules are
   policy and stay with the caller; "this one is in a corpus of leaks" is a
   fact.

6. ~~**"Sign out everywhere", as a fact rather than a list.**~~ **Done**, D26.
   `Holder.date_invalidated`, `HolderService.Invalidate`, and a delegation
   issued before it is refused where it is looked up. What is left of this
   entry is what it always said belonged to somebody else: an app compares the
   value when it resolves a session.

   The original entry: A registry of every
   app's live sessions is a copy of state whose truth is elsewhere: it grows
   ghosts when an app dies, disagrees with the app's own store, and puts other
   people's browser metadata in roster.

   One timestamp on `Holder` does the whole job. *Everything issued before this
   moment is invalid* — signing out everywhere is one write, and an app compares
   it when it resolves a session. 3 requires it anyway, since a password reset
   that leaves old sessions alive is not a reset.

   **The timestamp is the truth and a delivered event is only a hint.** Anything
   pushed can be missed — a disconnect, a restart, an app that was down — and if
   the push were the mechanism, one lost message would be a session that never
   dies, with nothing anywhere saying so. Because the value travels and is
   monotonic, a duplicate is a no-op and an old one cannot un-revoke; what a
   missed message costs is latency rather than correctness.

   **roster answers "invalid since when". The app answers "what is still
   alive".** Each says only what it holds the truth of. A list with device names
   is the app's, per app; across apps it is what OIDC back-channel logout is
   for, and that is Hydra again.

7. ~~**A read that answers which methods somebody has**, without the
   verifier.~~ **Done**, in two halves. `MeService` answers a person's own,
   which is safe without a subject argument and is what a self-service screen
   draws. `HolderService.SignsIn` answers the same about **somebody else**,
   behind the wall and needing a role that names it, which is what an
   operator's list draws.

   Two RPCs and one pair of shapes: `SignInIdentity` and `SignInCredential` are
   named for what they describe rather than for the RPC that first answered
   with one, because two shapes saying one thing would be two that drift --
   and the drift would be between what a person sees about themselves and what
   an operator sees about them.

   The original entry:
   `CredentialService` is unregistered because its `Get` answers with the
   secret (D13), so nothing today can say "this person has a password and a
   TOTP" — which both a self-service screen and an operator's list need.
   `MeService` already does it for the caller; this is the same answer about
   somebody else, narrowed by the wall and by D23's token.

8. ~~**Refusing to remove a last login method.**~~ **Done**, in
   `server/core/identity.go`, as a layer for the reason this entry gave. What
   counts as a way in is an `Identity` **or** a `Credential`, since those are
   the two things a Login App and `VouchService` between them can turn into a
   signed-in person. Erasing the *person* is a different act and is not
   stopped.

   The original entry: Removing it locks somebody out of
   their own account, and no deployment would want that configured differently
   — so it is a layer, the way D17 put the built-in team rules in one rather
   than in a policy.

9. ~~**Per-tenant provider connections.**~~ **Done**, and the decision is the
   one this entry called likely: the connection is roster's and the secret is
   the deployment's, with a reference here.

   Not holding it is what makes D13 survive without an exception. Everything
   about a connection that varies per tenant is **public** -- which issuer,
   which client id, which scopes -- and the secret has to reach the front door
   whatever roster does, because using it means doing the exchange, which is
   being the relying party and is what D19 says roster is not. So roster stores
   a string it does not read, and what it means is the deployment's to know.

   The original entry:
   ...has a boundary question
   rather than a schema question. "acme uses Entra, beta uses Google" is a fact
   about a tenant and every app would otherwise hold a stale copy — but a
   connection carries a client secret, and handing one back would make it the
   first secret roster returns rather than checks. D13 is the entry it argues
   with. The likely answer is that the connection is roster's and the secret is
   the deployment's, with a reference here, but that is a decision and not an
   assumption.

10. ~~**A write surface for `Credential`.**~~ **Done**, D28. `Vouch.Reset` and
    `Vouch.Unlock`, plus the rule over `Vouch.Set`, which was already on the
    wire. What is left of this entry is creating a credential *alongside* a new
    `Holder` in one act, which is two calls today and works.

    The original entry: D13 closed the whole service — not
    registered, closed to the batch — so nothing on the wire can set a password,
    and `init` plus a shell is the only way. That is right for the read and
    wrong for the write, and an air-gapped deployment with a local operator per
    tenant needs three of them: **reset a password**, **release a lockout**, and
    create one alongside a new `Holder`.

    The answer is the shape D13 named when it closed the door: the
    `VouchService` trick, a narrow service that takes secrets in and never
    answers with one. Not reopening `CredentialService`, whose generated `Get`
    is the reason it is shut.

    A lockout releases itself after fifteen minutes (D14), so the operator's
    version is a convenience rather than a necessity — but it is also the answer
    to the limitation D14 recorded and could not close from here: *an account
    can still be held closed by somebody else.* A person on site can simply
    open it.

11. ~~**Escalation prevention over credential writes.**~~ **Done**, D28, and
    before the surface, which this entry insisted on. The rule taken is the one
    it recommended; the alternative it named is written down beside it.

    The original entry: Resetting somebody's
    password is a way to **become** them, so an operator who may reset anybody
    in their tenant effectively holds every permission in it — two operations,
    and it is the same shape as the hole `server/core/escalate.go` exists to
    close. That file covers `Role.Add`, `Role.Patch`, `Binding.Add` and an API
    key's methods, and not this, because this does not exist yet.

    So it goes in before the surface does, and the rule is the one already
    written: **you may only reset somebody whose permissions are a subset of
    yours.** Conservative on purpose, for escalate.go's own stated reason — the
    failure it produces is somebody being told they may not, which is a
    conversation, and the other direction is silent.

    The alternative is to accept it and say so plainly: a tenant operator is a
    tenant administrator. That is honest, and it makes "operator" a smaller
    word than the permission it carries.

12. ~~**A disabled state, which is neither a lockout nor an erasure.**~~
    **Done**, D26. `Holder.date_disabled`, `Disable`/`Enable`, refused in
    `cmd.Resolver` and in `vouch.Verify` -- so a token minted before the
    suspension stops working, which is the half this entry said was the point.

    The original entry: A lockout
    is temporary and automatic; `date_erased` is deletion. Nothing today says
    *this person is not to sign in, and their rows stay.* Somebody who left,
    somebody suspended — that is likely what a local operator reaches for most,
    and it is missing.

    It has to reach the apps, which makes it the same subject as 4 and 6: a
    session already issued outlives the row that stopped being allowed to have
    one — the argument `payday/holder.proto` uses to justify `watch: {}`.

### Open questions for whoever reads this next

- **The repository is private.** custody is public; making roster public was
  refused by a permission check rather than decided, so it is worth an explicit
  choice.
- **F3** above: whether payday should refuse the declaration that lies.
- **The second axis is demonstrated.** `Team` carries the edge, and a caller
  narrowed to one site sees one team out of two in the same tenant. D4 is no
  longer a claim.
- **`Sets` is the app's now.** `cmd.Sets` reads a caller's bindings and their
  team memberships, and `bare.Scopes{pd.Wall(), pd.Grouped(...)}` composes the
  two axes. Checked by removing `pd.Grouped`: a caller bound in one site sees
  the other's teams again.
- ~~**Escalation prevention is missing rather than rejected.**~~ It is there;
  `server/core/escalate.go`, and `cmd/policy_test.go:269` is the two-RPC
  scenario this entry described, refused.
- **A team member sees their team's **site**.** One second axis, and `Site` is
  it, so being in a team means seeing the site's rows. Narrower than that is the
  app filtering; see D17.
- ~~**`/api/v1/me` is not written.**~~ `MeService` is (proto/app/me.proto:36),
  and the console reads it: `ts/src/page.tsx` asks it what the operator may do
  before deciding what to draw.
- **Credential verification is done** — `server/vouch`, D13 and D14. What is
  *not* done is what happens after it says yes.
- ~~**Nothing mints an `rt_` key over the wire.**~~ **Done**, D48, and not with
  a new service: `IssueService` already was the narrow service that takes a
  secret in and answers with it once, and it is now served on the data plane
  too, minting the customer's kind. The prefix is a fact about which server
  answered rather than a field, and the rules are `core.ApiKey.Add`'s --
  nothing hands out a method it does not hold, and nothing writes a way into an
  account wider than its own.
- ~~**And nothing mints a delegation over the wire either.**~~ **Done**, and not
  in the shape this bullet predicted: it rides back on `VouchService.Delegate`
  rather than on `Verify` growing an answer, because a role here is a list of
  methods and the two are different things to hand out. D23 and D25.
- ~~**Two-step verification is decided and not written.**~~ **Done**, D29 and
  D30: `Vouch.Continue`, the `Continuation` entity, one lockout count metered
  across both steps, and the single-use spend D34 had to go upstream for.
  D21's four conditions are what the shape was checked against.
- ~~**Magic link is inside the line and is not written.**~~ **Done**, D31.
  `Vouch.Link` and `Vouch.Redeem`, and a person with a second factor is still
  asked for it. F7 stopped blocking it when D27 gave an address a tenant to be
  unique within.
- ~~**The console's sessions are in `MemStore`**~~ **Done.** They are a table --
  `server/session`, behind `authsession.Store` -- so a cookie minted by one
  replica resolves on another. And the **watch broker**, which is the same shape
  one seam over, crosses replicas once it is named: `watch.broker: postgres`.
  See D33.
- ~~**`Audit` has no retention policy.**~~ **Done**, D44. Two clocks -- how long
  a row stays in the database and how long the record exists at all -- with the
  archive between them, applied by `serve` and by `roster trail`. Forever is
  still the default, and a window with nowhere to put what leaves it is refused
  at startup rather than obeyed.
- **Erasing one person from the trail is not written, and it is a decision
  first.** The policy above is about age and reaches everybody at once. A
  right-to-erasure request is about a subject, and what it should blank is the
  open part: the contents of writes about them, their identifier as an actor, or
  both. Blanking the actor destroys *who did this*; keeping it keeps an
  identifier that is itself personal data. The two answers in the field are to
  null the contents and keep the event -- the distinction `audit.proto` already
  draws for the hard erase -- or to encrypt per subject and destroy the key. It
  is the deployment's obligation that decides, and roster does not know what
  that is. D44.

### What "after it says yes" means, since it came up

roster answers whether a secret is somebody's. It does **not** issue a session,
and should not: it is called by machines, has no browser, no cookie domain and
no CSRF story. The session belongs to whatever the browser talks to.

Which makes the shapes:

| | needs Hydra? |
| --- | --- |
| one app, its own login | **no.** The app calls `Vouch.Verify`, sets its own cookie, and its `auth.Resolver` reads it back |
| several apps, one sign-in | **yes.** App A's cookie means nothing to app B, and a signed credential with an issuer, a JWKS, expiry and revocation *is* OIDC |

So the boundary is not id/pw versus OIDC — it is **one relying party versus
many**. An air-gapped single-app deployment needs no Hydra and no token.

**That half is written now**, and it is payday's: `auth/authsession`, beside
`authoidc`. It is the shape this entry predicted — a handler that mints the
cookie and an `auth.Handler` that reads it back into an `auth.Identity` — and it
did not re-introduce the deleted `auth.Issuer`, for the reason given here and
restated as D19.

What it actually is, since "session" is a word people fill in differently:

- the cookie value is **32 bytes from `crypto/rand`**, base64url. No claims, no
  signature, no expiry a client can read
- the `Session` row — actor, tenant, `frame.Grant`, absolute expiry, idle — is
  in a `Store` the serving app owns. **Revoking is a delete**, and it is
  immediate
- two clocks, and payday argues for both: an idle timeout under an absolute cap
- the cookie is `__Host-` prefixed by default, so a deployment that gets the
  attributes wrong finds out at the browser rather than later
- `Verify` is the one thing the app supplies, and payday's comment names this
  deployment: *"In a deployment with roster behind it this is one call to
  `VouchService.Verify` and nothing else."*

**Which means an opaque session key is worth nothing to a second app**, by
construction. That is not a shortcoming to fix here; it is the reason the table
above ends in Hydra, and D19 is why roster does not go there instead.

Two things this leaves:

- ~~**`MemStore` is the only store payday ships**~~, and its own comment says it
  is right for one replica and *silently wrong* for two. **Taken**: a table with
  an index on the key is what replaced it, in `server/session`, and roster is an
  app that makes tables. `authsession.Store` was already the seam; nothing in
  payday had to change, which is what a seam being real looks like.
- **A product app should not have to write a login endpoint.** The seam is a
  `Verify`, and roster is already meant to be imported (D10). An exported
  `authsession.Verify` backed by `VouchService` would make custody's whole
  sign-in one line, with no new service and no new network surface. That is the
  right answer to "does every app really have to care about cookies" — a
  package, not an endpoint.
