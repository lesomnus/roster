# Using roster

Task-shaped pages, in the order they are meant to be read. Everything here is
the **CLI** unless it says otherwise: `roster <thing> <verb>` is the shortest
description of what an RPC does, and a page that showed both would be two
descriptions to keep in step.

If you have half an hour and no deployment yet, read [the tutorial](tutorial.md)
first and come back. It is the same ground with nothing branching off it.

## In order

| | |
| --- | --- |
| **1.** [first-run.md](first-run.md) | the configuration file, `init`, `serve`, and what exists afterwards |
| **2.** [customers.md](customers.md) | a tenant, the people in it, and how anything is named |
| **3.** [ways-in.md](ways-in.md) | passwords, keys, provider accounts — what each is and who checks it |
| **4.** [permissions.md](permissions.md) | roles and bindings, and the four other things that widen what somebody can see |
| **5.** [tutorial.md](tutorial.md) | one deployment, end to end: a customer, a person who signs in, a service that calls |

The first four answer *what is this and how do I write one*. The tutorial
answers *what does a working deployment look like*.

## Which page answers your question

| you want to | |
| --- | --- |
| get roster running at all | [first-run.md](first-run.md) |
| add a customer | [customers.md](customers.md) |
| let somebody sign in | [ways-in.md](ways-in.md) § a password |
| let a service of yours call roster | [ways-in.md](ways-in.md) § a deployment key |
| let a customer's app call as one of their people | [ways-in.md](ways-in.md) § a tenant key |
| decide what somebody may do | [permissions.md](permissions.md) |
| stop a credential, now | [ways-in.md](ways-in.md) § stopping one |
| put somebody's SSO account on their row | [ways-in.md](ways-in.md) § an account somewhere else |

## What this is not

**Not the reference.** [`../entity.md`](../entity.md) is the twenty-three
entities and how they relate, one paragraph each; this is what to type.

**Not operations.** [`../operating.md`](../operating.md) is the long version —
ports, the console, the audit trail, retention, upgrades. Where the two overlap
it is the same material at a different length, and `operating.md` is the one
that goes deeper.

**Not the design.** The why of a refusal is written beside the thing that
refuses — the proto comment, the layer's file comment, or
[`position.md`](../position.md). If a page here says *this is refused* and you
want to know what it cost to decide that, that is where the answer lives.

## A note on the CLI, since everything here uses it

`roster tenant add` and `TenantService.Add` are the same call. Every entity has
the same six — `get`, `ls`, `watch`, `add`, `patch`, `erase` — and the command
is a client for exactly the RPC of that name, so what you learn typing is what
you write in code.

**It is not an operator's tool.** It has two modes, and which one you are in is
decided by the configuration rather than by who you are:

| | |
| --- | --- |
| **local** — no `client.addr` | opens the database and writes through `Ungated`: no wall, no gate, no rules. A shell on the box, doing what the deployment can do |
| **remote** — `client.addr` set | an ordinary caller. The wall narrows what comes back and the gate decides what is allowed, exactly as for any other client |

So a customer's own person uses the same binary. Their configuration has no
`db:` block at all — there is no database to open — only where to call and what
to call with:

```yaml
client:
  addr: "roster.internal:50051"
  auth:
    scheme: bearer
    credential_file: ~/.roster/key      # an rt_, which resolves to them
```

```sh
roster holder ls -o table              # the people in their tenant, and no others
roster tenant ls                       # PermissionDenied, if their role does not say so
```

The one thing that does not follow: **a command succeeding locally says the
write is possible, not that a caller could make it.** The local mode is outside
every rule, which is why the first role in a tenant can be written at all. If
you are working out what a role needs, test it remotely with a key.

`roster init`, `roster key` and `roster vouch` have no remote form. What they
write is not served, which is the whole reason they are commands.

### Not every RPC has a command yet

Nineteen do not, and that is a gap being closed rather than a boundary: the goal
is that everything is possible from a terminal, with no console anywhere in the
path. `HolderService`'s `Disable`, `Enable` and `Invalidate`, and most of
`VouchService`, are the notable ones. Where a page here shows an RPC and no
command, that is why. The D58 row in [`roadmap.md`](../roadmap.md) is the list.

`Apply` is the exception and is not on that list. It is one of the two general
writes, closed unless a deployment opts in, and roster does not.
