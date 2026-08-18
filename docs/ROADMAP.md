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

**Three of them are one table.** D21's `continuation`, D23's delegation token
and item 3's magic-link nonce are each described with the same four words —
opaque, short-lived, single-use, bound to the caller it was issued to. Built
separately they are three expiry sweeps and three ways to get single-use wrong.

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

### P1 · `Ticket`, the table three things wait on

**Forces the order:** D24 puts the delegation token first, and the token is a
row in this table.

Modelled on `ApiKey`, which has already argued every question this asks:
256 bits from `crypto/rand`, a **deterministic unsalted hash with a unique
index** because the verifier is also how the row is found, `(payday.field) =
{secret: true}` so the trail does not keep a second copy, and the generated
service closed the way `CredentialService` is.

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

### P2 · The delegation token (D23), and two fields on `Holder`

**Forces the order:** everything that draws a screen about a person needs the
token (D23 says so, and D24 §4 and §5 are the screens).

The prefix is not a new idea. OPERATING.md already says *the prefix decides
which database holds the row and who the token is served as*, so a third value
is that rule's next entry rather than an exception to it. It resolves to the
holder, exactly as `rt_` does, and is never wider than they are.

Beside it, the two `Holder` fields — item 6 and item 12. They are here rather
than later because item 3 needs the first one anyway: a password reset that
leaves old sessions alive is not a reset.

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

1. **Is `Ticket` one entity or several?** D16 refused `ApiKey` as a `Credential`
   of `kind: "api-key"` and gave three reasons — uniqueness, what is being
   proved versus what is being granted, and the cost of the hash. Whether that
   argument lands here in the same shape is the question, and it is the first
   thing P1 has to answer rather than assume.
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
| P1 | `Ticket` | **in progress** |
| P2 | delegation token · `Holder` epoch and disabled | — |
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
