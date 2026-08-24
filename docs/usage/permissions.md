# Permissions

Deny by default. A caller may call a method when something written down says so,
and nothing does until you write it.

## The one thing the gate reads

A **role** is a list of methods. A **binding** attaches one to somebody.

```sh
roster role add @newco/support '{"methods":["/roster.HolderService/Get",
                                            "/roster.HolderService/List"]}'

echo '{"role":  {"alias":{"alias":"support","tenant":{"alias":"newco"}}},
       "holder":{"slug": {"alias":"alice",  "tenant":{"alias":"newco"}}}}' \
  | roster binding add -
```

A method is written in full — `/roster.HolderService/Get` — and `*` is allowed
in either half:

| | |
| --- | --- |
| `/roster.HolderService/Get` | one method |
| `/roster.HolderService/*` | every method of one service |
| `/roster.*/*` | every RPC roster serves, now and after an upgrade |

A pattern is evaluated rather than expanded, so two replicas on different
versions agree about what somebody holds.

`/roster.*/*` deliberately does **not** cover payday's own package —
`BatchService` and `TokenService` are outside it. A deployment that wants those
grants them on purpose.

## What a caller may see

Two separate questions, and both are answered before your code runs:

- **the wall** narrows every read to the tenants the caller belongs to. It is a
  predicate on the query, so it applies to reads and not to `Add` — there is no
  row yet to narrow.
- **the gate** decides whether the method may be called at all, and for an `Add`
  it also checks that the rows the new row hangs off are ones this caller can
  see.

Neither is a thing you configure. The wall comes from the credential and the
gate from the roles.

## Four ways a binding reaches somebody

`cmd/policy.go` answers from three sets, and a fourth entity narrows rather than
widens. Getting this wrong in either direction is how permission systems leak,
so it is worth reading once:

| | | |
| --- | --- | --- |
| **Binding** → holder | a role written to one person | widens |
| **Binding** → group | a role written to a **group**, so everybody in it | widens |
| **TeamMembership** | a role held inside one team | widens, at the team's site |
| **Binding.site** | the same role, but only within one site | narrows |

> **A grant is any write that changes what the gate will answer for somebody.**

Which is wider than the writes that name a role. `GroupMembership.Add` names
none and hands over every binding written to that group; `ApiKey.Add` names none
and hands over a credential that acts as somebody. All of them are refused
unless the caller already holds what they are handing out.

## A group

People who belong together for the purpose of being granted things at once.

```sh
roster group add @newco/oncall

echo '{"group": {"alias":{"alias":"oncall","tenant":{"alias":"newco"}}},
       "holder":{"slug": {"alias":"alice", "tenant":{"alias":"newco"}}}}' \
  | roster groupmembership add -

echo '{"role": {"alias":{"alias":"support","tenant":{"alias":"newco"}}},
       "group":{"alias":{"alias":"oncall", "tenant":{"alias":"newco"}}}}' \
  | roster binding add -
```

A group is a **tenant-wide** set. Its members may sit in different sites, which
is the difference from a team.

## A site

A place: a region, an office, a subsidiary. It bounds what a binding reaches.

```sh
roster site add @newco/eu

roster role add @newco/eu-support \
  '{"site":{"slug":{"alias":"eu","tenant":{"alias":"newco"}}},
    "methods":["/roster.HolderService/Get"]}'

echo '{"role":  {"alias":{"alias":"eu-support","tenant":{"alias":"newco"}}},
       "holder":{"slug": {"alias":"alice",     "tenant":{"alias":"newco"}}},
       "site":  {"slug": {"alias":"eu",        "tenant":{"alias":"newco"}}}}' \
  | roster binding add -
```

A binding with no site reaches the whole tenant. One made in a site reaches that
site alone — and that is also the scope the escalation rule compares, so a site
administrator can grant inside their site and not across the tenant.

A role that names a site may only be bound in that site.

`SiteMembership` exists and **is read by nothing**: it records where somebody
is, and does not change what they may see. What decides that is the site on the
binding.

## A team

A set of people **within a site**, each holding a role in it.

```sh
roster team add @newco/eu-ops '{"site":{"slug":{"alias":"eu","tenant":{"alias":"newco"}}}}'

echo '{"team":  {"slug": {"alias":"eu-ops","site":{"slug":{"alias":"eu","tenant":{"alias":"newco"}}}}},
       "holder":{"slug": {"alias":"alice", "tenant":{"alias":"newco"}}},
       "role":  {"alias":{"alias":"eu-support","tenant":{"alias":"newco"}}}}' \
  | roster teammembership add -
```

**A team is named within its site, not its tenant.** `TeamRefBySlug` carries a
`site`, so a team created without one can only be referred to by identifier:

```sh
roster team ls -o name        # and use the identifier
```

The role a team membership names is granted at the team's site — a team with no
site answers the tenant. Which is why attaching a role to somebody *is* granting
it, and is checked as one.

## Group or team?

Both put people together and they answer different questions.

| | group | team |
| --- | --- | --- |
| scope | the whole tenant | one site |
| members | may be in any site | of that site |
| carries a role | no — a **binding** to the group does | yes, per member |
| used for | *grant these people this* | *these people do this here* |

A group is a handle you point a binding at. A team is a structure with roles
inside it.

## You cannot hand out what you do not hold

Every write above is refused if the caller does not already hold what the write
hands over:

```
role.methods: you do not hold /roster.HolderService/Erase here, so you may not grant it
```

Each method must be covered by **one** thing the caller holds, on its own.
Asking whether the union covers it would let somebody holding every service of a
package hand out the package — true today and wrong the moment a service is
added.

What counts is what they hold **wide**, through a binding. A role held inside
one team does not let them write a tenant-wide binding of it, because that would
be widening a scope rather than passing a permission on.

### And the reason the first role can be written at all

The rule is waived where there is **no caller**: `roster init`, a seed, and the
local CLI, which all write through the instance the deployment does its own work
through. That is the only place it is waived, and every later grant descends
from somebody who already held it.

So a command succeeding at a shell says nothing about whether a caller could
make the same write. If you are working out what a role needs, test it as a
caller — `client.addr` with a key, or the tutorial's last section.

## Seeing what somebody holds

```sh
roster binding ls -o wide
roster role ls -o wide
roster role get 01a0337b-a3e5-8c98-810f-d8d04db3e47e
```

**`Role`, `Group` and `ApiKey` cannot be named `@tenant/alias`** the way a
holder, a site or a team can — only by identifier, which `-o name` prints. It is
not a rule about those entities: the CLI recognises a reference whose oneof
field is called `slug`, and those three declare theirs as `alias`. `add` is the
exception and takes the name either way, because there it is setting a field
rather than finding a row.

```sh
roster role ls -o name | head -1        # the identifier a script wants
```

and, as the person themselves, over the wire:

```
MeService.Get → { alias, tenant, methods: ["/roster.*/*"], sites, every_site, teams }
```

`methods` comes back as the **pattern**, not what it expands to. A page that
expanded it would show what exists in one binary.

## Next

[tutorial.md](tutorial.md) — all of it once, on a deployment that answers.
