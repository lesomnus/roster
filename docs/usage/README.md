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

**Not the design.** [`../../PLAN.md`](../../PLAN.md) is every decision and why.
If a page here says *this is refused* and you want to know what it cost to
decide that, the answer is there and not here.

## A note on the CLI, since everything here uses it

`roster tenant add` and `TenantService.Add` are the same call. Every entity has
the same six — `get`, `ls`, `watch`, `add`, `patch`, `erase` — and the command
is a client for exactly the RPC of that name, so what you learn typing is what
you write in code.

Two things do **not** follow from that, and both matter:

- **The local CLI has no wall, no gate and no rules.** It opens the database and
  writes through `Ungated`, which is the instance the deployment does its own
  work through. So a command succeeding tells you the write is *possible*; it
  tells you nothing about whether a caller holding a role could make it. That is
  [permissions.md](permissions.md), and it is why the first role in a tenant can
  be written at all.
- **Not every RPC has a command.** The hand-written services — `MeService`,
  `FrontService`, `AuthService`, most of `VouchService` — are what an app calls,
  not what an operator types. `roster vouch` covers the three an operator needs
  and no more.

Point the commands at a running deployment instead by setting `client.addr`, and
then the wall and the gate do apply, because it is an ordinary caller. See
[first-run.md](first-run.md) § reaching a deployment that is not this process.
