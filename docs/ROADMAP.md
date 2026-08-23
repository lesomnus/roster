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
for is a second increment, and is taken: `SyncService`, PLAN.md D53. The thing
that settled it was not a rate but that the entity `Watch` refuses a
subscription with no filters, so an app cannot subscribe for people who have not
signed in yet.

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
- **Not single-use.** A delegation is looked up and nothing is touched, because
  it is what an app holds for a whole session and presents on every call. What
  *is* single-use is the continuation, which is one proof rather than one
  credential -- P7, D30, and D34 for what it took to make that true.

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
takes nothing, carrying three columns off `Holder` and no rows -- PLAN.md D53.

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
`docs/OPERATING.md` has the whole checklist under "Running more than one".

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

Each takes a `D` in PLAN.md when it is taken. **All four have been**, and the
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

Every payday finding is closed. What is left of this roadmap is one increment
that the plan defers on purpose.

| | | |
| --- | --- | --- |
| P0 | F9 — a reference reached erased rows | **done** — fixed in `protoc-gen-orm-ent@3843c60`, pin moved, both symptoms tested here |
| P1 | `Delegation` | **done** — the entity, `rd_`, the issuer binding, and `keys.Delegate`. PLAN.md D25. `Vouch.Delegate` mints one over the wire and `Vouch.Revoke` ends it; what has no wire surface is `DelegationService`, which is closed |
| P2 | `Holder` epoch and disabled, and the refusals | **done** — PLAN.md D26. Closes list items 6 and 12, and item 4's first increment came free |
| P3 | the reference app's spine | **done** — `Vouch.Delegate`/`Revoke`, `keys.Sweep`, the lifetime settled, identities and credentials on `MeGetResponse`, and `examples/sso` signing in with a password and reading its own record as the person |
| P4 | hostname, mail domain, and F7 | **done** — PLAN.md D27. `Host`, `MailDomain`, `FrontService`, `Email` stamped and unique per tenant, `VouchWho.address`, and `examples/sso` asking roster rather than holding a map |
| P5 | escalation over credential writes, then the write surface | **done** — PLAN.md D28, and D35, D40 and D41 for the five ways round it found later. `core.Reaching`, `Vouch.Reset`, `Vouch.Unlock`, the rule over `Vouch.Set`, and the second rule beside it: nobody writes a way in for somebody wider than they. And D48 closes the last of it: `IssueService` on the data plane mints an `rt_` over the wire, through the walled server so both rules run |
| P6 | the reads a screen needs, and the screens | **done** — the reads (items 7, 8), §5 the operator screen, §4 self-service in the reference app, and §6 the extraction. D24's order is complete. **And §4's *add an SSO method* is drawn now**, D50: `POST /me/ways` is the same redirect with a second cookie, `MeService.Link` is the write, and the session — never the browser — says whose account it is for |
| P7 | two-step verification | **done** — PLAN.md D29 and D30, and `examples/sso` showing two forms with a half-session between them |
| P8 | recovery and the magic link | **done** — PLAN.md D31. `Vouch.Link`/`Redeem`, a reset voiding what came before it, and the sweep over both short-lived tables. The air-gap half was already D28's |
| P9 | the rest | **done** — session table, the breached-password check, **provider connections**, **§6**, and now the event stream: `SyncService`, PLAN.md D53. It sends state and not a signal, takes no argument because the wall is what narrows it, and does not snapshot because a snapshot here is every holder of every tenant |
| — | F10 and F11, upstream | **done** — `pd.Secret` streamed the verifier it hides everywhere else, in payday's own reference app as much as here. `lesomnus/payday@b57f9a1`, pin moved, both halves pinned in `cmd/watch_test.go` |
| — | F12, upstream | **done** — `pd doctor` reads the app's schema now, which its own comment said it did and did not. `lesomnus/payday@9a252e5` |
| — | D33 · the broker | **done** — `watch.broker: postgres`, `LISTEN`/`NOTIFY` on the rows' own database. The last thing that did not cross replicas. `lesomnus/payday@73a90a0` |
| — | D34 · single-use, upstream | **done** — `Erase` answered `Empty`, so nothing could tell a win from a loss and one continuation minted up to 24 credentials on Postgres. `protoc-gen-orm-service@efff3ac` + `protoc-gen-orm-ent@f892843`, pins moved through payday |
| — | D35 · escalation, twice round | **done** — `TeamMembership.Add` handed out a role without asking, and a permission held through a group read as no permission at all — which `mayReach` allows on rather than refuses on. One query now answers all three readers. `cmd/escalate_test.go` |
| — | D36 · the named factor nothing could confirm | **done** — `Enrol` invites a name and `Verify` took none, so the first named second-factor a person added was one no call could reach and the deployment silently had one factor. `VouchVerifyRequest.name`, `cmd/enrol_test.go` |
| — | D37 · the review | **done** — every document read against the code, every finding attacked before it was believed. The admin port served without the leaked-password corpus and recorded half its writes; a rate counted no streams and built two buckets; a suspension did not reach `Introspect`; SIGTERM was ignored; `serve` checked one plane of two. One found and not fixed then, and closed since by D42: the last-way-in race, which turned out not to need a lock nobody offers — the schema's own version is one, once the count and the write share a transaction |
| — | D41 · the question asked from the other end | **done** — five families of impersonation enumerated and attacked; eight were not refused. A way in written onto somebody else's row (`Identity`, `Email`, `ApiKey`), an address stored unlike it is looked up, `TeamMembership.Patch`, a team role invisible to `mayReach`, `Vouch.Link` across a tenant, and a select reaching an erased parent. `cmd/as*_test.go` |
| — | F16, upstream | **done** — F15's redactor was written for one of the **three** recorders reading the same `bare.Change`; `watchRecorder` and `outboxRecorder` went on marshalling the patch raw, so an `outbox` row held a verifier at rest and the first broker carrying a patch would have carried one off the box. `lesomnus/payday@7ff5e8f`, pin moved, `cmd/outboxsecret_test.go` |
| — | F20 · two quiet generator holes | **done** — an overlay redeclaring a generated rpc replaced it and nothing said so (`lesomnus/payday@15a0e47`), and one whose name matched no contract was never merged at all (`@a06360f`). Plus `Email.date_verified`: a caller could assert their own address had been checked, which nothing reads **yet** — closed in `server/core/email.go`, because `immutable:` removes a field from Patch and keeps it in Add, which is backwards here. And payday now has the declaration this wanted — `payday.field.stamped`, `@1c2b63e` — which roster takes the moment payday's buf module is published again |
| — | F19 · an edge is a read | **fixed upstream** — the widest thing found here, and it needed a clerk. `Email.vouched_by` is not the path to anybody's tenant, so the gate never asked about it — and a nested select walks it, so `Email.Add` + `Email.Get` read another tenant's identity, that person's name, and their tenant. F14's shape with scope where liveness was. Both key forms, and the by-subject one needs no identifier and answers as an oracle. `lesomnus/payday@7d19dea` and `@51284cf`, pin moved, `cmd/foreignedge_test.go` |
| — | D52 · WebAuthn | **done** — the largest thing OPERATING.md listed as not here, and it needed no new decision: D20 designed it while arguing about TOTP. roster verifies because the **signature counter** is state and state belongs to the row; the relying party, origins and challenge arrive inside the presented bytes, because the request is generic across kinds. Burns one ECDSA rather than one argon2, which is `kind.go`'s finding a second time. `server/vouch/webauthn.go`, `vouchtest` |
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

## See also

- [PLAN.md](../PLAN.md) — the decisions, and the twelve subjects
- [POSITION.md](POSITION.md) — the line these are all on the near side of
