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

Grown from `examples/sso`.

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

**Depends on P1** for the continuation and **on P5** for a way to enrol a second
factor at all.

### P8 · Recovery and the magic link (item 3)

**Depends on P1** (the nonce), **P2** (the epoch), and **P4** for the front door
that most links are asked for through. In an air gap it is P5's operator instead
of the mail, which is the same mechanism reached differently.

### P9 · The rest, in no forced order

The event stream (item 4's second increment), the breached-password check (item
5), per-tenant provider connections (item 9, which needs a decision first), the
console's session table, and extracting the components (D24 §6, last for D24's
own reason).

## Decisions to take before the code that needs them

Each takes a `D` in PLAN.md when it is taken. None is taken here.

1. ~~**Is `Ticket` one entity or several?**~~ Answered, above: two of D16's
   three reasons land, so the delegation token is its own entity and the
   continuation and the nonce are not settled by that choice. It takes a `D`
   once P1 is written.
2. **The rule for item 11.** *Only somebody whose permissions are a subset of
   yours*, or *a tenant operator is a tenant administrator, and we say so*.
3. **Item 9's boundary.** A provider connection carries a client secret, and
   answering with one makes it the first secret roster returns rather than
   checks. It argues with D13.
4. **Where the session table lives.** PLAN.md says roster is an app that makes
   tables, and that is true — but `MemStore` being the only store payday ships
   means the next payday app rewrites this one too. That is the shape the one
   rule is about, so it is worth asking upstream first.

## Progress

| | | |
| --- | --- | --- |
| P0 | F9 — a reference reached erased rows | **done** — fixed in `protoc-gen-orm-ent`; `vouch` refuses; pin still to move |
| P1 | `Delegation` | **in progress** |
| P2 | `Holder` epoch and disabled, and the refusals | — |
| P3 | the reference app's spine | — |
| P4 | hostname, mail domain, and F7 | — |
| P5 | escalation over credential writes, then the write surface | — |
| P6 | the reads a screen needs, and the screens | — |
| P7 | two-step verification | — |
| P8 | recovery and the magic link | — |
| P9 | the rest | — |

## See also

- [PLAN.md](../PLAN.md) — the decisions, and the twelve subjects
- [POSITION.md](POSITION.md) — the line these are all on the near side of
