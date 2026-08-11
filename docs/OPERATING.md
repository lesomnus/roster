# Running roster

Task-shaped: what to type, in the order it has to happen. What each part *is*
lives in [POSITION.md](POSITION.md) and the package comments.

## The two planes

roster runs twice in one process, on two databases.

```yaml
db:
  driver: sqlite3
  dsn: "file:roster.db?_pragma=foreign_keys(1)"

# Who may call this deployment. Roster again: one tenant for you, a holder per
# service, and the keys under those.
control:
  db:
    driver: sqlite3
    dsn: "file:roster-control.db?_pragma=foreign_keys(1)"
```

```yaml
control:
  db: {...}

  # Where a console reaches it, and **empty is nowhere**. The rows are always
  # reachable in this process -- that is what the auth interceptor asks on
  # every request -- and this is what puts them on a port.
  #
  # Bind it somewhere only a console can reach. It serves `ApiKeyService`,
  # which every other port refuses, and a port that is not open is a control
  # nothing has to get right.
  addr: "127.0.0.1:50052"
```

**Leave `control` out and the deployment believes its callers** — `auth.Plain`,
which says so once in the log. That is right for a checkout and is not something
to serve anywhere else.

A key must not live in the tables it protects, which is why the second database
is a database and not a reserved tenant. There is no query from one to the
other.

## Your first tenant and person

```sh
roster init --tenant acme --holder admin
```

A tenant is not put up from inside one, so the first row cannot arrive over the
API. `init` writes it through the server instance the process holds.

It also writes the first **role and binding**, and that is not a convenience:
permissions are deny-by-default, so a tenant and a holder alone are somebody who
can call one method — the one that tells them they hold nothing. There is no way
out of that from the API, because writing the first role needs a binding only
writing the first role could give.

The role is `everything`, and what it holds is a **pattern**:

```
holder admin is 019ff2...
  bound to role "everything" = /roster.*/* -- every RPC roster serves, now and after an upgrade
```

A list written at `init` is a snapshot. The next release adds an RPC the first
administrator cannot call and cannot grant themselves either, because granting
is refused for anything the granter does not already hold — so a snapshot is
repaired by a shell on the box, once per upgrade, forever.

It is still an ordinary row: unbind it and it is gone, erase it and every
binding to it goes too. And it is not something anybody can hand out — see
below.

### And an operator, if there is a control plane

```
control plane
  holder ops is 019ff2...
  bound to role "everything"
  password  kQ9x...
```

Shown once and stored as an argon2id hash. There is no `--password` flag on
purpose: a secret on a command line is in the shell history and the process
list, which is the same reason `roster key add` will not take a key.

That is the console's bootstrap. A console cannot be what creates the first
person allowed to use it.

## The console

An **operator** signs in: a holder of the control plane, which is where the
people who run this deployment live. `roster init` makes the first one and
prints their password once.

```
POST /session      {"alias": "ops", "password": "..."}   -> 204, __Host-pd_session
DELETE /session                                          -> 204
```

The cookie is opaque, `HttpOnly`, `SameSite=Lax` and names a session this
server keeps. What it opens is the **control plane's** listener — `control.addr`
above — and nothing else:

| | |
| --- | --- |
| control listener | who runs this deployment, which services call it, their keys |
| data plane listener | customers and their people. Keys only; a cookie names nobody here |

That is not a restriction somebody chose. A session names a control plane
holder and the two planes are separate databases with no query between them, so
the row simply is not there — which is also the answer to why an operator has
no standing inside a customer's tenant. They administer the deployment; a
customer's people are the customer's.

Sessions are held **in this process**, which is right for one replica and
silently wrong for two: a browser is signed in or out depending on which
replica the load balancer picked, per request, with nothing in any log saying
so. Same trap as the memory broker.

## A key for a service

```sh
roster key add --service custody \
  --allow /roster.VouchService/Verify,/roster.HolderService/Watch
```

It prints the key **once**, to stdout, and stores a hash. This deployment cannot
tell anybody what their key was any more than it can tell them their password.

Naming a service creates it: the owner's tenant and the holder are made on the
way, because a service is not something you set up on purpose before you need
it.

`--allow` is refused when empty rather than defaulted in either direction.
Everything hands out more than you asked for; nothing mints a key that silently
does not work.

```sh
roster key list                 # what exists, what each may call, last used
roster key revoke --id <id>     # a delete, so the next call carrying it fails
```

### What a product app needs allowed

| | |
| --- | --- |
| `/roster.VouchService/Verify` | checking a password |
| `/roster.HolderService/Get` | who somebody still is — a name for a screen, and the periodic recheck that ends a session after somebody leaves |
| `/payday.TokenService/Introspect` | only if the app takes API tokens; see below |

Not `VouchService/Set` — changing a password belongs to whatever account portal
owns the person — and no `Holder` writes, since a product does not own the
people it serves.

## Tokens a product app was handed

A session cookie is between a browser and the app that set it. A token somebody
pastes into a script is not: the app that receives it has to find out what it
means, and the string means something only here.

`payday.TokenService/Introspect` is that question. The app asks with **its own**
key, and roster answers with who the token stands for and what it was narrowed
to:

```go
h := auth.Bearer(auth.Remote(pdpb.NewTokenServiceClient(conn)))
```

Three things about it are worth knowing before allowing it:

- **It is not public.** The bearer's token is the subject of the question; the
  caller is the product app. Allowing this on an app's key is the whole of the
  trust decision, and it is per app.
- **It answers about the holder, not the key.** The app in front resolves what it
  is told against its own rows, and those are about people.
- **It sees only this plane's keys.** Control-plane keys — the ones `roster key
  add` mints — live in the other database, and there is no query from one to the
  other. They are not refused here; they are invisible.

### Two kinds of key

The prefix is which, and it is not decoration — it decides which database holds
the row and who the token is served as.

| | | |
| --- | --- | --- |
| `rk_` | the deployment's | `roster key add`. Resolves to the **key**, holds no tenant, sees every tenant there is |
| `rt_` | a tenant's | belongs to a holder. Resolves to that **holder**, so the wall, the bindings and the sites all apply exactly as when that person calls |

A `rt_` key is therefore never wider than the person it hangs off. Its `methods`
narrow that further and can never widen it — a method on the key that its holder
cannot call is still refused.

What a tenant key costs is the trail: its writes are recorded as the person's,
so `Audit` says who and not which of their keys. Revoking still works, since the
row is what the token resolves through.

Nothing mints a `rt_` key over the wire yet — `ApiKeyService` is unregistered,
so it takes `Ungated`, which means a shell. The console is what changes that,
and the rules that make it safe are in place: see below.

## Who may do what

**Nobody, until you say so.** A caller with no binding may call nothing, because
the alternative is that adding the first role silently takes away what everybody
had.

A role is a list of RPCs, each a whole name or a pattern:

```
Role     alias=operator  methods=[/roster.HolderService/Get, …]  site?
Binding  role  holder|group  site?
```

| | |
| --- | --- |
| `/roster.HolderService/Get` | one method |
| `/roster.HolderService/*` | one service |
| `/roster.*/List` | that method wherever it appears |
| `/roster.*/*` | everything roster serves |
| `/*.*/*` | that and payday's own besides — `BatchService`, `TokenService` |

A whole part or nothing: `*Get*` is not a pattern, it is a method name that
happens to contain asterisks. See `frame.Covers`.

A pattern rather than a list because a list is a snapshot. Write out every
method of a service today and the next release adds one the role does not
allow — silently, to a role whose name says it covers that service.

Scope is where the reference sits:

| where the role is referenced | what it covers |
| --- | --- |
| a `Binding` with no site | the whole tenant |
| a `Binding` with a site | that site |
| a `TeamMembership` | that team |

A `Group` is a set of people and grants nothing by itself — a binding to a group
reaches everybody in it, so the rule is written once and the membership changes.

### You cannot hand out what you do not hold

Writing a role, patching one, binding one, and putting methods on an API key are
all refused when they name a method you do not hold **through a binding**. A
role you hold in one team is not yours to bind across the tenant.

**What you hold, where you hold it.** A binding made in a site is a permission
held there, so it may be handed on **in that site** and nowhere wider. A site
administrator delegates inside their own site and cannot reach past it.

A role held in a *team* is left out of this entirely: its scope is a team and
the scopes here are the tenant and a site, so there is nothing to compare. What
asks about a team is the per-call check in `server/core`.

**And a role that belongs to a site is bound only there**, whoever is asking.
That is the schema's own rule and it holds for somebody who legitimately holds
the whole tenant, because it is about the role rather than about the asker.
A role of no site is this schema's `ClusterRole` and is bindable anywhere in
its tenant: narrowing is free, widening is what needs permission.

A pattern is covered by **one** pattern you hold, never by several together.
Holding `/roster.HolderService/*` and `/roster.TeamService/*` does not let you
grant `/roster.*/*`, even in a deployment where those are the only two services
— the third one added next release would be covered by a grant made before it
existed.

So the first binding is `init`'s, and everything else descends from it.

### What a page shows

```
POST /roster.MeService/Get   {}
```

Takes nothing, answers about the caller: their identifiers, addresses, teams,
and every method they may call. That list is the union the server enforces, so
what a page shows and what it is allowed to do cannot drift.

It needs **no role** — requiring one to learn that you hold none is a deployment
where a new account cannot be told what it is for.

## Signing somebody in

See [LOGIN.md](LOGIN.md) for the whole path. In short: a product app calls
`Vouch.Verify` with its key, gets yes and two identifiers, and sets its own
session cookie. roster never talks to a browser.

## Talking to it over TLS

A key travels on **every call**, so a cleartext connection between two machines
has given it away. On the client side that is payday's `DialConfig`:

```yaml
roster:
  addr: roster:50051
  token: ${CUSTODY_ROSTER_TOKEN}
  tls:
    ca_file: /etc/ssl/private-ca.pem
```

Nothing written down is plaintext, and it warns once.

## What is not here

- **No admin console.** Keys and roles are the CLI's, which means a shell on the
  box. A console would itself need a key, and the first key has to come from
  somewhere that is not one.
- **Nothing mints a `rt_` key over the wire.** `ApiKeyService` is unregistered,
  so issuing one takes `Ungated` and therefore a shell. The rules that make a
  customer-minted key safe are in place — the prefix, the holder it resolves to,
  and `mayGrant` on `methods` — and what is missing is the surface that would
  use them.
- **`Binding` cannot be re-pointed.** Its edges are immutable, so changing who
  holds what is a delete and an add. That is the safe direction and it is worth
  knowing before writing a console screen that looks like an edit.
