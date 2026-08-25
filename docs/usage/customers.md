# Customers

A **tenant** is a customer. A **holder** is somebody in one. Everything else in
roster hangs off those two.

## How anything is named

Every entity command takes the same shape:

```
roster <entity> <verb> [NAME] [REQ...] [options]
```

- `NAME` is a **reference**: an identifier, `@tenant`, or `@tenant/alias`.
- `REQ` is the rest of the request as JSON, merged over it. `-` reads stdin.
- **Flags come before arguments.** `roster tenant ls -o json`, not `... ls json -o`.

```sh
roster tenant ls -o table
roster holder get @newco/alice
roster holder ls -o wide
```

`-o` is `pretty` (the default), `json`, `protojson`, `prototext`, `name`,
`table`, `wide`, or `template=...`. `-o name` prints the identifier alone, which
is what a script wants.

An **alias** is unique within a tenant and among the living. Two tenants may
both have an `admin`; a tenant may reuse an alias after the person holding it is
erased.

### References in JSON

A reference is the oneof it is declared as, so the outer key says *which way of
naming* and the inner one is the name:

```json
{"tenant": {"alias": "newco"}}
{"holder": {"slug": {"alias": "alice", "tenant": {"alias": "newco"}}}}
{"role":   {"slug": {"alias": "everything", "tenant": {"alias": "newco"}}}}
{"team":   {"slug": {"alias": "eu-ops", "site": {"slug": {"alias": "eu", "…": ""}}}}}
{"holder": {"id": "01a03322-4034-842a-8802-990533c39e6a"}}
```

`Tenant` is the exception and carries the string directly, because a tenant's
alias is unique on its own — there is no parent to name it within. Everything
else is `{"slug": {"alias": …, "<parent>": …}}`, and the parent is whatever the
alias is unique inside: a tenant for most, a **site** for a team, a **holder**
for a key.

`roster <entity> add --help` prints the shape when you cannot remember it.

## A tenant

```sh
roster tenant add @newco
roster tenant add @newco '{"name": "Newco Ltd"}'
```

`alias` is what people type; `name` is what they read. Neither is an
identifier — that is minted, and it is what every other row points at.

### Giving it an identifier

```sh
roster tenant add @newco '{"id": "019ff2ab-…"}'
```

Almost never, and once in a way that matters: an app served by this roster
anchors its own rows on the identifier a credential carries, and when that app
also has the tenant written down as a constant the two must agree **from the
start**.

Getting it wrong is not an error. Both sides come up, somebody signs in, and the
app makes a *second* tenant because the identifier it was handed is not one it
has — two rows for one organisation, the rows that belong together split across
them, nothing failing. It has to be a tenant-domain identifier and `Tenant.Add`
refuses anything else.

## A person

```sh
roster holder add @newco/alice
roster holder add @newco/alice '{"name": "Alice Nguyen"}'
```

That row is not yet somebody who can do anything, or somebody who can sign in.
Those are two separate things and they are the next two pages:

- what they may **do** — a role and a binding — is [permissions.md](permissions.md)
- how they **prove who they are** is [ways-in.md](ways-in.md)

Neither is written for you, deliberately. A command that created a person with a
password would put a secret on the screen before there was anybody to give it
to, and one that created them with permissions would decide something only you
know.

## Standing a customer up

The four writes, in order. This is what the console's *new customer* form does,
and what `roster init` used to do for one fake customer:

```sh
roster tenant add @newco
roster holder add @newco/admin
roster role   add @newco/everything '{"methods":["/roster.*/*"]}'

echo '{"role":  {"slug":{"alias":"everything","tenant":{"alias":"newco"}}},
       "holder":{"slug": {"alias":"admin",     "tenant":{"alias":"newco"}}}}' \
  | roster binding add -
```

Then a way in, which is [ways-in.md](ways-in.md):

```sh
roster key add --tenant newco --holder admin --allow '/roster.*/*'
```

`everything` is a **pattern** and not a list. A list written the day a customer
is created is what existed that day; the next release adds an RPC their
administrator cannot call and cannot grant themselves either, because granting
is refused for anything the granter does not already hold.

It is still an ordinary role: unbind it and it is gone, erase it and every
binding to it goes too.

### It is four writes and not a transaction

There is no fifth RPC that does all of this and there should not be — each of
the four is held to the same rules every other write is, and a composite would
be a fifth thing to hold to them.

A failure part way leaves what came before it: a tenant with nobody in it, or
somebody with no role. Both are finishable, because whoever is writing is
outside every tenant. That is the difference from the deadlock a *caller* would
hit, where writing the first role needs a binding only writing the first role
could give.

## Where a customer's requests come from

If your product resolves a customer by hostname, tell roster the name:

```sh
roster host add '{"tenant":{"alias":"newco"},"name":"newco.example.com"}'
```

A front door then asks `FrontService.WhoseHost` before it knows anything else,
and gets back a tenant identifier and nothing more. `Host` is the deployment's
to write — roster resolves no DNS, and the wall does not narrow uniqueness — so
it is not a row to put on a customer's role.

A `MailDomain` is the other half and answers a different question: which tenant
an **address** belongs to. It claims nothing and proves nothing; roster does not
do domain-ownership verification, and a deployment that needs it does the DNS
check itself and writes the row afterwards.

## Removing somebody

```sh
roster holder erase @newco/alice     # unreachable, and destroys nothing
roster restore @newco/alice          # while the grace lasts
roster forget @newco/alice           # destroys, including the trail's copy
```

`erase` is *this person has left*: two columns written, the alias and addresses
and identities and the whole trail still there. `forget` is *destroy what you
hold about them*, which is a different request with a legal clock on it.
`holder.forget_after` in the configuration is the window between them.

See [`../operating.md`](../operating.md) § destroying somebody.

## Next

[ways-in.md](ways-in.md) — how somebody proves who they are.
