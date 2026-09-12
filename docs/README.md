# roster's documentation

Five questions, and the page that answers each. Every fact is meant to live in
exactly one of them: where two pages touch the same ground, one of them says so
and links.

## 1. Is this the right thing?

[position.md](position.md) -- what roster owns, where it stops, and why the line
is worded as a test (*who checks this?*) rather than as a list of features. Read
this before deciding it cannot do something: two of the "cannot"s in this
repository's history were a layer away.

## 2. Get it running

[../README.md#quick-start](../README.md#quick-start) is `docker compose up` and
five commands from a checkout.

[usage/tutorial.md](usage/tutorial.md) is the same ground slowly: an empty
directory, a customer, somebody who signs in, a service that calls. `cmd/tutorial_test.go`
runs it, so the page and the binary cannot disagree.

## 3. Use it

Task-shaped, in this order. Everything here is the CLI, and every command is a
client for exactly the RPC of that name.

| | |
| --- | --- |
| [usage/cli.md](usage/cli.md) | the two modes, how anything is named, what a reference looks like in JSON |
| [usage/customers.md](usage/customers.md) | a tenant, the people in it, standing one up |
| [usage/ways-in.md](usage/ways-in.md) | passwords, keys, delegations, provider accounts, second factors -- and stopping one |
| [usage/permissions.md](usage/permissions.md) | roles, bindings, groups, sites, teams, and what you may hand out |

## 4. Run it

[operating.md](operating.md) -- which database, the two planes, the listeners, the
console, one process or four, the audit trail and its retention, destroying
somebody, replicas, TLS.

[ldap.md](ldap.md) is the directory, for the clients on a network that speak LDAP
and nothing else.

## 5. Sign people in

[login.md](login.md) -- the path a password takes, what changes when Hydra is in
front, every hop and the call it makes, and the HTTP contract a sign-in page of
your own is written against.

[relying-party.md](relying-party.md) -- the two shapes an app in front takes, with
a runnable example of each, and which gate can see what.

## Reference

| | |
| --- | --- |
| [entity.md](entity.md) | the twenty-three tables, drawn, with a paragraph each |
| [glossary.md](glossary.md) | the words -- the wall, the gate, a grant, a layer, a plane -- and the four that name two things |
| [baseline.md](baseline.md) | the promises a normal user relies on, each pinned to the tests that hold it |
| [development.md](development.md) | working on roster: generation, upgrading payday, the pages, the sandbox |
| [roadmap.md](roadmap.md) | how it was built, in order, and what each thing cost to get wrong first |

## Where a *why* lives

Beside the thing it decides -- the proto comment, the layer's file comment, the
`server/core` file named after the rule. There is no central log of decisions and
there deliberately is not one: [roadmap.md](roadmap.md) is the record of what
happened, and the reasoning is next to the code it constrains.
