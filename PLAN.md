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

---

## Progress

| Phase | State |
| --- | --- |
| 0 · repo, plan, rules | **done** |
| 1 · schema — Site, Identity, Email | **done**, 15 tests, both databases |
| 1b · Team, on the second axis | **done**, 21 tests, both databases |
| 1c · memberships, Credential | **done**, 27 tests, both databases |
| 2 · payday fixes | F1, F2, F4 done · F3 open · F5 written down |
| 3 · app layer | linking rules **done** · `/api/v1/me`, policy, credential verify next |
| 4 · keys, sync, console | — |

### Open questions for whoever reads this next

- **The repository is private.** custody is public; making roster public was
  refused by a permission check rather than decided, so it is worth an explicit
  choice.
- **F3** above: whether payday should refuse the declaration that lies.
- **The second axis is demonstrated.** `Team` carries the edge, and a caller
  narrowed to one site sees one team out of two in the same tenant. D4 is no
  longer a claim.
- **`Sets` is still handed in by the test, not by the app.** `cmd.Build`
  installs the wall only. The membership table it should read now exists, so
  wiring `pd.Grouped` over `SiteMembership` is the next real step.
- **`/api/v1/me` is not written.** It needs an overlay RPC, which is the first
  thing here that is not plain CRUD.
- **Credential verification is a schema and no behaviour.** The row is there;
  the RPC that compares a secret without handing it out is not.
