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

## Locally, in one command

```sh
docker compose up
# http://localhost:5173 — admin / admin
```

roster on Postgres, both planes, and the console on a vite dev server with hot
reload. Not the sandbox: this is the console talking to a roster that is really
there, which is where the differences from SQLite show up — and they have shown
up more than once.

The first operator comes from the environment, applied **once** by the image's
entrypoint and not by the CLI:

| | |
| --- | --- |
| `ROSTER_ROOT_USER` | `admin` |
| `ROSTER_ROOT_PASSWORD` | `admin` |

`roster init` takes no `--password` flag and will not grow one — an argument is
in the shell history and in the process list, which is why `roster key add` will
not take a key either. The entrypoint reads the variable and hands it over on a
pipe (`--password-stdin`), which is what `POSTGRES_PASSWORD` and
`KEYCLOAK_ADMIN_PASSWORD` do in their images.

Once, decided by a marker beside the databases — the same way Postgres looks for
`PG_VERSION` in its data directory. Running `init` twice is an error rather than
a no-op, deliberately, so the marker is what stops it rather than swallowing the
error.

**A password in an environment variable is visible** in `docker inspect`, in the
process environment and in the compose file. Postgres says the same about its
own and offers a `_FILE` variant for anything real; this image has none because
it is a development image. `ROSTER_ADMIN_*` is *not* the prefix — that one is
roster's own, for the admin listener.

## The console

```sh
npm --prefix ts install
npm --prefix ts run dev:sandbox    # no backend at all
npm --prefix ts run dev            # against a running roster
```

`dev:sandbox` compiles the whole server into the page — `GOOS=js GOARCH=wasm`,
SQLite in a Worker, a message port instead of HTTP/2. A reload is a fresh
deployment: two new databases, `roster init` run again, nothing left over. It
signs in as `ops` with the password `sandbox`.

The password is checked there by the same `vouch`, so a wrong one is refused —
but the **cookie** cannot work over a message port, and the server behind it is
`auth.Plain`, so nothing after the sign-in is checking a session. That is a
sandbox being a sandbox; see `wasm/main.go`.

### Where the console's sessions live

Signing in to the console mints an opaque cookie — 32 random bytes naming a row,
so signing out is a delete that takes effect at once.

**In a table**, on the control plane, which is where the operators who sign in
live. It used to be in memory, and payday's own store still says what that
means: right for one replica and *silently wrong* for two, because a cookie
minted on one is unknown to the other — intermittently, per request, with
nothing in any log saying why. It was lost on restart besides, so a deploy
signed everybody out.

Two things about the table worth knowing:

- **The cookie value is not in it.** What is stored is a digest, so a copy of
  the rows is not a set of live cookies. The lookup is the same one indexed
  read.
- **A session dies with the person.** The holder is an edge, so erasing an
  operator ends their sessions without anybody having to remember to.

Nothing collects expired rows yet: `authsession` checks both clocks when it
reads one, so an expired session is refused the moment it is presented, and what
is left is a table that grows. `keys.Sweep` and `vouch.Sweep` are the shape
that fixes it and this has not been given one.


An **operator** signs in: a holder of the control plane, which is where the
people who run this deployment live. `roster init` makes the first one and
prints their password once.

```
POST /session      {"alias": "ops", "password": "..."}   -> 204, __Host-pd_session
DELETE /session                                          -> 204
```

The cookie is opaque, `HttpOnly`, `SameSite=Lax` and names a session this
server keeps. It opens **two** listeners, and there are three in all:

| | | |
| --- | --- | --- |
| `server.addr` | product apps | walled and gated. Keys only — a cookie names nobody here |
| `control.addr` | operators | who runs this deployment, which services call it, their keys |
| `control.http.addr` | **the console** | the same, transcoded. This is what the UI talks to |
| `admin.addr` | operators | **customers**: the data plane, no wall, behind a session |
| `admin.http.addr` | a console | the same, transcoded. This is what a browser talks to |

```yaml
admin:
  addr: "127.0.0.1:50053"
  http:
    addr: "127.0.0.1:8081"
    allow_web: true
    origins: ["http://localhost:5173"]
```

A browser cannot speak gRPC, so a port without `http` is a port a console
cannot reach — and `server.http` is the wrong one: it fronts the **walled**
data plane, where an operator's session names nobody. Sign in there and there
is nothing to call.

`/session` is served on every listener that has HTTP, because a console
reaches one origin and signing in has to be there. Which listener the session
is a credential *for* is what differs.

Three because it can be nothing else. The product port is walled and an
operator has no tenant in that database, so it shows them nothing; and the
control port already registers `roster.HolderService` over its own rows, so the
customer-facing one cannot join it under the same name.

The rule the admin port runs on is one sentence:

> **Who is calling** and **what they hold** are control plane questions. What
> they are operating on is the data plane.

Found by running it: with that backwards, an operator creates a customer and a
holder and is then refused the role, because the check for what they may hand
on looks in the wrong database.

### Verifiers are not in either trail

`Credential.secret` and `ApiKey.secret` are declared `(payday.field).secret`,
which does two separate things. The layer clears them on the way out of the
walled stack — `vouch` and `roster key` read the same columns through an
unwalled one, deliberately, because comparing a verifier is their whole job.

And the **recorder** reads the declaration for itself, which is the half that
was missing: it sits behind every layer on purpose, so an argon2id hash was
being copied into `Audit.value` — in the one table nothing erases, readable by
anybody who may read the trail, in a deployment whose `CredentialService` is
unregistered precisely so that could not happen.

### What an operator'''s write leaves behind

Two rows, in two databases, joined by the trace.

```
control plane   ops called /roster.TenantService/Add        trace=a1b2…
data plane      TenantService/Add on 019ff4…  actor=<ops>   trace=a1b2…
```

The control plane's is written **first** and is about the decision, so an
attempt that then fails is still on record — which is the one an audit most
wants. They are not one transaction and cannot be: two databases, deliberately.

The data plane's row names an actor that resolves in neither database from
there, and that is what the trace is for. Do **not** infer it from the actor's
tenant failing to resolve: a hard-erased tenant looks exactly the same.

The trace is made by the admin port when there is none, rather than relying on
`otel:`. Observability is a thing a deployment may turn off; an audit that comes
apart when it does is not an audit.

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
| `/roster.VouchService/Delegate` | checking one **and** getting a credential to act for that person. A separate grant from `Verify`, on purpose: an app that only signs people in never needs it |
| `/roster.VouchService/Revoke` | ending one when somebody signs out |
| `/roster.FrontService/WhoseHost` | which tenant serves the name a browser arrived at |
| `/roster.FrontService/WhereFrom` | where the people at an address authenticate |
| `/roster.MeService/Get` | somebody's own record, through a delegation |
| `/roster.HolderService/Get` | who somebody still is — a name for a screen, and the periodic recheck that ends a session after somebody leaves |
| `/payday.TokenService/Introspect` | only if the app takes API tokens, or asks about a delegation it was given; see below |

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

### Three kinds of key

The prefix is which, and it is not decoration — it decides which database holds
the row, which table in it, and who the token is served as.

| | | |
| --- | --- | --- |
| `rk_` | the deployment's | `roster key add`. Resolves to the **key**, holds no tenant, sees every tenant there is |
| `rt_` | a tenant's | belongs to a holder. Resolves to that **holder**, so the wall, the bindings and the sites all apply exactly as when that person calls |
| `rd_` | a **delegation** | a product app calling as somebody it just signed in. Resolves to that holder in the same way — but it does **not** go in `authorization`: it rides in `roster-as`, beside the app's own key |

A `rt_` key is therefore never wider than the person it hangs off. Its `methods`
narrow that further and can never widen it — a method on the key that its holder
cannot call is still refused. The same is true of an `rd_`, which is the point
of it: an app drawing a person their own record calls with the person's reach
and not with its own.

Three things about `rd_` an operator should know.

**It is never a credential on its own.** A request carrying one carries two:
`authorization: Bearer rk_…` saying who is calling, and `roster-as: rd_…` saying
who the call is about. Presented alone it authenticates nobody, which is what
makes it safe to hand around inside an app and what makes "bound to the caller
it was issued to" a rule rather than a sentence — the binding needs both halves
on the request to be checkable at all.

**It must carry an expiry.** An absent one is refused rather than read as
forever, which is the opposite of how `rk_` and `rt_` read that column.

**Rotating an app's key invalidates the delegations it issued**, because the
issuer is the key row. PLAN.md D25 says why that was the answer taken.

What a tenant key costs is the trail: its writes are recorded as the person's,
so `Audit` says who and not which of their keys. Revoking still works, since the
row is what the token resolves through.

Nothing mints a `rt_` key over the wire yet — `ApiKeyService` is unregistered,
so it takes `Ungated`, which means a shell. The console is what changes that,
and the rules that make it safe are in place: see below.

Nor a delegation, and for a different reason: what would mint one is
`VouchService.Verify` answering with it, and the page that decides how long it
should live has not been written. PLAN.md D24 is why that order, D25 is the
shape.

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

### A name a front door answers at

A tenant is the same service under a different operator's own domain, so the
name a browser arrived at *is* the operator whose service they are signing in
to. That used to be a map in every app's configuration; it is a row now.

```
HostService/Add        acme.example.com -> acme
MailDomainService/Add  acme.com -> entra          (optional; where they authenticate)
```

A front door asks `FrontService/WhoseHost` before it knows anything, and gets a
tenant identifier and nothing else. `FrontService/WhereFrom` is the other half —
identifier-first sign-in, answered per **domain** and never per person, because
per person it would say whether an account is here.

Three things to know:

- **A host is stored as it is compared**, lowercased and without a port, and one
  that is not is refused rather than fixed. Fixing it quietly hands back a row
  that differs from what was typed. What goes wrong without the rule is nothing,
  for a long time — the row is written, a console lists it, and the only thing
  that never happens is a match.
- **A host is unique across the deployment.** Two operators cannot both own one
  name, so the second is told it is taken by somebody they cannot see. A
  hostname is a public fact, which is why that is the cheap side of the trade.
- **A mail domain is unique within a tenant**, and two operators saying
  something about `@gmail.com` are two facts.

And which provider one operator's people arrive through:

```
ConnectionService/Add  entra -> https://login.microsoftonline.com/acme/v2.0
                       client_id, scopes, secret_ref: "env:ACME_ENTRA_SECRET"
```

**The secret is not here.** roster stores a reference and does not read it —
what `env:` means is the front door's to know. Everything that varies per tenant
is public, and the secret has to reach the front door whatever roster does,
because using it means doing the OIDC exchange, which is being the relying party.

So a front door walks: a hostname → a tenant → an address → a provider → a
connection, and then resolves one string in whatever way this deployment already
resolves secrets.

With a host, an address names one person again — so `VouchService` takes one:

```
Vouch.Verify {who: {tenant: "acme", address: "erin@acme.example"}, secret: …}
```

Always the tenant **and** the address. There is no form that takes an address
alone, because a lookup that could be made without naming a tenant is one a
front door that forgot to think about which one compiles a wrong answer for.

### The screen for it

The console's **customers** tab: a tenant, the people in it, and one person's
ways in — `HolderService/SignsIn`, which is the read that exists for this
screen. Beside them the four acts below, each drawn only when the operator holds
the method that makes it.

It reaches `admin.http`, which is where the deployment operates on customers,
and `VouchService` is served there for the reason this section exists: an air
gap has an operator instead of a mail server. `cmd/admin.go` says what that
costs — the rule about writing somebody's credential is **not** applied on that
port, because it reads bindings the caller has none of, and an operator's
standing there comes from the port rather than from a role.

A new password appears **once**, in the page, to be read out. There is no field
to type one into: a secret the caller chose is a secret the caller knows.

### A password somebody has already lost

```yaml
vouch:
  breached: /var/lib/roster/leaked.txt
```

SHA-1, uppercase hex, one per line, **sorted** — the format the well-known
corpus is published in, and `sort -u` is enough to make one. A file rather than
a service because the deployment this is most careful about has no network at
all; the lookup halves the file rather than loading it, so the size of the
corpus costs nothing but disk.

Named and it is a **refusal**: `Vouch.Set` and `Vouch.Reset` answer
`FailedPrecondition` and the person picks again. Unnamed and nothing is checked,
which is every deployment that has not said otherwise.

Two things it is not. It is not a length or complexity rule — those are policy
and stay with whoever collects the password. And it is not advisory: a check
whose result is advice is a check nobody acts on.

The order is verified once at startup rather than trusted. A file that is not
sorted answers *no* to things that are in it, which is the quiet direction in
the one feature whose whole job is to say yes.

### What a local operator does

For a deployment with no mail, where the person who delivers a recovery code is
a person.

| | |
| --- | --- |
| `/roster.VouchService/Reset` | a new password, generated here and answered with **once**. The operator reads it out |
| `/roster.VouchService/Unlock` | opens an account ten wrong answers closed, without changing the secret |
| `/roster.VouchService/Set` | writes a password somebody chose — an account portal's, not an operator's |

**You may only write the credential of somebody whose permissions are a subset
of yours.** Resetting a password is a way to become somebody, so without that
rule an operator who may reset anybody in their tenant holds every permission in
it. The refusal names the method that was in the way.

Changing your own is always allowed. And nothing here stops you **suspending**
an administrator — that is a denial of service rather than an escalation, and it
is not covered.

### Suspending somebody, and signing them out of everything

Two facts an operator writes about a person, and three methods because a role is
a list of methods -- what you can grant is exactly what you can name.

| | |
| --- | --- |
| `/roster.HolderService/Disable` | they are not to sign in, and their rows stay. A session, a tenant key and a delegation they already held all stop working |
| `/roster.HolderService/Enable` | the other direction, and a separate grant on purpose |
| `/roster.HolderService/Invalidate` | everything issued **before now** is void. There is no undo and no time to give: the server stamps it |

Neither is a lockout, which is temporary and automatic and belongs to a
password, and neither is `Erase`, which is deletion.

Three things to know before handing these out:

- **`Invalidate` does not touch an API key.** A key is named, listed and revoked
  one at a time; killing somebody's scripts silently under "sign out everywhere"
  is an outage with nothing saying why. Use `roster key revoke`, or erase the
  row.
- **They do not require a version.** Every other write here is a
  compare-and-swap; these take one if you have read the row and proceed without
  one if you have not, because a suspension that fails when somebody edits a
  profile is a suspension that editing a profile in a loop can prevent.
- **Nothing stops you suspending an administrator.** Escalation prevention
  covers roles, bindings and an API key's methods, and not this. PLAN.md's list,
  item 11.

What an app in front does with `date_invalidated` is its own half: roster
answers *invalid since when*, and the app answers *what is still alive*. It
arrives on `HolderService/Watch` like anything else about a person.

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

- **Nothing mints a `rt_` key over the wire.** `ApiKeyService` is unregistered,
  so issuing one takes `Ungated` and therefore a shell. The rules that make a
  customer-minted key safe are in place — the prefix, the holder it resolves to,
  and `mayGrant` on `methods` — and what is missing is the surface that would
  use them.
- **A reference app that uses a delegation.** `VouchService.Delegate` mints one
  and `Revoke` ends it, but nothing in `examples/sso` calls either yet: it signs
  people in with OIDC and never calls `Vouch` at all. PLAN.md D24 and
  `docs/ROADMAP.md` P3.
- **`Binding` cannot be re-pointed.** Its edges are immutable, so changing who
  holds what is a delete and an add. That is the safe direction and it is worth
  knowing before writing a console screen that looks like an edit.
- **No second factor.** It is roster's to hold and to check when it exists — see
  PLAN.md D20 and D21 — and today there is only a password. Deciding *when* to
  demand one is not roster's either way; that belongs wherever the browser is.
  What will be roster's alongside it is the `continuation`: an opaque handle
  carrying "this person satisfied the first factor" between the two calls, so an
  app serving two forms holds nothing but a string.
- **No magic link.** Inside the line and unwritten, and the thing in the way is
  F7: an address does not resolve to one person by design, so the usual front
  door for a link has nothing to look anybody up with.
- **Nothing collects expired sessions.** They are refused on read, so this is
  a table that grows rather than a hole. See above.
- **Nothing here signs a token.** If several products need one sign-in, that is
  Hydra in front and roster answering it — LOGIN.md, "What changes when Hydra is
  in front". Do not reach for a JWT minted here; PLAN.md D19 is why.
