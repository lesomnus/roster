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

Four writes, and then a way in. This is what the console's *new customer* form
does, in the same order:

```sh
roster tenant add @newco
roster holder add @newco/admin
roster role   add @newco/everything '{"methods":["/roster.*/*"]}'

echo '{"role":  {"slug":{"alias":"everything","tenant":{"alias":"newco"}}},
       "holder":{"slug":{"alias":"admin",     "tenant":{"alias":"newco"}}}}' \
  | roster binding add -

roster key add --tenant newco --holder admin --allow '/roster.*/*'
```

`everything` is a **pattern** and not a list. A list written the day a customer is
created is what existed that day; the next release adds an RPC their administrator
cannot call and cannot grant themselves either, because granting is refused for
anything the granter does not already hold. It is still an ordinary role -- unbind
it and it is gone, erase it and every binding to it goes too.

### It is four writes and not a transaction

There is no fifth RPC that does all of it and there should not be: each of the
four is held to the same rules every other write is, and a composite would be a
fifth thing to hold to them.

A failure part way leaves what came before it -- a tenant with nobody in it, or
somebody with no role. Both are finishable, because whoever is writing is outside
every tenant. That is the difference from the deadlock a *caller* would hit, where
writing the first role needs a binding only writing the first role could give.

An operator with no shell does the same four from the console's customers screen,
over `admin.addr`, as a session and through every rule. Nothing is only in one
path: [operating.md](../operating.md) § "The console" is why that works, and what
it costs.

## Where a customer's requests come from

If your product resolves a customer by hostname, tell roster the name:

```sh
roster host add '{"tenant":{"alias":"newco"},"name":"newco.example.com"}'
```

A front door then asks `FrontService.WhoseHost` before it knows anything else and
gets back a tenant identifier and nothing more.

**`Host` is the deployment's to write, not a customer's.** A hostname is unique
across the whole deployment, so a customer claiming one takes it from whoever owns
it and is told only that somebody has it; roster resolves no DNS -- it is meant to
run in an air gap -- and what makes traffic for a name arrive here is DNS and your
ingress, both yours. Nothing enforces this and nothing can: keep
`/roster.HostService/Add` off the roles a tenant's administrators hold.

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
