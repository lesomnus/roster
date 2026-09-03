# Building it — the order, and how far it has got

[position.md](position.md) is what roster is and why. This file is the record
of building it: the twelve subjects on the near side of that line, the order
they were taken in, the dependencies, and what is open now.

Decision numbers below -- `D1`…`D58`, `F1`…`F20` -- name entries in the decision
log this repository kept while it was being built (`PLAN.md`, since retired).
Each number's row here keeps what was decided; the why lives beside the thing it
decided -- a file comment, a proto comment, a section of a doc -- and the full
accounts are in history: `git log -p -- PLAN.md`.

**All twelve are done, and so is every phase below.** The last was item 4, the
sync channel, which the list had carried since the first week and which D26 took
the first half of for free: `SyncService`, D53. What is here now is a record of
the order rather than a queue -- so a change to roster is a new decision written
beside what it decides, and a new row here, not the next line of a plan.

## The twelve subjects

Kept because code and docs cite them by number ("roadmap.md's item 10"):

1. **A tenant from a hostname.** D27 -- `Host`, and `FrontService.WhoseHost`.
2. **Home-realm discovery, by domain.** D27 -- `MailDomain` and `FrontService.WhereFrom`.
3. **Recovery.** D28 and D31 -- an operator's `Vouch.Reset`/`Unlock`, and the magic link.
4. **The sync channel, as an invalidation signal.** D53 -- `SyncService`, state and not a signal.
5. **A breached-password check.** `vouch.Breached`, refused at set-time against a local corpus.
6. **"Sign out everywhere", as a fact rather than a list.** D26 -- `Holder.date_invalidated`, one monotonic timestamp.
7. **A read that answers which methods somebody has.** `MeService.Get`, and `MeSaysWhatTheGateSays` holds it to the gate.
8. **Refusing to remove a last login method.** `server/core`'s last-way-in count, D42 for the race, D43 for what counts.
9. **Per-tenant provider connections.** D27's neighbour -- `Connection`, config and never a token check.
10. **A write surface for `Credential`.** D28 -- `Vouch.Reset`/`Set`/`Unlock`, an operator's, on the admin port.
11. **Escalation prevention over credential writes.** D28, D35, D40, D41 -- both rules, every path.
12. **A disabled state, which is neither a lockout nor an erasure.** D26 -- `Holder.date_disabled`, read wherever a credential resolves.

## What the list turned out to be

The twelve were written as they were found, so they are twelve subjects and not
twelve pieces of work. Reading them together, four collapse.

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
for is a second increment, and is taken: `SyncService`, D53. The thing
that settled it was not a rate but that the entity `Watch` refuses a
subscription with no filters, so an app cannot subscribe for people who have not
signed in yet.

**Two of them hang off a domain.** Item 1 (a tenant from a hostname) and item 2
(home-realm discovery) are both lookups by a name, and `holder.proto` already
wrote the rule that decides their shape: *anything that has to be looked up goes
flat beside it* — which is the reason `Identity` is an entity and `Profile` is
not. So they are entities, not repeated fields on `Tenant`. Two of them, because
login.md is explicit that a hostname and an email domain answer different
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
- **Not single-use.** A delegation is looked up and nothing is touched, because
  it is what an app holds for a whole session and presents on every call. What
  *is* single-use is the continuation, which is one proof rather than one
  credential -- P7, D30, and D34 for what it took to make that true.

The prefix is not a new idea. operating.md already says *the prefix decides
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
than being slipped in here. **It has one: D49**, and the answer is that roster
does not check the token at all, because `connection.proto` had already decided
roster is not the relying party. The front door checks and `Vouch.Accept` mints.

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

**Forces the order:** stated in item 11 itself, and it is the only pair in the
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
`continuation` -- and **mint a continuation only when there is more
to prove**, so a deployment with one factor pays exactly what it pays today and
the single-factor path stays one round trip. Minting stays on `Delegate`, which
takes a continuation in place of `who`+`secret`.

That also fixes the fail-open: an app gates on **the token being present**
rather than on a boolean, so one that ignores the new fields fails closed.

`ok` is never set on a response carrying a continuation. They are mutually
exclusive.

#### Four increments

1. **`Credential` grows up.** `name` at field 5 and the index at
   `(holder, kind, name)` -- because *one of each per person* is right for a
   password, defensible for TOTP and wrong for WebAuthn, where registering a
   backup authenticator is the standard advice. Written here as `alias` at 4,
   which is what the field-number convention reserves and is the wrong number:
   payday **makes an alias up** when a caller gives none, and this wants the
   opposite, an empty value meaning *the only one*. `credential.proto` has the
   long version. It costs nothing now and is a
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
5), per-tenant provider connections (item 9, which needed a decision first and
has one), and extracting the components (D24 §6, last for D24's own reason).
**All four are done.** The event stream is `SyncService` -- one stream that
takes nothing, carrying three columns off `Holder` and no rows -- D53.

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
request -- and one thing left, the watch broker. That is written now:
`watch.broker: postgres` is `LISTEN`/`NOTIFY` on the database the rows are
already in, so it needs no second piece of infrastructure. See D33;
`docs/operating.md` has the whole checklist under "Running more than one".

None of the rest blocks the screens. Item 4's second increment is explicitly
*taken when the noise is measured rather than predicted*; item 5 is a rule about
what a password may be and changed no shape; item 9's boundary question was
answered -- the connection is roster's and the secret is not -- and both are
written.

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

Each took a decision number when it was taken. **All four have been**, and the
bullets below are struck with the decision that took them; the section stays for
what the questions were before anybody answered them.

1. ~~**Is `Ticket` one entity or several?**~~ Answered, above: two of D16's
   three reasons land, so the delegation token is its own entity and the
   continuation and the nonce are not settled by that choice. It takes a `D`
   once P1 is written.
2. ~~**The rule for item 11.**~~ **Taken**: D28, the subset rule -- *you may
   only write the credential of somebody whose permissions are a subset of
   yours*. The alternative it names, *a tenant operator is a tenant
   administrator and we say so*, is honest and is written down beside it as the
   one that was not taken. `server/core/escalate.go`, and D35 for the two ways
   round it that were found later.
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

Every payday finding is closed, every phase is done, and so is every one of
the twelve. The last was P9's event stream, which the plan had deferred on
purpose and which stopped being a deferral when the reason it was one turned out
to be wrong: the entity `Watch` is not merely noisy for this, it refuses a
subscription with no filters, and an app cannot name people who have not signed
in yet.

The last thing that was **written and waiting** rather than open has landed too:
`Email.date_verified` carries `(payday.field).stamped` now, and the layer in
`server/core/email.go` that did the same refusal by hand is gone. It waited on a
`buf push` rather than on a decision -- the option was in payday's checkout and
reached roster only through `buf.build/payday/payday:dev`.

`cmd/foreignedge_test.go` did not change, which was the point: it asserts the
refusal and never where the refusal lives, including *the deployment's own work
is not a request* -- which survives because the generated check sits in the
**gate**, and the gate is installed on `Walled` alone.

| | | |
| --- | --- | --- |
| P0 | F9 — a reference reached erased rows | **done** — fixed in `protoc-gen-orm-ent@3843c60`, pin moved, both symptoms tested here |
| P1 | `Delegation` | **done** — the entity, `rd_`, the issuer binding, and `keys.Delegate`. D25. `Vouch.Delegate` mints one over the wire and `Vouch.Revoke` ends it; what has no wire surface is `DelegationService`, which is closed |
| P2 | `Holder` epoch and disabled, and the refusals | **done** — D26. Closes list items 6 and 12, and item 4's first increment came free |
| P3 | the reference app's spine | **done** — `Vouch.Delegate`/`Revoke`, `keys.Sweep`, the lifetime settled, identities and credentials on `MeGetResponse`, and `examples/sso` signing in with a password and reading its own record as the person |
| P4 | hostname, mail domain, and F7 | **done** — D27. `Host`, `MailDomain`, `FrontService`, `Email` stamped and unique per tenant, `VouchWho.address`, and `examples/sso` asking roster rather than holding a map |
| P5 | escalation over credential writes, then the write surface | **done** — D28, and D35, D40 and D41 for the five ways round it found later. `core.Reaching`, `Vouch.Reset`, `Vouch.Unlock`, the rule over `Vouch.Set`, and the second rule beside it: nobody writes a way in for somebody wider than they. And D48 closes the last of it: `IssueService` on the data plane mints an `rt_` over the wire, through the walled server so both rules run |
| P6 | the reads a screen needs, and the screens | **done** — the reads (items 7, 8), §5 the operator screen, §4 self-service in the reference app, and §6 the extraction. D24's order is complete. **And §4's *add an SSO method* is drawn now**, D50: `POST /me/ways` is the same redirect with a second cookie, `MeService.Link` is the write, and the session — never the browser — says whose account it is for |
| P7 | two-step verification | **done** — D29 and D30, and `examples/sso` showing two forms with a half-session between them |
| P8 | recovery and the magic link | **done** — D31. `Vouch.Link`/`Redeem`, a reset voiding what came before it, and the sweep over both short-lived tables. The air-gap half was already D28's |
| P9 | the rest | **done** — session table, the breached-password check, **provider connections**, **§6**, and now the event stream: `SyncService`, D53. It sends state and not a signal, takes no argument because the wall is what narrows it, and does not snapshot because a snapshot here is every holder of every tenant |
| — | D58 · everything from a terminal, for everybody | **done** — the CLI has two modes and the documentation described one: a customer's own person runs the same binary with `client.addr` and their `rt_`, against a config with no `db:` block, and is walled and gated like any caller. So the goal is a command for every RPC, because *what can be done without a console* has one correct answer. Of the 26 that had none: **the six overlay methods** went first — `pdcmd` matched a fixed list of six verb names, payday grew `Tree.Unary` (any unary method by full name; everything around the call is shared and the name and brief are the app's), and `roster holder update|disable|enable|invalidate|signs-in|revoke-key` are one line each in `cmd/entity.go`, so the next overlay method is too. Of the hand-written: `roster me` (six), `roster front whose-host|where-from`, `roster sync watch`, then the sign-in surface whole — `vouch verify`, `delegate`, `continue`, `link`, `redeem`, `revoke`, `enrol` and `accept`, secrets on stdin and tokens printed once, remote only because a delegation, a link and a continuation are bound to the caller they were issued to and a local run has none — and `roster issue key|password`, the wire mint. Three absences are decisions: `Apply` (closed unless a deployment opts in); `AuthService` (it mints the console's session, and a session cookie is a browser's credential where a terminal's is a key); and `issue key --service` (minting is granting, the grant rule reads bindings, and a key holds none — by design, or a key could replicate itself wider; the mints for a service are `roster key add` and a console). Finding that last one also found that a key whose methods reach `IssuePassword` can hand out any operator's first password, so `roster key add` now warns about it the way it warns about `Accept` and the trail. And a third thing that was neither, **done**: `Role`, `Group` and `ApiKey` could not be named `@tenant/alias` at all, because `pdcmd` fills a reference from a field called `slug` and those three declared their index as `alias` -- in the other `refs` order as well. Both aligned with payday, which renames three ref messages and three oneof fields and moves no field number; a caller migrates `RoleRef{alias:}` to `{slug:}`. No SQL moved: `pd gen` normalises fields before edges, so the index was `(alias, tenant)` either way. `cmd/mecmd_test.go`, `cmd/holdercmd_test.go`, `cmd/frontcmd_test.go`, `cmd/synccmd_test.go`, `cmd/vouchwire_test.go`, `cmd/issuecmd_test.go` |
| — | D57 · the terminal can finish what it starts | **done** — after D56 the first customer is created by somebody, and every command that creates one is local already (`tenant add`, `holder add`, `role add`, `binding add`, all through `Ungated`). Then nothing: `key add` was the control plane's alone, so a deployment run from a terminal needed a browser for the last step. `--tenant`/`--holder` mints an `rt_`; the plane is said by which flags were given and the prefix follows from it. Naming a customer's person does **not** create them, unlike a service -- that asymmetry is the wall. And `roster vouch reset|set|unlock` beside it: the password was first held back on the grounds that generating one is an act with a person on the other end, which is true of an operator at a console too and so was never a difference. `cmd/customerkey_test.go`, `cmd/vouchcli_test.go`, D57 |
| — | D56 · a customer is an operator's act, not a seed | **done** — `roster init` took `--tenant contoso --holder admin`, so every deployment began life with a customer nobody asked for; and after D55 that person could not be signed in as anyway, since a data plane holder gets no credential from `init` and both writes that would give them one are served on `admin.addr`. It seeds the control plane alone now. What makes that possible was already true and never asserted end to end: `mayGrant` compares methods and **site** rather than tenants, so the operator's control-plane binding reaches a tenant created a moment ago. `ts/src/customers.tsx` is the screen, `cmd/newcustomer_test.go` is the sequence including both ways of writing a way in. D56 |
| — | D55 · a control plane is not a thing to add later | **done** — `roster init` refuses a configuration with no `control.db`, which is the only setting it insists on. Not symmetry: under `auth.Plain` anybody can call `MeService.IssueKey`, the `rt_` lands on the data plane, nothing reads it because `auth.Bearer` is wired inside `if c.Control.Serves()`, and an expiry is optional — so naming a control plane later turns every key minted while nobody was checking into a working credential at once. `cmd.Seed` is not asked, which is where a test and the Wasm sandbox live. D55, `cmd/init_test.go` |
| — | F10 and F11, upstream | **done** — `pd.Secret` streamed the verifier it hides everywhere else, in payday's own reference app as much as here. `lesomnus/payday@b57f9a1`, pin moved, both halves pinned in `cmd/watch_test.go` |
| — | F12, upstream | **done** — `pd doctor` reads the app's schema now, which its own comment said it did and did not. `lesomnus/payday@9a252e5` |
| — | D33 · the broker | **done** — `watch.broker: postgres`, `LISTEN`/`NOTIFY` on the rows' own database. The last thing that did not cross replicas. `lesomnus/payday@73a90a0` |
| — | D34 · single-use, upstream | **done** — `Erase` answered `Empty`, so nothing could tell a win from a loss and one continuation minted up to 24 credentials on Postgres. `protoc-gen-orm-service@efff3ac` + `protoc-gen-orm-ent@f892843`, pins moved through payday |
| — | D35 · escalation, twice round | **done** — `TeamMembership.Add` handed out a role without asking, and a permission held through a group read as no permission at all — which `mayReach` allows on rather than refuses on. One query now answers all three readers. `cmd/escalate_test.go` |
| — | the baseline | **done** — after the control plane spent a day refusing every key it held (every part tested, the documented journey not), the everyday promises were made a surface of their own: `docs/baseline.md` names what a normal user may rely on and the tests that pin each, `TestBaselineNamesRealTests` keeps the mapping honest, and the missing journeys were written — `TestTheTutorialRunsAsWritten` (usage/tutorial.md, step for step, over `server.http`), `TestARestartKeepsEveryCredential`, the port×credential refusal cells, sync and self-service under real keys. The pass found a second bug of the same class: `MeService.SignOutEverywhere` voided delegations and left every console session alive, because `server/session` never read the stamp — fixed, `TestInvalidateEndsTheConsolesOwnSessions`. And `PLAN.md` was condensed from 4.7k lines to an index that keeps every number citable |
| — | D58, finished | **done** — the sign-in surface from a terminal: `roster vouch` grew its wire half (verify, delegate, continue, link, redeem, revoke, enrol, accept — secrets on stdin, tokens printed once, the uniform no preserved, remote only because those calls are a caller's), `roster issue` mints over the wire, and every roster RPC that can have a command has one (payday's own `Introspect` and `Batch` are the SDK's surface, not an operator's). Findings along the way, from an adversarial review of the new surface: the upstream `pdcmd` seam D58 waited on had existed all along (`Tree.Unary`); a key reaching `IssuePassword` can become any operator — `Widest` warns now; and `IssuePassword` on the **data plane** took a bare alias, resolved it against an arbitrary tenant and wrote a password with no escalation check — a real hole, now refused off the control plane (`server/console`, `codes.Unimplemented`). The review also tightened the CLI: a half-done sign-in exits non-zero so a script cannot capture a `vc_` where an `rd_` was meant, `delegate` refuses being told who twice, and `issue key` refuses a name with no tenant. And the `authsession.Verify` open question turned out to have been answered by `frontdoor` the day after it was asked. `cmd/vouchwire_test.go`, `cmd/issuecmd_test.go` |
| — | the plan is retired | **done** — the condensed index dissolved too: the why of every decision lives beside the thing it decided, the twelve subjects and the open questions moved here, position.md took the full picture of where roster sits when Hydra is in front, and every `PLAN.md` citation in the tree now points at the living copy. The numbers stay citable through the rows above, and the full accounts are in history: `git log -p -- PLAN.md` |
| — | D36 · the named factor nothing could confirm | **done** — `Enrol` invites a name and `Verify` took none, so the first named second-factor a person added was one no call could reach and the deployment silently had one factor. `VouchVerifyRequest.name`, `cmd/enrol_test.go` |
| — | D37 · the review | **done** — every document read against the code, every finding attacked before it was believed. The admin port served without the leaked-password corpus and recorded half its writes; a rate counted no streams and built two buckets; a suspension did not reach `Introspect`; SIGTERM was ignored; `serve` checked one plane of two. One found and not fixed then, and closed since by D42: the last-way-in race, which turned out not to need a lock nobody offers — the schema's own version is one, once the count and the write share a transaction |
| — | D41 · the question asked from the other end | **done** — five families of impersonation enumerated and attacked; eight were not refused. A way in written onto somebody else's row (`Identity`, `Email`, `ApiKey`), an address stored unlike it is looked up, `TeamMembership.Patch`, a team role invisible to `mayReach`, `Vouch.Link` across a tenant, and a select reaching an erased parent. `cmd/as*_test.go` |
| — | F16, upstream | **done** — F15's redactor was written for one of the **three** recorders reading the same `bare.Change`; `watchRecorder` and `outboxRecorder` went on marshalling the patch raw, so an `outbox` row held a verifier at rest and the first broker carrying a patch would have carried one off the box. `lesomnus/payday@7ff5e8f`, pin moved, `cmd/outboxsecret_test.go` |
| — | F20 · two quiet generator holes | **done** — an overlay redeclaring a generated rpc replaced it and nothing said so (`lesomnus/payday@15a0e47`), and one whose name matched no contract was never merged at all (`@a06360f`). Plus `Email.date_verified`: a caller could assert their own address had been checked, which nothing reads **yet** — closed in `server/core/email.go`, because `immutable:` removes a field from Patch and keeps it in Add, which is backwards here. And payday now has the declaration this wanted — `payday.field.stamped`, `@1c2b63e` — which roster takes the moment payday's buf module is published again |
| — | F19 · an edge is a read | **fixed upstream** — the widest thing found here, and it needed a clerk. `Email.vouched_by` is not the path to anybody's tenant, so the gate never asked about it — and a nested select walks it, so `Email.Add` + `Email.Get` read another tenant's identity, that person's name, and their tenant. F14's shape with scope where liveness was. Both key forms, and the by-subject one needs no identifier and answers as an oracle. `lesomnus/payday@7d19dea` and `@51284cf`, pin moved, `cmd/foreignedge_test.go` |
| — | D52 · WebAuthn | **done** — the largest thing operating.md listed as not here, and it needed no new decision: D20 designed it while arguing about TOTP. roster verifies because the **signature counter** is state and state belongs to the row; the relying party, origins and challenge arrive inside the presented bytes, because the request is generic across kinds. Burns one ECDSA rather than one argon2, which is `kind.go`'s finding a second time. `server/vouch/webauthn.go`, `vouchtest` |
| — | D54 · a key somebody mints for themselves | **done** — the last of `operating.md`'s *what is not here* that was a missing feature. `MeGetResponse.keys`, `MeService.IssueKey` and `MeService.RevokeKey`: no subject in any of them, so the smallest role covering one means *may mint a key that acts as you* rather than *for anybody in this tenant*. `server/core` still refuses a list wider than the person writing it, reached by writing through the walled stack. `examples/sso` draws it. D54 |
| — | D51 · the screen a key is minted from | **done** — D48 left *nothing in `ts/src` calls it* true. It needed a read first: `ApiKeyService` is unregistered everywhere, so `HolderService.SignsIn` answers with keys as well as identities and credentials, verifier absent rather than deselected. `RevokeKey` beside it, a *which* within a *whose*. `IssueService` on the admin port too, because that is the port a console reaches. `cmd/holderkey_test.go` |
| — | D50 · adding a way in | **done** — §4's undrawn half. `MeService.Link` beside `Unlink`, and **not** waived: `cmd/asself_test.go` failed on `MeLinkRequest.subject` and was right on the substance — what `aboutYourself` waives is what somebody must be able to do with no role, and attaching a provider account is a feature a deployment offers. The reference app does not grant it either, because doing so would mean its key could bind any role to anybody. `cmd/melink_test.go` |
| — | D49 · a front door that checked is believed | **done** — D23's last open sentence. An OIDC-fronted deployment could find out who somebody was and could not act as them: the password half had `Delegate` and the provider half had nothing. roster does not check the token — `connection.proto` had already ruled that out as being the relying party — so `Vouch.Accept` takes the claim a front door verified and mints for whoever it reaches. Its own method because the grant is the whole control, and `roster key add` says so. `cmd/accept_test.go` |
| — | D48 · an `rt_` over the wire | **done** — P5's one loose end, and the last *not done* in this table. Not a new service: `IssueService` on the data plane, minting the customer's kind. The prefix is a fact about which server answered rather than a field, and each plane names a holder its own way — an alias creates one where there is a single tenant and would be a typo into somebody else's where there are many. The rules are `core.ApiKey.Add`'s, unchanged. `cmd/tenantissue_test.go` |
| — | D47 · no new RPC on the trail | **done, by deciding** — `Prune` is refused not by the generated layer (an overlay is a new method, and `core` sits below `Audit` anyway) but by `cmd/policy.go` matching methods by **pattern**: a role or key holding `/roster.*/*` picks up a new method with nobody deciding. `List` over the archive wants an index that does not exist, and a call that is sometimes slow is worse than one that is honest. `Status` has no consumer — the console has never read the trail |
| — | F17 · a deployment key reads every trail | **written down** — by design (`Where` answers `frame.Everything` for a key), and nobody had noted the magnitude: one method answers every table's contents, in every tenant, across all time. Asserted in both directions, and `roster key add` says so when a key's methods reach it. `cmd/trailkey_test.go` |
| — | D46 · an erase destroyed nothing | **done** — `Holder.Erase` writes two columns and stops, so *we deleted them* was true of nothing roster served: the alias, the addresses, the identities and the whole trail survived, including the copy the erase itself wrote. `roster forget` destroys, `roster restore` undoes it while the grace lasts (and without that the window is a delay, not a grace), `holder.forget_after` is the clock. The trail keeps its events and loses its contents, in the database and the archive both. `server/forget`, `cmd/forget_test.go` |
| — | D45 · retention upstream, and per kind | **done** — D44 built it here and it is payday's: the `Audit` entity, the recorder and the layer that refuses trail writes are all payday's, and *this table grows forever* is every payday app's problem. Moved, with the split `outbox.go` already drew — generated `pd.TrailStore`, judgement in `trail`. And one clock over the table was wrong: a person's record has to stop existing and a machine's usually must not, so the policy is per kind on a new indexed `Audit.domain`. Profiles carry their own citation. `lesomnus/payday@db9e366`, `cmd/retention_test.go` |
| — | D44 · the trail's retention | **done** — `audit.proto` asked for a retention policy and there was none, so the answer was forever on the one table that never stops growing. Two clocks (`audit.retain` in the database, `audit.destroy` in the archive), written and synced before anything is deleted, deleted by identifier rather than by re-running the query. Forever stays the default and `retain` with nowhere to put the rows is refused at startup. `roster trail`, `server/trail`, `cmd/retention_test.go`. What it left open — erasing one person from the trail — is D46 |
| — | D43 · a second factor is not a way in | **done** — both sides counted a TOTP seed as a way somebody could sign in. `server/core` let the last provider be unlinked because a seed was left; `Verify` set `ok` for somebody whose only credential *was* the seed, so six digits and a thirty-second window were a whole sign-in. One sentence, `vouch.Begins`, asked by both. `cmd/secondfactor_test.go` |
| — | D42 · the last way in | **done** — the race D37 recorded as unfixable. An optimistic lock is a write, and a `Patch` carrying only a version precondition is not one: the count, the erase and the swap are one transaction now. Forty of forty keep a way in on PostgreSQL, where thirty-nine were lost |
| — | F15, upstream | **done** — `secret:` cleared a column out of `Audit.value` and not out of `Audit.patch`, so `Vouch.Set` wrote a password hash into a **served** table that nothing erases. `lesomnus/payday@312ccbf`, pin moved, `cmd/trailsecret_test.go` |
| — | F14, upstream | **done** — a select reached a parent that had been erased, so an erased person was readable whole through a row that outlived them. `protoc-gen-orm-ent@28a0a48`, `lesomnus/payday@dbe36f0`, pin moved |
| — | D40 · the third way round escalation | **done** — `GroupMembership.Add` names no role and grants as much as one: joining a group hands over every binding written to it. `Rules` gained `Joining`. The rule is now stated as *any write the gate reads differently afterwards*, which is what makes the fourth findable |
| — | D39 · one grace for the process | **done** — five listeners were stopped by a `defer` each, so five graces ran end to end against the ten seconds `docker stop` allows. `cmd/shutdown_test.go` |
| — | D38 · both planes are closed | **done** — `Close` closed the outer server only, so every test that built a control plane left a pool behind and the suite ran out of connections against the PostgreSQL its own CI gives it. Found by running CI's shape at a desk. `cmd/close_test.go` |
| — | F13, upstream | **done** — the record of a soft erase was filed under the actor's tenant with an empty value, because the row it recorded was already invisible to the read that records it. Every entity holding a person's data is soft-erased — `Tenant`, `Audit` and `Outbox` declare `hard:` and none of them is anybody. `lesomnus/payday@f1f9321`, pin moved, pinned here in `cmd/erasetrail_test.go` |
| — | F3 | **already fixed upstream**, and this document was stale about it: `pdgen.checkPresence` refuses a message field that has `Has…` and a NOT NULL column, exempting the three server stamps by their declarations rather than their names. Confirmed here, through `pd doctor` |
| — | the self-only verbs folded back | **done** — `Credential.ChangeMine`/`EnrolMine` were a second name for rows `Set`/`Enrol` already write. RBAC stays method-level and *whose row* is `mayReach`'s (yourself always passes), so a person calls `Set`/`Enrol` with their own reference and the app is the layer that passes only that; CLAUDE.md § *Overlay before service* says so now. What the pair had that mattered — proving the current password before your own is replaced — is `Set`'s rule for your own row (`current`), and it counts a wrong answer toward the lockout, which `ChangeMine` did not |
| — | and `MeService`'s three | **done** — `Me.Link`/`IssueKey`/`RevokeKey` were the same twin one service over, kept for the grant they let a role name. A person calls `Identity.Add`, `ApiKey.Issue` and `Holder.RevokeKey` with their own reference now; `roster me` and `examples/sso` are the layer that passes only that reference. What stays on `MeService` is exactly what no role can be asked for — `Get`, `Unlink`, `SignOutEverywhere`, waived and therefore subject-less by necessity (`asself`) — and its proto says so |
| — | two official UIs, P0 | **done** — the plan is `ts/plan.md` (retired when it lands, the way this file's own plan was). Two spikes settle what the account app is built on: it holds one **tenant** key per operator it fronts, because a deployment key resolves to a frame with no tenant and `frame.Everything` while a tenant key lets the wall do the narrowing (`cmd/accountkey_test.go`); and the browser reaches roster through `frontdoor.Door.Proxy`, speaking Connect to the app's origin with the delegation swapped in server-side, so `ts/gen` serves both UIs and no roster token is ever in a page (`frontdoor/proxy.go`) |
| — | two official UIs, P1 | **done** — the console draws how a customer's people arrive: the names that reach the tenant (`Host`), the providers (`Connection` — issuer, client id, scopes, and the `secret_ref` roster stores and never reads, said on the screen), and the mail domains that route to one (`MailDomain`, the provider a choice among the tenant's connections). Add and remove, no edit, no new RPC: `Patch` is closed on the wire and none of the three needed an `Update`. `cmd/arrives_test.go` makes the writes an operator's session makes on the admin port |
| — | two official UIs, P2a | **done** — the reads the organisation and access panels need. `list.by` grew a tenant filter on `Site`, `Team`, `Group` and `Role` (site too where there is one), and holder/site/team/group/role filters on the memberships and `Binding`, so a console draws one customer's rows without reading every customer's. And `Holder.Reaches`: what somebody may call, as the gate decides it, answered by the same function `MeService.Get` and the policy use (`core.Rules.Held`), as patterns and not an expansion. Behind the wall and nothing else -- it adds up what `Binding.List` and the membership lists already answer the same caller |
| — | two official UIs, P2b | **done** — the console draws a customer's organisation (sites, their members and teams, a team's members with the role each holds; groups and theirs), their access (roles as patterns, and under each role who has it — a person or a group, in a site or tenant-wide), their trail (`Audit` by tenant, who did what to which entity), and beside each person what they may call (`Holder.Reaches`). Every membership and binding form says beside the button that it is a grant, because it is. Add and remove throughout; reads through the store narrowed by the P2a filters, writes through `useCall` |
| — | two official UIs, P3 | **done** — the rest of a person in the console: their addresses (`Email` lists by holder now; add is a way in and runs `mayWriteAWayIn`, remove is not and does not), an identity unlinked from the operator's side (`Identity.Erase`, no rule needed — taking a way in away is not adding one, pinned in `cmd/person_test.go`), the profile replaced whole through `Holder.Update` under its version, a factor enrolled for them, and erase — soft, gone from every read, kept by the trail. And the deployment's own screen: an operator's password issued (`IssuePassword`, which makes them if new) and service keys minted and revoked, all from the page and shown once. The sandbox keeps one listener |
| — | two official UIs, P4a | **done** — `account/`, roster's own front door, and `roster account serve`. Many operators behind one process: a host resolves to a tenant (`FrontService.WhoseHost`, remembered) and every call about it goes out with **that tenant's** key, so the wall does the narrowing (`cmd/accountkey_test.go`'s fact, built on). Providers are the tenant's `Connection` rows read with that key, the client secret resolved from `secret_ref` (`env:NAME`), the OIDC exchange done here — the relying party roster is not. The browser speaks Connect to the app's origin and is handed on as the person (`frontdoor.Door.Proxy`, the bearer now asked per request). Two `Enrol` policies. A consumer only: `rstr`, `frontdoor`, `authsession`, and `front.Hostname`. `account/account_test.go` fronts two operators through one app |
| — | two official UIs, P4b | **done** — `ts/` is two UIs over one library: `ts/console/` and `ts/account/`, each its own Vite root, sharing `ts/lib/` (the clients, the store, `covers`) and one `ts/gen/`. The account page is the console's client with the transport pointed at its own origin — `roster account serve` hands the calls on as the person — so the store, the generated messages and the permission check are the same code in both, which is what `ts/lib/client.ts` said the design was. Sign-in with the operator's providers or a password (and the second form), then who you are, how you sign in, your keys, sign out, and a second provider account attached from the page |
| — | two official UIs, P5a | **done** — the three verbs the account screens were missing, each the entity's own and none a twin. `Credential.Erase` is served: it answers with no verifier and takes none, and the layer gives it what it owed — a password is replaced and never removed, a kind that begins a sign-in meets D42's last-way-in count, and the row's holder is held to `mayReach`. `Delegation.Get`/`List`/`Erase` are served: the token in `secret` is stripped on the way out like every other secret, and the layer holds the rows to `mayReach`, so a person lists where they are signed in and ends one, and `MeService` did not grow a field for it. `Email.Verify` mints a link for an address and `Email.Confirm` spends it and stamps `date_verified` — on `Email`, because unlike recovery there is a row to reference; and worth strictly less than a recovery link, minting nothing. `Link` grew an optional `email` edge so the two kinds share one table and are told apart by it |
| — | two official UIs, P5b | **done** — the two flows a mailed link finishes, in the account app: roster mints, the app delivers (`Config.Mail`). Recovery mails a link to an address that is somebody's — roster answers a token either way so the browser learns nothing, and the app, holding the tenant's key, may ask before it mails, so a stranger's form is not a way to have this deployment send mail anywhere — and the link, clicked, proves the mailbox and hands over a **password** shown once (`Vouch.Reset`, voiding everything issued before) rather than a session: a mailbox read once is a password the person changes, not an account somebody holds. Verification mails a link for an address the signed-in person owns, minted as the app so the app can confirm it from a click with no session, and the click stamps the row and signs nobody in. `frontdoor.Door.Redeem` beside `Accept`, for the one thing that has to be right once |
| — | two official UIs, P5c | **done** — the account page, section by section: profile (`Holder.Update` under its version), the provider accounts and the password they sign in with (`Me.Unlink`, a second account attached at another provider), addresses with a confirm button that mails a link (`Email.Add`/`Verify`/`Erase`), the password changed by proving the one held (`Credential.Set` on your own row), second factors enrolled and removed — an authenticator app whose seed is shown once, a security key through the browser's own ceremony handed to roster as the envelope it checks (`Credential.Enrol`/`Erase`) — where they are signed in and ending one or all (`Delegation.List`/`Erase`, `Me.SignOutEverywhere`), keys minted and revoked (`ApiKey.Issue`, `Holder.RevokeKey`), and a recovery form on the sign-in page. Every write names the person's own row; no verb was added for a screen |
| — | two official UIs, P6, and the plan retired | **done** — `roster serve` serves the built console under `/console/` on `control.http` (`control.console.dir`, with `control.console.admin` telling the page where the admin listener is), so a deployment needs no `origins:` for its own page; `roster account serve --static` serves the account page the same way. `Tenant.Update` lets a console edit what a customer says about itself and never its alias. `scripts/test.sh` checks that `account/` imports nothing of the server but `front.Hostname`. `README.md`, `docs/position.md` and `docs/baseline.md` say what the two UIs are and pin what the account app promises. `ts/plan.md` is deleted: every decision it held is beside the code it decided, which is where this file says a why lives |
| — | `Holder.RevokeKey` folded into `ApiKey.Erase` | **done** — it existed because `ApiKeyService` was served nowhere, and that premise ended when `Issue` was served everywhere; two names for one row. `ApiKey.Get`/`List`/`Erase` are served on every port now: the verifier stripped on the way out like every other secret, the row held to `mayReach` on its holder (a plain person ends their own and a peer's, not an administrator's). `roster me revoke-key`, the console, the account page and the example all end a key by identifier. `Add`, `Patch` and `Apply` stay shut off the control port |
| — | the pages, driven | **done** — `scripts/e2e.sh` and `ts/e2e/`: Playwright against a deployment the script stands up from the operating guide's own recipe (`roster init`, the four seed writes, `key add`, `roster serve` with the console mounted, `roster account serve` fronting it), so what is checked is the page and the wiring rather than a mock of either. Its first run found three defects every other gate was green on, which is the argument for it. `Me.Get` narrowed a person's **held patterns** by the credential's exact list -- `Grant.Allows("/roster.*/*")` is false for a delegation naming twenty methods -- so anybody bound to a wildcard role saw *this needs a role naming …* under every heading of the account page; `server/me.meet` now meets the two pattern sets both ways (`cmd/meet_test.go`). The factors panel read `Me.Get` and nothing it wrote answered with the record, so an enrolled factor did not appear until a reload; it now asks the store to forget the record. And a wrong second code left the page on the second form, where every further code is refused by design -- the half-session is ended and the continuation spent -- so it goes back to the first form and says so. The security key is a CDP virtual authenticator, so the ceremony is the browser's real one. Not in `test.sh`: a browser and a minute; its own CI job. |
| — | the sandbox, two instances, and opened by a test | **done** — `npm run dev:sandbox` had quietly stopped working: nothing opened it. Five things had drifted -- the page moved under `/console/` and the sandbox still fetched `/app.wasm` and `/wasm_exec.js` from the root; `AuthService.SignIn` was refused for having no caller, because the instance used payday's public set rather than roster's (`cmd.Public`, now exported); the cookie header `SignIn` set failed the whole call where a transport has nowhere to put one (best effort now, `server/console`); `MeService` was never registered on the control instance; and `file:data?vfs=memdb` is a **private** database per connection -- `memdb` shares only under a name that begins with `/`, so the schema was created on one connection and the first query on another said *no such table*. Then the customers screen: it reaches `admin.http`, a third listener, and one instance answers one message port, so the sandbox is two instances (`wasm/admin`), each with its own databases, opened by `customers()` the way `admin.http` is dialed beside `control.http`. Two instances are two deployments and the page cannot tell, for the reasons `wasm/admin/main.go` gives; folding them into one waits on `jsport` serving two entry points from one worker. Over a port no cookie carries the sign-in, so the instance remembers who `console.Auth` accepted and takes later calls to be them (`wasm/sandbox`, natively tested). `ts/e2e/sandbox.spec.ts` signs in, opens customers and stands one up through the second instance; `scripts/e2e.sh` builds both wasm files and serves the sandbox on a dev server for it. |
| — | locally, in one command, again | **done** — `compose.yaml` ran roster on Postgres and the console from a vite dev server bind-mounted in, with no customer and no account app; the guide described that. Now: Postgres with both planes (the control database made by a one-shot `psql`, so no bind mount -- the engine a desk points at is not always the machine the checkout is on), roster with the console built into the image and mounted under `/console/`, a `customer` service that runs the operating guide's recipe once and mints the account app's key into a volume, and `account` fronting it. The image builds both pages (`node:22` stage) and `golang:1.27`, which `go.mod` asks for. A dev server on the host is told the origins instead of being a service, because a bind mount of `./ts` is the other thing in a compose file that assumes the engine is this machine. Verified by `ts/e2e/` against it unchanged, on Postgres. Two things it taught: the console's `__Host-`/`Secure` cookie works over plain http from `localhost` only, which is right and is why the stack is opened by that name; and payday's `pdcmd` warns about `ROSTER_ACCOUNT_KEY_<ALIAS>`, which roster reads itself rather than through the loader -- a payday change (issue filed), and the entrypoint's own `ROSTER_ROOT_*` are unset before the binary sees them. |
| — | the account app keeps no sessions (#1) | **done** — the cookie was a handle to a map in the process, twice over: `authsession.MemStore` for the session and the door's own `held` for the delegation beside it, so a second replica was anonymous. Chosen over an external store because everything the app checks is roster's already: the session is now **sealed into the cookie** (`authsession.Sealed`, in payday -- AES-256-GCM under a key every replica holds, the first key seals and every key opens, which is rotation) and what it carries is roster's delegation, which roster ends. The contract with the browser did not change; who makes the key did, which is the one optional method a store may add (`Sealer`). Two things it gives up, said where they are paid: nothing on the server can end a sealed cookie before its clock, and there is one clock -- both fine for a handle to something revoked elsewhere, and the reason `Session.Held` now exists for that handle rather than a map beside the store. The door reads sessions through `Sessions.Read` and the sign-out through `Sessions.Take` (dead or alive, so an expired session's delegation is still revoked); the second form's one-attempt rule is roster's where the cookie is the store. `--seal env:NAME` on `roster account serve`; empty is a key made at start. `TestASecondReplicaOpensTheCookie` is the scenario the issue described. |
| — | arrives through, edited in place (#2) | **done** — the *arrives through* panel added and erased and could not edit, and for a `Connection` erase-and-add is a gap in service and, under a mistyped name, every identity through it orphaned silently. `Patch` is closed on the wire, so each of the three grew the `Update` overlay `Tenant` and `Holder` have (`proto/ext/app/host_svc.ext.proto`, `connection_svc.ext.proto`; `server/core`): what a row says about itself under the version read, and never the **name** -- a host is what a tenant is resolved through, a domain what an address is routed by, a provider's name what `Identity.provider` points at. Host: the note. MailDomain: where it routes, and the note; empty routes nowhere, as on `Add`. Connection: issuer, client id, scopes, secret ref, note. The panel edits a row inline; `cmd/arrivesupdate_test.go` on the admin port, and the console spec adds a name and edits it. |

## Open, for whoever picks this up next

Nothing, at the moment. The two that stood here longest both closed in 2026-08,
and one of them closed by *looking*:

- **A product app should not have to write a login endpoint.** It does not,
  and it had not since the day after the question was written: `frontdoor` is
  the sign-in shipped as a package an app mounts in one line (D22, D24 §6) --
  the two forms, the half session, the delegation held beside the cookie, and
  a sign-out that revokes. The literal `authsession.Verify` adapter the
  question guessed at was never the right artifact: it would drop the
  delegation and have nowhere to put a second factor's continuation. payday
  needed no change. The question was carried forward verbatim for nine days
  after `frontdoor` landed, which is its own small lesson about re-reading an
  open question against the tree before carrying it.
- **RPCs without commands.** Zero, as of the D58 row above; the three absences
  that remain are decisions, not gaps.

## See also

- [position.md](position.md) — the line these are all on the near side of
- [usage/](usage/) — what to type, per topic, and a tutorial
