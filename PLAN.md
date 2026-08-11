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

Hydra, the Login App, `AuthProvider` implementations (Entra/LDAP/SAML/magic
link), MFA flows, the login UI, the session proxy. Devices, certificate
authorities, ownership transfer — those belong to the product.

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

The unique key is the pair `(provider, subject)`, and `subject` is whatever that
provider calls immutable — a numeric ID for GitHub, `objectGUID`/`entryUUID`
for LDAP. Never a username and never an email.

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
    Binding  holder=2?  site=3?  role=8  group=9?
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
- **Escalation prevention later.** Nobody should be able to grant what they do
  not hold, and it is not the first thing to build.

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

**The actor on the frame is the key**, not the Holder it hangs off and not
`pdid.Nil`. The trail then names which key asked, revoking is a delete, and no
person-row is involved -- which is what `frame.Everything` warns about: *a
privilege granted by being a particular row cannot be revoked, cannot be
narrowed, and belongs to whoever finds the row.* A key row is the opposite case:
it exists to be revoked.

`Id.Domain()` is how the resolver tells the two apart before it reads anything.
An identifier says what kind of thing it names, so a caller that is a key and a
caller that is a person are distinguishable without a lookup.

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

### F3 · A non-nullable message field lies about presence — **open**

The one above (D6) as a payday question rather than a roster one. A
`google.protobuf.Timestamp` with no `nullable`, no `default` and no marker
generates a NOT NULL column, while the API it generates beside it has `Has…`.
The two cannot both be true, and the caller is told a value it never set is set.

It should be a generation failure, the way everything else that fails quietly
is: *this field has presence in the API and nowhere to keep it — say
`nullable: true` or give it a default.* Not attempted yet, because the rule has
to leave `date_created` (`default: ""`), `date_updated` (`version: {}`) and
`date_erased` (`erased: {}`) alone, and getting that boundary wrong breaks every
existing schema.

### F6 · A schema cannot say "written, never read" — **fixed upstream, not adopted here**

payday has `(payday.field).secret` now, and `pd.Secret` is the generated layer
that clears a marked field on the way out. apptest declares one and the test
fails with the layer removed.

roster cannot use it yet. The extension is in payday's **buf module**, and this
app's `buf.yaml` depends on the published `buf.build/payday/payday:dev` -- so
until that is pushed, `(payday.field)` is an unknown extension here and the
compile refuses. Adopting it is one line per field and one in the stack, on the
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

### F7 · Signing in by address has no answer yet — **open**

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

---

## Progress

| Phase | State |
| --- | --- |
| 0 · repo, plan, rules | **done** |
| 1 · schema — Site, Identity, Email | **done**, 15 tests, both databases |
| 1b · Team, on the second axis | **done**, 21 tests, both databases |
| 1c · memberships, Credential | **done**, 27 tests, both databases |
| 2 · payday fixes | F1, F2, F4 done · F3, F6, F7 open · F5 written down |
| 3 · app layer | linking rules, credential verification, **roles and the second axis** done · `/api/v1/me` next |
| 4 · keys, sync, console | **keys done** · sync channel, console — |

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
- **Escalation prevention is missing rather than rejected.** Nothing stops
  somebody binding a role they do not hold themselves.
- **A team member sees their team's **site**.** One second axis, and `Site` is
  it, so being in a team means seeing the site's rows. Narrower than that is the
  app filtering; see D17.
- **`/api/v1/me` is not written.** It needs an overlay RPC. `VouchService` is
  now the worked example of one, so this is no longer the first of its kind.
- **Credential verification is done** — `server/vouch`, D13 and D14. What is
  *not* done is what happens after it says yes.

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

The half that is missing is payday's, and payday already left the seam: `web.New`
answers a mux the app mounts on, and `cmd.serveHttp` carries a commented
`h.Handle("/login", …)` saying exactly this. What is not written is an
`auth/authsession` beside `authoidc` — a handler that mints the cookie and an
`auth.Handler` that reads it back into an `auth.Identity`. It does not
re-introduce the deleted `auth.Issuer`: that minted tokens **other parties
verify**, which is an IdP's job, and a session cookie is an opaque handle
meaningful only to the server that minted it. The store it needs carries the
`broker: memory` trap exactly — right for one replica, silently wrong for two.
