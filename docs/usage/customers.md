# Customers

A **tenant** is a customer. A **holder** is somebody in one. Everything else in
roster hangs off those two. How anything is named is [cli.md](cli.md).

## A tenant

```sh
roster tenant add @newco
roster tenant add @newco '{"name": "Newco Ltd"}'
```

`alias` is what people type; `name` is what they read. Neither is an identifier --
that is minted, and it is what every other row points at.

### Giving it an identifier

```sh
roster tenant add @newco '{"id": "019ff2ab-…"}'
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
roster holder add @newco/alice
roster holder add @newco/alice '{"name": "Alice Nguyen"}'
```

That row is not yet somebody who can do anything, or somebody who can sign in.
Those are two separate things, and they are the next two pages:

- what they may **do** -- a role and a binding -- is [permissions.md](permissions.md)
- how they **prove who they are** is [ways-in.md](ways-in.md)

Neither is written for you, deliberately. A command that created a person with a
password would put a secret on the screen before there was anybody to give it to,
and one that created them with permissions would decide something only you know.

## Standing a customer up

One write, and then a way in:

```sh
roster tenant add @newco '{"name":"Newco Ltd"}'
roster vouch reset @newco/admin          # a password, printed once
```

The first command writes four rows: the tenant, `admin` in it, a role called
`everything` that holds `/roster.*/*`, and the binding. The second is the
ordinary way anybody gets a password, pointed at the person the first made.

`everything` is a **pattern** and not a list. A list written the day a customer
is created is what existed that day; the next release adds an RPC their
administrator cannot call and cannot grant themselves either, because granting
is refused for anything the granter does not already hold. It is still an
ordinary role -- unbind it and it is gone, erase it and every binding to it goes
too.

### Why `add` writes four rows

It used to write one, and the other three were yours. This page argued they
should stay four separate writes: each is held to the same rules every other
write is, and a composite would be a fifth thing to hold to them.

That argument is about a **caller** composing them, and `Tenant.Add` is not one.
The three writes after the row go back through the same layer they would have
arrived at on their own, so a role naming methods the caller does not hold is
refused there exactly as `Role.Add` refuses it.

What the four actually left was worse than the risk: a tenant with **nobody who
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
roster host add '{"tenant":{"alias":"newco"},"name":"newco.example.com"}'
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
roster mail-domain add '{"tenant":{"alias":"newco"},"name":"newco.com","routes":"entra"}'
```

## Removing somebody

```sh
roster holder erase @newco/alice     # unreachable, and destroys nothing
roster restore @newco/alice          # while the grace lasts
roster forget @newco/alice           # destroys, including the trail's copy
```

`erase` is *this person has left*: two columns written, and the alias, addresses,
identities and the whole trail still there. `forget` is *destroy what you hold
about them*, which is a different request with a legal clock on it, and
`holder.forget_after` is the window between them.
[operating.md](../operating.md) § "Destroying somebody, which an erase does not"
is what each one leaves behind.

## Next

[ways-in.md](ways-in.md) -- how somebody proves who they are.
