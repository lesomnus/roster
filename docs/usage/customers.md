# Customers

A **tenant** is a customer. A **holder** is somebody in one. Everything else in
roster hangs off those two. How anything is named is [cli.md](cli.md).

## A tenant

```sh
roster --as admin tenant add @newco
roster --as admin tenant add @newco '{"name": "Newco Ltd"}'
```

`alias` is what people type; `name` is what they read. Neither is an identifier --
that is minted, and it is what every other row points at.

### Giving it an identifier

```sh
roster --as admin tenant add @newco '{"id": "019ff2ab-…"}'
```

Almost never, and once in a way that matters: an app served by this roster anchors
its own rows on the identifier a credential carries, and when that app also has
the tenant written down as a constant, the two must agree **from the start**.

Getting it wrong is not an error, which is why it is worth saying here. Both sides
come up, somebody signs in, and the app makes a *second* tenant because the
identifier it was handed is not one it has -- two rows for one organisation, the
rows that belong together split across them, nothing failing. It has to be a
tenant-domain identifier, and `Tenant.Add` refuses anything else.

## A person

```sh
roster --as admin holder add @newco/alice
roster --as admin holder add @newco/alice '{"name": "Alice Nguyen"}'
```

That row is not yet somebody who can do anything, or somebody who can sign in.
Those are two separate things, and they are the next two pages:

- what they may **do** -- a role and a binding -- is [permissions.md](permissions.md)
- how they **prove who they are** is [ways-in.md](ways-in.md)

Neither is written for you, deliberately. A command that created a person with a
password would put a secret on the screen before there was anybody to give it to,
and one that created them with permissions would decide something only you know.

### How much is written of anybody

Everything a caller writes onto a row has a length. A write past it is refused
-- `InvalidArgument`, naming the field and the bound -- and nothing is cut to
fit. Each number is past anything the thing it bounds is, and together they are
small enough that a full page of any list is one answer: nobody makes their
organisation's list of people unreadable by what they write about themselves.

| what | at most | counted in |
| --- | --- | --- |
| an `alias`, of anything | 63 | bytes: small letters, digits and single hyphens |
| a `name`, of anything -- a person, a team, a site, a group, a role, a tenant | 256 | characters |
| a `desc`, of anything | 1,024 | characters |
| `labels` | 32 on one row; a key 128, a value 256 | bytes |
| a profile's `display_name` and `department` | 256 each | characters |
| a profile's `employee_no` | 64 | characters |
| a profile's `locale` | 35 | characters |
| a profile's `picture` | 2,048 | bytes; an `https` address |
| `data` | 4,096 | bytes, its type's name and its value together |
| an address | 254, and 64 before the `@` | bytes |
| a number | 64 | characters |
| what a directory calls somebody: its id for them, its name for them | 255, 256 | bytes, characters |
| a `subject` at a provider | 255 | bytes |
| a list of `methods`, on a role, a key or a delegation | 1,024 entries; an entry 255; 16,384 in all | bytes |
| a connection's `issuer` | 2,048 | bytes |
| a connection's `scopes` | 32 of them; each 255 | bytes |
| an identifier: a connection's `client_id` and `secret_ref`, the `provider` a mail domain names, the `client` a group is shown to, each of the words a consent is said in | 255 | bytes |

A name and a note are counted in characters, so a name is as long in Hangul as
in Latin; what is compared and not read -- a subject, a method, an identifier
-- in bytes. A list of methods has a length in all as well as a count: roster
serves some two hundred methods, which named one by one are six thousand
bytes, and a list that means all of them is one pattern (`/roster.*/*`).
`data` is one slot for a few facts an app keeps beside a person; what
is large, or has to be looked up, the app keeps itself, by the person's
identifier.

An **address** is also held to being one, in one spelling: the plain mailbox,
`someone@example.com`. Small letters; before the `@`, letters, digits and the
marks an address is written with (``!#$%&'*+-/=?^_`{|}~``), with single dots
between; after it a hostname, with no dot at its end. A name beside the
address, brackets round it, a quoted or a commented mailbox and a line break
are each refused, and so is a letter outside ASCII before the `@` -- for now:
the same letter has more than one encoding, and taking them later breaks
nobody, where refusing them later would. A name after the `@` in its own
script is written as its A-label (`xn--…`), as a hostname is. Whoever writes
an address is held to this -- a person, an administrator, a directory, a
provider's word at a sign-in -- and what somebody *types* to sign in or to
recover an account is folded to small letters and looked for, as before.

## Standing a customer up

One write, and then a way in:

```sh
roster --as admin tenant add @newco '{"name":"Newco Ltd"}'
roster --as admin vouch reset @newco/admin          # a password, printed once
```

The first command writes five rows: the tenant, `admin` in it, a role called
`everything` that holds `/roster.*/*`, the binding, and a role called
`self-service` that nobody is bound -- what everybody of the tenant does to
their own account ([permissions.md](permissions.md) § *What somebody does to
their own account*). The second is the ordinary way anybody gets a password,
pointed at the person the first made.

`everything` is a **pattern** and not a list. A list written the day a customer
is created is what existed that day; the next release adds an RPC their
administrator cannot call and cannot grant themselves either, because granting
is refused for anything the granter does not already hold. It is still an
ordinary role -- unbind it and it is gone, erase it and every binding to it goes
too.

### Why `add` writes more than the tenant

It used to write one row, and the rest were yours. This page argued they
should stay separate writes: each is held to the same rules every other
write is, and a composite would be one more thing to hold to them.

That argument is about a **caller** composing them, and `Tenant.Add` is not one.
The writes after the row go back through the same layer they would have
arrived at on their own, so a role naming methods the caller does not hold is
refused there exactly as `Role.Add` refuses it.

What the separate writes actually left was worse than the risk: a tenant with **nobody who
could do anything in it**, finishable only by a roster operator reaching inside
through `admin.addr` -- where standing comes from the port and two escalation
rules are waived. That is what made *let a customer manage their own people*
mean *act as the operator*.

It is one transaction, which four calls could not be: a refusal part way leaves
nothing, rather than a tenant nobody can get into whose alias cannot be used
again.

**The control plane's tenant is not a customer**, and `Tenant.Add` does nothing
extra there: its one tenant is the deployment itself, and its first holder is
`roster init`'s -- named by `--operator`, bound, and given a password in the
same act.

A roster operator with no shell does the same from the admin console's customers
screen, over `admin.addr`, as a session and through every rule. Nothing is only
in one path: [operating.md](../operating.md) § "The admin console" is why that
works, and what it costs.

## Where a customer's requests come from

If your product resolves a customer by hostname, the name has to be a row. A front
door asks `FrontService.WhoseHost` before it knows anything else and gets back a
tenant identifier and nothing more. The Login App reads the same rows to work out
which tenant a flow is about, from the redirect the authorization request named.

**There are two roads to a row**, and `Host.date_proved` is which one it was:

| who | how |
| --- | --- |
| a tenant's own administrator | claim the name, publish what roster asks for, take it |
| a roster operator | write the row. Whoever routed the name is writing it |

The operator's road is one call, and it is the one to take when you route the name
and run the zone:

```sh
roster --as admin host add '{"tenant":{"alias":"newco"},"name":"newco.example.com"}'
```

The customer's road is two calls and a TXT record, and is what makes it safe for
them to have `/roster.HostService/Add` at all --
[operating.md](../operating.md) § "A tenant registers its own hostname" is the
exchange.

**This section used to say the opposite** -- that `Host` was the deployment's to
write and never a customer's, on the grounds that a customer claiming a name takes
it from whoever holds it and that roster resolves no DNS because it runs in an air
gap. The first half was a real problem and the answer is the proof above rather
than a permission nothing could enforce. The second half was answering the wrong
question: an air-gapped deployment has no tenant registering hostnames either, and
what it has instead is an operator with a shell -- which is the second road, and
why `host.resolver: none` exists.

A `MailDomain` answers a different question -- which tenant an **address**
belongs to, and where the people at it authenticate. It claims nothing and is
unique only *within* a tenant, so two operators saying something about
`@gmail.com` are two facts and this one is safe to hand out.

```sh
roster --as admin mail-domain add '{"tenant":{"alias":"newco"},"name":"newco.com","provider":"entra"}'
```

## Removing somebody

```sh
roster --as admin holder erase @newco/alice  # unreachable, and destroys nothing
roster --as admin restore @newco/alice          # while the grace lasts
roster forget @newco/alice           # destroys, including the trail's copy
```

`erase` is *this person has left*: two columns written, and the alias, addresses,
identities and the whole trail still there. `forget` is *destroy what you hold
about them*, which is a different request with a legal clock on it, and
`holder.forget_after` is the window between them. `restore` gives back the
account and not how they were signed in: everything issued to them before it is
void, and they sign in again. Nor the roles, the groups and the bindings that
were of them as an app (`Role.app`, `Group.app`, `Binding.app`): those are
erased with the holder, and stay erased. Their keys are the exception, and their alias may
have been taken meanwhile; [operating.md](../operating.md) § "Destroying
somebody, which an erase does not" says what to do about each.

**What hung on something erased is still listed.** An erase writes the row it
names and no other -- of a person, a team, a group, a role or a site alike. So
somebody's places in sites, teams and groups, the bindings to them and the
relations other people have to them stay where they were, and so do the places
in a team or a group that was erased, the bindings to that group and of an
erased role, and the clients an erased group was shown to. A `list` goes on
answering each, pointing at something `get` answers `NotFound` for. None of
them gives anybody anything: an erased role, team or group grants nothing, and
somebody erased signs in nowhere. A reader that draws such a list reads what
each row names and leaves out the rows whose thing is not there. Somebody who
wants one gone erases it like any other row of its kind; one that gives
nothing because its role, its team or its group is gone is erased by whoever
may erase where it is
([permissions.md](permissions.md#taking-away-is-held-the-other-way-round)).

**Or they leave by themselves**, where the tenant offers it: `config.leave` on
the tenant (the admin console's *people here may delete their own account*, or a
resources file), and the account page draws a *delete your account* section that
erases them exactly as the first line above does, signed out everywhere first.
`roster restore` brings them back the same way, without any of their old
sign-ins. It is off unless a tenant turns it on, because a directory a company
provisions is the company's to empty -- a person deleting themselves out of it is
somebody the directory can neither find nor make again until they are forgotten,
or an administrator erases the identity and the address they left, and then makes
again with nothing of theirs -- and where a tenant with a directory
does turn it on, the people that directory provisions are refused all the same
([scim.md](../scim.md)). A consumer product's
directory, where an account is the person's own, is the case it is for: Loft's
SaaS turns it on for the one tenant every account is in, and links to the account
page's `/#leave` from its own.

**Such a product sets `holder.forget_after` as well.** It is never unless set,
and until somebody is forgotten their addresses and their accounts at providers
are still theirs, for a restore to give back. So one who deletes their account
and signs up again is not made again: at the front door they are answered *this
account was deleted here; an administrator of your organisation can bring it
back, or let it go*, at every try, until an operator restores or forgets them.
With the window set they are refused inside it, where `roster restore` gives
them back what they had, and arrive as somebody new once it has run out.

[ways-in.md](ways-in.md) § *Deleting your own account* is what it asks first.
[operating.md](../operating.md) § "Destroying somebody, which an erase does not"
is what each one leaves behind.

## A customer leaving

The same three steps one level up, and a fourth for the name:

```sh
roster --as admin tenant close @newco  # nobody of it gets in, and nothing is destroyed
roster --as admin restore @newco   # while tenant.restore_within lasts
roster forget @newco            # every person forgotten, every other row of it gone
```

and its alias stays taken for `tenant.alias_hold` -- a year unless the deployment
says otherwise -- so that nobody can be *newco* to its former people the day after.
What somebody who deletes their own account is told is what is done: that it is
forgotten *from* the day `holder.forget_after` runs out, by the first pass after it
and not before, that it can be brought back until then, that a legal claim puts it
off, and that two things are kept past it -- what they agreed to, for
`consent.keep`, and the record of what was done and of who read their data, by
identifier. The account page and `roster me leave` both say it.

An operator's `roster hold place --why … @newco` stops the tenant being destroyed,
its people being forgotten on the clock and its alias being given back, and keeps
what the trail has of it. It does not stop `roster forget @newco/erin`, which
destroys that person's rows under a hold as under none and keeps the trail's lines
about them until it lifts; nor the grace, which runs on, so that a closed tenant held
past `tenant.restore_within` is destroyed when the hold is lifted.
[operating.md](../operating.md) § "A tenant leaving" is the long version.

## Next

[ways-in.md](ways-in.md) -- how somebody proves who they are.
