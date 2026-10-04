# Permissions

Deny by default. A caller may call a method when something written down says so,
and nothing does until you write it.

## The one thing the gate reads

A **role** is a list of methods. A **binding** attaches one to somebody.

```sh
roster role add @newco/support '{"methods":["/roster.HolderService/Get",
                                            "/roster.HolderService/List"]}'

echo '{"role":  {"slug":{"alias":"support","tenant":{"alias":"newco"}}},
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
and hands over a credential that acts as somebody, and so does
`NominationService/Add` -- an app's key answered as somebody in this tenant. All
of them are refused unless the caller already holds what they are handing out.

## A group

People who belong together for the purpose of being granted things at once.

```sh
roster group add @newco/oncall

echo '{"group": {"slug":{"alias":"oncall","tenant":{"alias":"newco"}}},
       "holder":{"slug": {"alias":"alice", "tenant":{"alias":"newco"}}}}' \
  | roster group-membership add -

echo '{"role": {"slug":{"alias":"support","tenant":{"alias":"newco"}}},
       "group":{"slug":{"alias":"oncall", "tenant":{"alias":"newco"}}}}' \
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

echo '{"role":  {"slug":{"alias":"eu-support","tenant":{"alias":"newco"}}},
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
       "role":  {"slug":{"alias":"eu-support","tenant":{"alias":"newco"}}}}' \
  | roster team-membership add -
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
| carries a role | no — a **binding** to the group does | a member may hold one, or none |
| used for | *grant these people this* | *these people work together here* |

A group is a handle you point a binding at. A team is the organisation's own
structure, and a member's role in it is optional.

### Membership is not a permission, outside roster either

What somebody may do is the union of their bindings, the bindings of their
groups and the roles they hold in teams -- and roster guards every write to it:
joining a group is refused unless you hold what the group's bindings hand out.
**A membership nothing is bound to is guarded by nothing**, because it hands out
nothing roster knows of.

So an app must not grant on membership. It asks `HolderService/Reaches` and
checks the method it is about to serve ([apps.md](../apps.md)); anything finer
than one RPC is a method of a permission service the app declares. A product that
checked *is she on the ops team* would be granting something any holder of
`TeamMembership.Add` could hand out, past the rule above.

A client that can only grant on membership -- an LDAP client reading `memberOf`
([ldap.md](../ldap.md)), a proxy reading a token's `groups` -- gets the same
protection one way: **bind the group to a role naming what it grants**, even
though no RPC by that name exists:

```sh
roster role add @newco/jenkins-admin '{"methods": ["/ext.jenkins.Admin/*"]}'

echo '{"role": {"slug":{"alias":"jenkins-admin","tenant":{"alias":"newco"}}},
       "group":{"slug":{"alias":"jenkins-admins","tenant":{"alias":"newco"}}}}' \
  | roster binding add -
```

Joining that group is then refused to anybody who does not hold
`/ext.jenkins.Admin/*` themselves. That includes a tenant's first administrator,
whose role from `tenant add` is `/roster.*/*` and covers no other app's methods:
somebody has to be bound the external permission -- or a pattern over it -- before
they can hand it out, and a roster operator on the admin listener is who starts
that. The same is true of every app's own methods, `/hday.oasys.*/*` included. A
team carries no such binding, so map an external permission to a group and never
to a team.

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
caller -- `client.addr` with a key ([cli.md](cli.md)), or the tutorial's last
section.

## And you cannot write a way into an account wider than yours

The second rule, and the one that does not look like permissions. Resetting a
password is a way to become somebody, so every write that adds one is refused
unless that person's permissions are a subset of the caller's:

| | |
| --- | --- |
| `CredentialService/Set`, `/Issue`, `/Unlock` | their secret |
| `CredentialService/Enrol` | their second factor |
| `IdentityService/Add` | an account at a provider that signs in as them |
| `EmailService/Add` | a mailbox a recovery link is sent to |
| `ApiKeyService/Add`, `/Issue` | a key that **acts as** them |
| `NominationService/Add`, `/Patch` | an app's deployment key answered **as** them in this tenant |

The middle two are worth reading twice before granting. They sound like keeping a
directory tidy, and each is a way to sign in as whoever the row is about: link an
account you control to somebody's `Holder`, or put a mailbox you read on it and
ask for a link.

**What counts as theirs is wider than what they may hand out.** Somebody
provisioned as an administrator through a `TeamMembership`, or through a `Group`,
holds those permissions *for this rule* even though they may not bind them
anywhere. The two readings differ on purpose: missing a path in the first rule
refuses a grant somebody could have made, which is a conversation, and missing one
in the second lets an administrator be reset by anybody.

Changing your own is always allowed, and nothing here stops you **suspending** an
administrator -- that is a denial of service rather than an escalation, and it is
deliberately not covered. `server/core/escalate.go` is both rules, with the file
comment that says how they were arrived at.

⚠️ On `admin.addr` these are **waived**, because an operator's standing there comes
from the port rather than from a role: [operating.md](../operating.md) § "What the
admin port waives" is the whole of it.

## Seeing what somebody holds

```sh
roster binding ls -o wide
roster role get @newco/support
roster role ls -o wide
```

and, as the person themselves, over the wire:

```
MeService.Get → { alias, tenant, methods: ["/roster.*/*"], sites, every_site, teams }
```

`methods` comes back as the **pattern**, not what it expands to. A page that
expanded it would show what exists in one binary.

## Next

[tutorial.md](tutorial.md) — all of it once, on a deployment that answers.
