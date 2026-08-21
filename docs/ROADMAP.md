# Building it — the order, and how far it has got

`PLAN.md` is what roster is and why, and its list of twelve is the **subjects**
that are still on the near side of D19's line. It deliberately takes no
decisions and states no order.

This file is the other half: **the order, the dependencies, and what is done.**
When the two disagree about a subject, PLAN.md is right; when they disagree
about a sequence, this one is.

## What the list turned out to be

The twelve items were written as they were found, so they are twelve subjects
and not twelve pieces of work. Reading them together, four collapse.

**Three of them share a test, and it turned out not to be a table.** D21's
`continuation`, D23's delegation token and item 3's magic-link nonce are each
described with the same four words — opaque, short-lived, single-use, bound to
the caller it was issued to — and D19's question has one answer for all three.
This file said they were therefore one entity with a `kind`, and D16 refutes
that in its own words:

- **What is proved is not what is granted.** The delegation token carries
  `methods`, read by the interceptor before the handler; D23 calls it *an `rt_`
  key with a short life*. The continuation carries `satisfied` and grants
  nothing, and must never be a bearer at all — D21 calls it *a bearer credential
  for a half-proven identity, and the only thing that makes that acceptable is
  that it is barely alive*. One table means a `methods` column that is
  load-bearing for one kind and must be empty for the others, which is an
  invariant no schema states.
- **The kind selects the cost.** A delegation token and a continuation are 256
  bits from `crypto/rand`, so a fast deterministic hash is right. An air-gapped
  recovery code is *read out or written down by a local operator* — short, and
  therefore argon2 with an attempt counter and a lockout. That is `Credential`'s
  machinery, not `ApiKey`'s, and it is the third leg of D16 landing exactly
  where D16 said it would.

So the consolidation was wrong and the category is real. What follows from it is
smaller and better: **P1 builds the delegation token and nothing else**, which
is also what D24 put first and what D24 says must be specified by a page rather
than reasoned about.

**Two of them are two fields, and a third is already half-built.** Item 6 (an
epoch: everything issued before this is void) and item 12 (disabled) are
timestamps on `Holder`, and payday leaves this app exactly two free numbers
there — 11 and 12. Item 4 wants them to travel, and `Holder` already declares
`watch: {}`, justified in `holder.proto` for precisely this: *the one fact about
somebody that has to travel is that they are gone.* So item 4's first increment
is a field arriving on a stream that already exists. The event stream it argues
for is a second increment, taken when the noise is measured rather than
predicted.

**Two of them hang off a domain.** Item 1 (a tenant from a hostname) and item 2
(home-realm discovery) are both lookups by a name, and `holder.proto` already
wrote the rule that decides their shape: *anything that has to be looked up goes
flat beside it* — which is the reason `Identity` is an entity and `Profile` is
not. So they are entities, not repeated fields on `Tenant`. Two of them, because
LOGIN.md is explicit that a hostname and an email domain answer different
questions, and because they differ in the two ways that matter: one is unique
across the deployment and read without a tenant, the other is unique within one.

**And one of them does not do what it says.** Item 1 claims to close F7. It does
not, on its own: `Email` is unique on `(holder, address)`, so two people **in
the same tenant** may hold one address and knowing the tenant still does not
resolve it to a person. What closes F7 is that plus a unique `(tenant,
address)` — which needs a stamped tenant column, which is a mechanism this
schema already uses. `Identity` has `stamp: "tenant_id"` for the same reason.
D3's consultant keeps everything: that case is across tenants, and this
constrains within one.

## The order

Each phase names what forces its position. Anything not named is free to move.

### P1 · `Delegation`, and the prefix that reaches it

**Forces the order:** D24 names it as the one thing everything else is built
wrong without.

Modelled on `ApiKey`, which has already argued every question this asks:
256 bits from `crypto/rand`, a **deterministic unsalted hash with a unique
index** because the verifier is also how the row is found, `(payday.field) =
{secret: true}` so the trail does not keep a second copy, and the generated
service closed the way `CredentialService` is.

It is close enough to `ApiKey` that the comparison has to be made rather than
skipped, and D23 makes it: *practically it is an `rt_` key with a short life,
minted for the person an app just authenticated*. What keeps it a second entity
is that an `ApiKey` is a thing a **person** names and manages — `alias` is
unique per holder and is *what somebody calls this key when deciding whether to
revoke it* — and a delegation token is minted per sign-in by an app the person
never sees. One list would be full of rows nobody named, which is the screen
item 7 and D24 §5 are for.

Three things that are not `ApiKey`'s:

- **`issuer` cannot be an edge.** D21 requires a ticket to be bound to the
  caller it was issued to, and that caller is a **control-plane** holder while
  the ticket lives in the data plane. D15 put a database boundary between them
  and said there is no query from one to the other, so this is an opaque
  identifier held in a column and compared, not a foreign key. A schema cannot
  say it, which makes it a layer's job.
- **Expiry is enforced on read.** A sweep that is the mechanism is a sweep whose
  outage is a security incident. The sweep collects garbage; the read decides.
- **Single-use is a compare-and-swap**, for D14's reason one row over: two
  concurrent uses must be one success.

The prefix is not a new idea. OPERATING.md already says *the prefix decides
which database holds the row and who the token is served as*, so a third value
is that rule's next entry rather than an exception to it — and it is that rule's
next entry only because this is one kind of thing. It resolves to the holder,
exactly as `rt_` does, and is never wider than they are.

The issuer binding cannot live where the lookup does. `auth.TokenStore.Lookup`
is handed the token and nothing else — no caller, no peer, no frame — so a
comparison written there compiles, runs and binds nothing. It goes where a frame
exists: `Introspect`, and whatever mints one.

### P2 · Two fields on `Holder`, and the refusals that make them mean something

**Forces the order:** item 3 needs the first one anyway — a password reset that
leaves old sessions alive is not a reset.

Item 6 (an epoch) and item 12 (disabled), at the two numbers payday leaves free.
Both timestamps and neither a bool: item 6's correctness argument *is*
monotonicity, and item 12 rides the same stream, where a flag that flips has no
such safety and cannot answer *since when*.

A column is inert on arrival. Nothing in the wall, the gate or the erased
machinery reads a new timestamp, so the phase is not done when the field
generates — it is done when `vouch` and `cmd.Resolver` refuse, which is the same
pair of paths F9 was about.

### P3 · The reference app's spine (D24 §2)

**Forces the order:** D24 exists to *specify* rather than demonstrate, and the
token's lifetime, scope and refresh point are decided by a page that uses it.

Grown from `examples/sso`. The design was stress-tested against the code before
any of it was written, which found one defect in what P1 had already shipped
(now fixed: a delegation is not a bearer credential) and reshaped the rest of
this phase. What follows is what survived.

#### What P3 has to decide, because D25 left it

**The lifetime, and it is longer than it looks.** `keys.DelegateFor` is 15
minutes with a comment saying *provisional*, and D23's *refreshed by signing in
rather than by extending* was read as "and therefore there is no refresh". Under
a 12-hour session with a 30-minute idle clock, that is a re-authentication
prompt several times an hour to read your own addresses. Nobody ships it, and
there is nothing to refresh with: the app must not keep the secret.

What changes the answer is the fix above. D21's *barely alive* was the argument
for a **standalone** bearer. A delegation is now half of a pair whose other half
is the app's own key -- so one that leaks is worth nothing to anybody who does
not already hold the key, and anybody who does is already past every wall this
has. So: **the delegation's expiry is the session's**, never longer, and
revocation rather than the clock is the lever.

**Which means a revoke has to exist.** D23 says *revoking it is a delete* and
there is no delete: `DelegationService` is unregistered and in `closed`, and
nothing outside `keys` touches the table. Today a person clicks sign out, the
session row goes, and the delegation the app holds stays good. The narrow shape
is the one caller who can prove they hold it -- the issuer, presenting it, the
same pair `Acting` already reads.

#### What P3 pulls forward from P6, because the page cannot be built without it

A delegation narrows **methods**, not rows. So "my identities" through one is
`IdentityService/List` answering with the whole tenant and the app filtering --
which is precisely the leak D23 exists to remove, reinstated inside the app
written to refuse it. And `MeService` cannot answer instead: `MeGetResponse` has
emails and no identities.

There is a second wall behind that one. A delegation resolves to the holder, so
`policy.May` wants a `Binding` for every method except the one `aboutYourself`
waives -- and `sso.Enrolling` creates a Holder and no binding. So the only thing
a delegation can call in the reference app today is `MeService/Get`, which is
the read that has no identities in it.

**So item 7 comes here rather than in P6**: identities on `MeGetResponse`. It is
the only shape narrowed to the person by construction, which is why
`aboutYourself` can waive it at all.

#### And the sign-in it rides on is a specimen, deliberately

`examples/sso` signs people in with OIDC and never calls `Vouch`, so there is
nothing for a delegation to ride back on in its main flow. D23 already recorded
this and said what to do: *a deployment with Hydra in front does not call
`Vouch` at all... Anything built on this should assume the `Vouch` case first
and leave the seam.*

So P3 adds a password route to the reference app, and says in `sso.go` that the
my-record page is reachable from it and not from the OIDC half. Exchanging an
`id_token` for a delegation is the seam, and it is a D19 question -- roster
accepting somebody else's assertion as proof -- so it takes its own entry rather
than being slipped in here.

#### Three things about the mint, found before they were written

- **A separate RPC, not a field on `Verify`.** D26 just argued three methods
  rather than one field for the reason that applies here unchanged: a role is a
  list of methods, so granting `VouchService/Verify` would be granting the power
  to mint a person-scoped credential, with no way to tell a Login App (which
  must never mint) from a product app (which must). The alternative it is scored
  against is not verify-then-delegate -- which is two hashes and two lockout
  counts -- but `Delegate(who, secret, methods)` **instead of** `Verify`: one
  round trip, one hash, one lockout, sharing the verification path verbatim.
  It also removes an overload, since on a field an empty method list has to mean
  "do not mint" while `keys.Delegate` refuses an empty list outright.
- **Any check on what may be minted goes before the secret is compared.**
  Evaluated after, a caller that over-asks gets one answer for a wrong password
  and another for a right one -- D14's equal-cost refusal, undone as a status
  code, which is worse than a timing leak because it is exact.
- **The mint is a write on the sign-in path**, which `vouch.passed` goes out of
  its way to avoid -- *every successful sign-in would otherwise be a write, for
  a fact that did not change*. It is now a row, a version, an audit entry and a
  watch event per sign-in. Worth knowing before it is measured rather than
  after, and it is what makes the sweep below not optional.

#### What P3 owes the table it filled

**A sweep.** Expiry is enforced on read, deliberately, and nothing collects the
rows -- so the table grows by one row per sign-in forever. `spin.Run` already
carries the outbox drain and is where this goes.

### P4 · A tenant from a hostname, a domain to a provider, and F7

**Forces the order:** D24 §3 puts it here, on the grounds that it is untestable
until something is actually served at a customer's name — which P3 is.

Three pieces: the hostname entity (global, read without a tenant, through the
unwalled server for the same reason `cmd.Resolver` and `vouch.Verify` are —
working out who somebody is cannot require knowing who they are), the mail
domain entity (within a tenant), and `Email` gaining a stamp and a unique
`(tenant, address)`.

The third has a **migration**: a deployment already holding two people with one
address in one tenant cannot take the index. That is the real cost of this
phase.

> **If the air-gapped deployment is what matters first, P5 goes before P4.** It
> does not depend on it at all — an operator names somebody by alias, not by
> address.

### P5 · Item 11 before item 10

**Forces the order:** stated in PLAN.md item 11, and it is the only pair in the
list where the order is a correctness question rather than a convenience.
Resetting a password is a way to become somebody, and `escalate.go` does not
cover it.

Then the write surface: reset, release a lockout, create alongside a new
`Holder`. The shape is `IssueService`'s, which already exists — on the control
plane only, wired at `console.Issue`. Extending it to the data plane is this
phase, and it is where `rt_` finally gets minted over the wire.

### P6 · The reads a screen needs (items 7, 8) and the screens (D24 §4, §5)

**Depends on P2** for the token every one of those reads is narrowed by.

### P7 · Two-step verification (D20, D21)

Stress-tested before any of it was written, which found that the obvious shape
breaks four things and that one of its stated dependencies does not exist. What
follows is what survived, and it is **four increments** rather than one.

#### What the obvious shape gets wrong

- **The lockout does not span the steps.** D21's fourth condition -- *one count
  across `Begin` and `Continue`, or the second factor is an unmetered guessing
  surface reached by passing the first* -- was to be satisfied by "the counter
  is on the `Credential` row". It is on **a** `Credential` row, and the index is
  unique on `(holder, kind)`: the password row and the totp row carry two
  counters. So ten wrong codes lock the second factor and leave the first
  untouched, and a fresh first factor costs nothing because a **successful**
  verify is never counted. The answer is that `Continue` counts its failures
  against **the row the first step used**, so exhausting the second factor
  closes the door the attempt came through.
- **`ok` would mean two things, and the difference fails open.** D21 forbids
  roster deciding sufficiency, so `ok` on a first step that passed has to mean
  *this factor was proved* -- and every caller in the tree reads it as *signed
  in* and mints a session. An app that does not read the new fields signs people
  in on one factor, silently, in the open direction.
- **A two-step sign-in cannot end in a delegation.** Nothing would mint one:
  `Begin`/`Continue` answer no token, and calling `Delegate` afterwards is the
  shape `vouch.proto` refuses in as many words -- two hashes and two lockout
  counts, or a credential for somebody nobody just proved.
- **There is no way to enrol a second factor**, which P7 was written as
  depending on P5 for. `Vouch.Set` argon2-hashes whatever it is handed, which is
  the one thing a TOTP seed must not be, and `Vouch.Reset` refuses a non-password
  kind in as many words. That sentence is wrong now: a seed **is** the sensible
  thing to generate and it **is** read out, as a QR code.

#### The shape that survives

> One new RPC. `Verify` and `Delegate` grow.

`Continue(continuation, kind, secret)` is new, because proving a second factor
is a distinct thing for a role to name and it takes a continuation rather than a
`who`. `Verify` and `Delegate` grow the answer -- `satisfied`, `available`,
`pending`, `continuation` -- and **mint a continuation only when there is more
to prove**, so a deployment with one factor pays exactly what it pays today and
the single-factor path stays one round trip. Minting stays on `Delegate`, which
takes a continuation in place of `who`+`secret`.

That also fixes the fail-open: an app gates on **the token being present**
rather than on a boolean, so one that ignores the new fields fails closed.

`ok` is never set on a response carrying a continuation. They are mutually
exclusive.

#### Four increments

1. **`Credential` grows up.** `alias` at field 4 and the index at
   `(holder, kind, alias)` -- because *one of each per person* is right for a
   password, defensible for TOTP and wrong for WebAuthn, where registering a
   backup authenticator is the standard advice. It costs nothing now and is a
   migration later. Plus a **last accepted step**, because D20 requires that a
   spent TOTP code not work twice and there is nowhere to record it.

   And verification becomes **per kind**, which is the finding that would have
   been hardest to see: `Burn` costs one argon2 unconditionally, so the moment a
   TOTP compare is a microsecond, *this person has no second factor* costs 40ms
   and *wrong code* costs nothing -- D14's equal-cost property inverted into a
   cleaner oracle than the one it replaced.

2. **TOTP, and the first secret roster must read back.** A seed is not a hash,
   so it is stored wrapped with a deployment key. Enrolment generates it,
   answers once with the seed and an `otpauth://` URI, and the factor does not
   count until one code has verified -- a mis-scanned QR discovered when
   somebody is already half signed in is the failure to avoid.

3. **The attempt.** `Continuation`: `Delegation`'s shape, `issuer` at 10 for the
   same reason, no `date_used` -- spending it is an erase, and *used* is *not
   there*, which is `Undelegate`'s answer and one mechanism rather than two. Its
   lifetime is **roster's and fixed**: D25's *the caller names it* was carved out
   for a credential that is half of a pair, and a continuation is exactly the
   standalone bearer that argument was carved out from.

4. **The reference app's half-session.** payday already anticipated the shape --
   `authsession.Session.Expires` may be set by a `Verify`, *which is how an app
   gives a short session to somebody who has not finished a second factor*. So
   `POST /session` answers the first form's result and a short cookie, and a
   second route spends it. Two shapes in one app is also the second consumer
   D24 §6 was waiting for.

### P8 · Recovery and the magic link (item 3)

**Depends on P1** (the nonce), **P2** (the epoch), and **P4** for the front door
that most links are asked for through. In an air gap it is P5's operator instead
of the mail, which is the same mechanism reached differently.

### P9 · The rest, in no forced order

The event stream (item 4's second increment), the breached-password check (item
5), per-tenant provider connections (item 9, which needs a decision first), and
extracting the components (D24 §6, last for D24's own reason).

**§6 is done, and it answered smaller than D22 guessed.** The Go half is the
whole of it -- `frontdoor`, which is the two forms, the half session, the
delegation held beside it and the header a call to roster rides on, lifted out
of the reference app and imported back into it. The browser half is **one
module and not a component library**: what the two screens that now exist share
is the protocol -- three answers where a page expects two -- and none of their
markup, because one is a person's page in plain HTML and the other an
operator's in React over a different transport. D24's reason for putting it last
held; it just turned out that what there was to extract was smaller than the
guess it was protecting against.

**The console's session table is done** — it was the one item here that blocked
running the console rather than improving it, since a cookie minted by one
replica had to resolve on another.

It did not, on its own, make roster horizontally scalable, and this paragraph
used to read as though it had. An audit of every other piece of process-local
state found the rest already externalised -- keys, delegations, failure counts,
lockouts, the TOTP replay window, continuations, links, all rows re-read per
request -- and **one thing left: the watch broker.** `broker: memory` publishes
inside a process, so a client watching one replica never hears about a write on
another and nothing reports it. See D33; `docs/OPERATING.md` has the whole
checklist under "Running more than one".

None of the rest blocks the screens. Item 4's second increment is explicitly
*taken when the noise is measured rather than predicted*; item 5 is a rule about
what a password may be and changes no shape; item 9 has a boundary question that
argues with D13 and has not been answered.

**What is left is that one increment**, and it is left on purpose rather than
unfinished: item 4 says a dedicated stream is taken *when the noise is measured*,
and nothing has run long enough to measure any. Its first increment -- a console
that watches `Holder` and redraws when somebody is disabled or their epoch moves
-- came free with D26 and is what a screen needs today.

**"Came free" was asserted and is now run.** `cmd/watch_test.go` opens the
stream, disables somebody and moves their epoch, and reads both facts off it.
That is worth the file it took: a column can be added, be right in the database,
be answered by `Get`, and still not be on the stream, and nothing would say so
until an app that trusted the stream went on trusting a signed-out session.

Writing it found F10 next door, which is in the table below.

## Decisions to take before the code that needs them

Each takes a `D` in PLAN.md when it is taken. None is taken here.

1. ~~**Is `Ticket` one entity or several?**~~ Answered, above: two of D16's
   three reasons land, so the delegation token is its own entity and the
   continuation and the nonce are not settled by that choice. It takes a `D`
   once P1 is written.
2. **The rule for item 11.** *Only somebody whose permissions are a subset of
   yours*, or *a tenant operator is a tenant administrator, and we say so*.
3. ~~**Item 9's boundary.**~~ **Taken**: the connection is roster's and the
   secret is not. Everything that varies per tenant is public, and the secret
   has to reach the front door whatever roster does -- so roster holds a
   reference it does not read, and D13 survives without an exception.
4. ~~**Where the session table lives.**~~ **roster's**, and the reasoning is in
   `session.proto`. The argument for upstream is real — the next payday app
   writes this one again — and it is not enough: a store payday could ship
   would need a dialect story it does not have, acquired for one table, and the
   half that costs something is the **migration**, which is the app's either
   way. What payday ships is the seam, which it already does.

## Progress

Every payday finding is closed. What is left of this roadmap is one increment
that the plan defers on purpose.

| | | |
| --- | --- | --- |
| P0 | F9 — a reference reached erased rows | **done** — fixed in `protoc-gen-orm-ent@3843c60`, pin moved, both symptoms tested here |
| P1 | `Delegation` | **done** — the entity, `rd_`, the issuer binding, and `keys.Delegate`. PLAN.md D25. Nothing mints one over the wire; D24 puts the page first |
| P2 | `Holder` epoch and disabled, and the refusals | **done** — PLAN.md D26. Closes list items 6 and 12, and item 4's first increment came free |
| P3 | the reference app's spine | **done** — `Vouch.Delegate`/`Revoke`, `keys.Sweep`, the lifetime settled, identities and credentials on `MeGetResponse`, and `examples/sso` signing in with a password and reading its own record as the person |
| P4 | hostname, mail domain, and F7 | **done** — PLAN.md D27. `Host`, `MailDomain`, `FrontService`, `Email` stamped and unique per tenant, `VouchWho.address`, and `examples/sso` asking roster rather than holding a map |
| P5 | escalation over credential writes, then the write surface | **done** — PLAN.md D28. `core.Reaching`, `Vouch.Reset`, `Vouch.Unlock`, and the rule over `Vouch.Set`. Not done: minting an `rt_` over the wire |
| P6 | the reads a screen needs, and the screens | **done** — the reads (items 7, 8), §5 the operator screen, §4 self-service in the reference app, and §6 the extraction. D24's order is complete |
| P7 | two-step verification | **done** — PLAN.md D29 and D30, and `examples/sso` showing two forms with a half-session between them |
| P8 | recovery and the magic link | **done** — PLAN.md D31. `Vouch.Link`/`Redeem`, a reset voiding what came before it, and the sweep over both short-lived tables. The air-gap half was already D28's |
| P9 | the rest | session table, the breached-password check, **provider connections** and **§6** done · left: the event stream, which item 4 itself defers |
| — | F10 and F11, upstream | **done** — `pd.Secret` streamed the verifier it hides everywhere else, in payday's own reference app as much as here. `lesomnus/payday@b57f9a1`, pin moved, both halves pinned in `cmd/watch_test.go` |
| — | F12, upstream | **done** — `pd doctor` reads the app's schema now, which its own comment said it did and did not. `lesomnus/payday@9a252e5` |
| — | D34 · single-use, upstream | **done** — `Erase` answered `Empty`, so nothing could tell a win from a loss and one continuation minted up to 24 credentials on Postgres. `protoc-gen-orm-service@efff3ac` + `protoc-gen-orm-ent@f892843`, pins moved through payday |
| — | F3 | **already fixed upstream**, and this document was stale about it: `pdgen.checkPresence` refuses a message field that has `Has…` and a NOT NULL column, exempting the three server stamps by their declarations rather than their names. Confirmed here, through `pd doctor` |

## See also

- [PLAN.md](../PLAN.md) — the decisions, and the twelve subjects
- [POSITION.md](POSITION.md) — the line these are all on the near side of
