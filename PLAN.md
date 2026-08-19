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
| `date_disabled` | `cmd.Resolver`, and `vouch.Verify` | the resolver is where every credential that resolves to a holder arrives -- a session, an `rt_`, a delegation -- and it never sees a password. `vouch` is where somebody signs in and there is no frame yet |
| `date_invalidated` | the credential's own lookup | the resolver sees the holder and not the credential, so it cannot know **when** the thing in front of it was issued. Only the row does |

That split is the whole of why this is two changes and not one.

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
6. extract the components

Six is last because extracting first means guessing what to extract. What 4 and
5 turn out to need is the specification, and it is not knowable in advance.

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
- ~~**Escalation prevention later.**~~ Done, and wider than this entry
  expected: `server/core/escalate.go`, on `Role.Add`, `Role.Patch`,
  `Binding.Add` and an API key's methods, plus the rule that a role scoped to a
  site is bound only in that site.

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

### F10 · `pd.Secret` does not cover `Watch`, and nothing closed can be a stream — **open**

Two gaps that compose, and together they mean **a watchable entity with a
verifier streams the verifier**.

- payday's `Secret` layer writes wrappers for `Add`, `Get`, `Patch`, `Apply` and
  `List`. There is no `Watch` override, so a `WatchItem` carries the whole
  message -- and a `WatchRequest` has no `select` to leave a column out of.
- roster installs `grpcx.ClosedUnary` and never `ClosedStream`, so `closed`
  structurally cannot shut a streaming method. `grpcx.Closed` exists and is not
  used.

Which leaves **not registering the service** as the only control that covers a
stream at all. `Credential` declares `watch: {}` today and would stream password
hashes over `CredentialService/Watch`; the one thing stopping it is D13 having
taken the service off the wire for a different reason.

`Delegation` therefore declares no `watch:`, and says so in the schema rather
than relying on this being remembered.

Two fixes, one each side. payday: emit a `Watch` wrapper in `emitSecretOf`, or
refuse `watch:` on an entity with a `secret:` field -- the second is the loud
one and is probably right, since a stream that silently omits a column is its
own surprise. roster: install `grpcx.Closed` instead of `ClosedUnary`, so that
`closed` means what its name says.

### F11 · A `secret:` field with no `list:` generates code that does not compile — **open**

`emitSecretOf` emits the `List` wrapper unconditionally, with no `if e.List !=
nil` guard, so the generated `Secret` layer names `<E>ListRequest` and
`s.<E>ServiceServer.List` for an entity that declared no list. `pd gen` succeeds
and says nothing; `go build` fails inside a generated file nobody is allowed to
edit.

It did not block `Delegation`, which wants a `list:` anyway -- a page of them is
what a sweep reads and what an operator asking "what is live for this person"
reads. So this is written down rather than worked around, and the fix is four
characters in payday.

### F12 · `pd doctor` does not read the schema — **open**

`CLAUDE.md` sells it as *what would go wrong, before it does*, and doctor's own
comment says it *reads the schema the way the generator does, so that everything
`pd gen` refuses is refused here too*. `doctorSchema` globs payday's shipped
entity files and checks that the overlay **filenames** match, and returns. It
never opens the app's own protos.

Checked: with an entity that `pd gen` refuses outright in place, `pd doctor`
printed *looks like an app that generates* and exited 0.

So `pd gen --check` is the only schema gate, and doctor is for missing go tools,
a mis-named overlay, and a layer with no `WithDriver`. Either make it run
`pdgen.Read` or delete the sentence that says it does; the sentence is the part
that costs something, because it is what makes somebody trust the exit code.

---

## Progress

| Phase | State |
| --- | --- |
| 0 · repo, plan, rules | **done** |
| 1 · schema — Site, Identity, Email | **done**, 15 tests, both databases |
| 1b · Team, on the second axis | **done**, 21 tests, both databases |
| 1c · memberships, Credential | **done**, 27 tests, both databases |
| 2 · payday fixes | F1, F2, F4, F9 done · F7 closed by D27 · F3, F6, F10, F11, F12 open · F5 written down |
| 3 · app layer | linking rules, credential verification, roles and the second axis, `MeService`, escalation prevention, the console · **done** |
| 4 · keys, sync, console | **keys done** (both planes; no wire surface to mint an `rt_`) · **delegation done** (D25; no wire surface either) · sync channel, console — |
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

3. **Recovery.** The same machine as a magic link — a single-use opaque nonce
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

5. **A breached-password check.** roster is the only thing that sees the
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

7. **A read that answers which methods somebody has**, without the verifier.
   **Half done** in P3: `MeService` answers a person's own identities and
   credential kinds, which is the half a self-service screen needs and the only
   half that is safe without a subject argument. What is left is the same
   answer **about somebody else**, for an operator's list, narrowed by the wall
   and by a delegation.

   The original entry:
   `CredentialService` is unregistered because its `Get` answers with the
   secret (D13), so nothing today can say "this person has a password and a
   TOTP" — which both a self-service screen and an operator's list need.
   `MeService` already does it for the caller; this is the same answer about
   somebody else, narrowed by the wall and by D23's token.

8. **Refusing to remove a last login method.** Removing it locks somebody out of
   their own account, and no deployment would want that configured differently
   — so it is a layer, the way D17 put the built-in team rules in one rather
   than in a policy.

9. **Per-tenant provider connections**, and this one has a boundary question
   rather than a schema question. "acme uses Entra, beta uses Google" is a fact
   about a tenant and every app would otherwise hold a stale copy — but a
   connection carries a client secret, and handing one back would make it the
   first secret roster returns rather than checks. D13 is the entry it argues
   with. The likely answer is that the connection is roster's and the secret is
   the deployment's, with a reference here, but that is a decision and not an
   assumption.

10. **A write surface for `Credential`.** D13 closed the whole service — not
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

11. **Escalation prevention over credential writes.** Resetting somebody's
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
- **Nothing mints an `rt_` key over the wire.** A data-plane holder's key
  resolves to that holder and is narrowed by their own permissions, and
  `TokenService/Introspect` already serves it to product apps — so the half that
  answers is done and the half that issues is a shell (`cmd/key.go`,
  OPERATING.md). What it needs is the `VouchService` trick: a narrow service
  that takes a secret in, answers with the plaintext exactly once, and can never
  read one back.
- **And nothing mints a delegation over the wire either**, for a different
  reason: D24 puts the page that would ask for one before the RPC that answers.
  `keys.Delegate` is the mint and it is a Go call; D23 says where it belongs,
  which is riding back on `VouchService.Verify`.
- **Two-step verification is decided and not written.** D20 and D21 say what it
  is — a `Credential` row, a `continuation` handle, one lockout count across
  both steps — and nothing implements it. D21's four conditions are the ones
  that are cheap to leave out and expensive to find.
- **Magic link is inside the line and is not written.** D19 admits it — an
  opaque single-use nonce roster mints and checks, with delivery somewhere else
  — but F7 blocks the usual front door for it, since most links are asked for by
  typing an address.
- **The console's sessions are in `MemStore`** (`cmd/serve.go`), which is right
  for one replica and silently wrong for two. See the "after it says yes"
  section below; this is a table.

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

- **`MemStore` is the only store payday ships**, and its own comment says it is
  right for one replica and *silently wrong* for two. `cmd/serve.go` uses it for
  the console. A table with an index on the key is what replaces it, and roster
  is an app that makes tables.
- **A product app should not have to write a login endpoint.** The seam is a
  `Verify`, and roster is already meant to be imported (D10). An exported
  `authsession.Verify` backed by `VouchService` would make custody's whole
  sign-in one line, with no new service and no new network surface. That is the
  right answer to "does every app really have to care about cookies" — a
  package, not an endpoint.
