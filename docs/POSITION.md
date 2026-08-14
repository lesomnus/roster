# What roster is, and where it stops

roster began as one thing: **an employee directory that does not depend on an
external identity provider.** People, the addresses they use, and the link
between them — held by us, in our schema, so that changing IdP is changing a
login screen rather than changing the record of who works here.

Everything since has been that sentence answering its own consequences. This
document is the boundary, so that the next good idea can be measured against it
rather than argued about.

## What roster owns

| | |
| --- | --- |
| **A person** | `Holder`. Its identifier is the `sub` every product knows them by, which is why it is roster's and not a provider's |
| **How they sign in elsewhere** | `Identity` — a `(provider, subject)` pair, so Entra at work and GitHub for the same human is one person |
| **Addresses** | `Email`, several per person, each with whether anybody checked it |
| **How they sign in here** | `Credential` — a password when there is no provider in front, verified here and never handed out |
| **Where they belong** | `Tenant`, `Site`, `Team`, and the memberships between |
| **Who may call this** | `ApiKey` in the control plane, `Role`/`Group`/`Binding` in the data plane |

The first four are the original sentence. The last two are what it costs to
serve the first four to more than one product.

## What roster is not

**Not an identity provider.** roster does not implement OIDC, issue tokens
anybody else verifies, run a login flow or hold a session. Ory Hydra is the
protocol and a Login App is the flow. roster answers the question they both ask
— *who is this?* — and owns the answer.

**Not a product's own database.** custody keeps its own rows and anchors them to
roster's identifiers. What roster holds is what every product would otherwise
hold a stale copy of.

**Not a policy engine.** See below, which is the point of this document.

## Authorization: what roster does

Three things, and they are chosen to be the ones an employee directory actually
needs.

**1. The tenant wall.** Every row belongs to a tenant and no read crosses one.
This is payday's and is not configurable — there is no rule anybody can write
that turns it off.

**2. Roles bound at a scope.** A `Role` is a list of RPCs. A `Binding` grants it
to a `Holder` or a `Group`, either across the tenant or within one `Site`. This
is Kubernetes' shape: `Site` is a namespace, a role with no site is a
`ClusterRole`, a binding with one is a `RoleBinding`.

**3. Built-in rules about roster's own entities.** "The administrator of a team
manages its members" is not configuration — it is true of every deployment there
will be, so it is roster's rule, in roster's layer, tested once. A configurable
invariant is one that every deployment configures identically until one of them
gets it wrong.

That is the whole of it. Union only, no deny rules, no conditions, no
inheritance.

## Where roster stops, exactly

The line is **transitivity**. roster answers questions of the form:

    does this subject hold a role that allows this method, here?

One lookup, a union, an answer. What it cannot answer, and will not grow to:

- **Permissions inherited through a graph.** "Alice may edit this document
  because it is in a folder owned by a team she is in." Every hop is a join, the
  hops are not known in advance, and the answer depends on the shape of data
  roster does not hold.
- **Permissions computed from other permissions.** `viewer = editor +
  parent.viewer` — a rewrite rule, evaluated recursively.
- **Permissions on arbitrary objects.** roster knows about people, teams, sites
  and its own rows. A product's documents, repositories and machines are the
  product's, and roster has no name for them.

**That is Zanzibar**, and it is solved: SpiceDB and OpenFGA are the open
implementations. They exist because of exactly those three things — relation
rewrites, graph traversal, and doing it consistently at a scale where the
consistency needs its own protocol.

So the rule is short:

> If the question needs a **graph**, it is not roster's. If it needs a **list**,
> it is.

A deployment that reaches the line runs an authorization service beside roster
and feeds it roster's memberships. That is the intended shape, not a defeat:
roster is where the memberships are true, and a relationship engine is where
they are traversed.

## What we deliberately do not have, and what it would take

| | why not | what it would cost |
| --- | --- | --- |
| deny rules | order stops mattering without them, and a precedence table is a thing nobody holds in their head | a decision about precedence, forever |
| `resourceNames` | `gate.Policy` cannot see what a call is **about**; only who is asking. So object rules are layers, and a declarative form needs a seam payday does not have | a payday change |
| nested groups | one join becomes a traversal, which is the line above | see Zanzibar |
| conditions / ABAC | "during working hours", "from this network" — an expression language, and then a debugger for it | a language |

Escalation prevention **is** here, and used to be the last row of that table.
Nobody hands out what they do not hold: `server/core/escalate.go`, on
`Role.Add`, `Role.Patch`, `Binding.Add` and the methods of an API key -- four
places rather than the one that row estimated -- plus the rule that a role
scoped to a site is bound only in that site. `OPERATING.md` has the operator's
half of it.

## Two planes, one schema

roster runs twice in one process — a data plane holding customers and their
people, a control plane holding the deployment's own services and their keys.
Same schema, different instance, and a `Holder` means a person in one and a
caller in the other.

That is not a trick. It is what the first sentence of this document implies once
more than one product asks the question: somebody has to say which products may
ask, and the thing that answers "who is this" is the thing that should answer it.

## See also

- [PLAN.md](../PLAN.md) — the decisions, with the reasoning that produced them
- [LOGIN.md](LOGIN.md) — what happens when somebody signs in, end to end
