# Running roster

Task-shaped: what to type, in the order it has to happen. What each part *is*
lives in [position.md](position.md) and the package comments.

If you are meeting roster for the first time, **[usage/](usage/)** is the
gentler path — one page per topic, and a [tutorial](usage/tutorial.md) that
takes an empty directory to a person signing in. This page is the same ground
for somebody who has to run the thing: every port, the console, the audit trail,
retention, and what goes wrong.

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

`roster init` will not make that deployment at all — it refuses a configuration
with no `control.db`, and the reason is what happens to the ones that add one
later. `ApiKey.Issue` works perfectly under `Plain`: a name is written, a
frame is built, an `ApiKey` row lands on the data plane, and nothing reads it
because `auth.Bearer` is not in the chain. An expiry is optional, so the row
stays. Name a control plane afterwards and every key minted while nobody was
checking becomes a working credential at once, issued by nobody. That is not a
migration.

`cmd.Seed` is not asked the same question, which is the line: a deployment
raised by a Go call is a test or the Wasm sandbox, and `Plain` is what those are
for.

Leave the **database** out and write the rest of the block, and you get that
same deployment with a control plane written down beside it. What decides is
`control.db.driver` and nothing else — an address is only how the plane is
reached — so `control.addr` on its own built a server that reads no `rk_` and
no `rt_`, honours no `rd_`, mints no session and opens no port, while the only
line in the log about any of it was the `Plain` warning a file with no
`control:` at all prints too. It is refused at startup now, naming the field.

A key must not live in the tables it protects, which is why the second database
is a database and not a reserved tenant. There is no query from one to the
other.

## Your operator

```sh
roster init
```

Both planes, or it refuses: see above for why a control plane is not a thing to
add later.

What it writes is one person — the **operator** who runs this deployment, in the
control plane — and the role that lets them. Nothing in the data plane at all:

```
control plane
  holder ops is 019ff2...
  bound to role "everything" = /roster.*/* -- every RPC roster serves, now and after an upgrade
  password  kQ9x...

sign in to the console as ops. that password is shown once and is not stored -- write it down now.

there are no customers yet, which is the right state to start in.
```

That row cannot arrive over the API. A tenant is not put up from inside one, so
the first row of a deployment has nowhere else to come from; `init` writes it
through the server instance the process holds.

It also writes the **role and binding**, and that is not a convenience:
permissions are deny-by-default, so a tenant and a holder alone are somebody who
can call one method — the one that tells them they hold nothing. There is no way
out of that from the API, because writing the first role needs a binding only
writing the first role could give.

The role is `everything`, and what it holds is a **pattern**. A list written at
`init` is a snapshot: the next release adds an RPC the first operator cannot
call and cannot grant themselves either, because granting is refused for
anything the granter does not already hold — so a snapshot is repaired by a
shell on the box, once per upgrade, forever.

It is still an ordinary row: unbind it and it is gone, erase it and every
binding to it goes too. And it is not something anybody can hand out — see
below.

### About that password

Shown once and stored as an argon2id hash. There is no `--password` flag on
purpose: a secret on a command line is in the shell history and the process
list, which is the same reason `roster key add` will not take a key.

That is the console's bootstrap. A console cannot be what creates the first
person allowed to use it.

## Your first customer

`init` seeds no customer, so this is the first thing to do — and it is the same
thing you will do for the hundredth. Four writes and then a way in.

```sh
roster tenant add @newco
roster holder add @newco/admin
roster role   add @newco/everything '{"methods":["/roster.*/*"]}'

# a binding has no alias of its own, so the whole request goes in
echo '{"role":  {"slug":{"alias":"everything","tenant":{"alias":"newco"}}},
       "holder":{"slug": {"alias":"admin",     "tenant":{"alias":"newco"}}}}' \
  | roster binding add -
```

Every entity command takes `[NAME] [REQ...]`: `NAME` is a reference — an
identifier, or `@tenant` / `@tenant/alias` — and `REQ` is the rest of the
request as JSON, merged over it. `-` reads stdin. Flags come before arguments,
and `-o name` prints the identifier alone, which is what a script wants.

A reference in JSON is the oneof it is declared as: `{"id":…}` or
`{"slug":{"alias":…,"tenant":…}}`. The outer key says *which way of naming* and
the inner one is the name. `roster <entity> add --help` prints the shape.

They read the database directly and say so on stderr, which is what a shell on
the box is: no wall, no gate, no rules. That is the same reason the first role
can be written at all.

### And a way in, which nothing writes for you

Creating somebody writes no credential, deliberately. Give them one:

```sh
roster key add --tenant newco --holder admin --allow '/roster.*/*'
```

That prints an `rt_` to stdout and one line to stderr, so `$(roster key add …)`
is the key and nothing else. It is shown **once** — what is stored is a hash.

The prefix is a fact about which plane the key belongs to and is never something
you name: `--service` is the deployment's own kind and `--tenant`/`--holder` is
a customer's, and giving both is refused. Naming a service creates it, because
the control plane has one tenant and a service is not something anybody sets up
before they need it; naming a customer's person does **not**, because a
customer's people are the customer's and a typo would otherwise write rows into
somebody else's tenant.

Then check it, which is the whole loop closed:

```sh
curl -sS -X POST http://127.0.0.1:8080/roster.MeService/Get \
  -H 'content-type: application/json' -H 'connect-protocol-version: 1' \
  -H "authorization: Bearer ${KEY}" -d '{}'
```

A key resolves to its **holder**, so what comes back is that person — their
tenant, and `/roster.*/*` as the pattern rather than what it expands to.

For somebody at a browser rather than something that calls, the way in is a
password:

```sh
roster vouch reset @newco/admin              # generated here, printed once
printf '%s' "${PASSWORD}" \
  | roster vouch set --password-stdin @newco/admin   # one they chose
roster vouch unlock @newco/admin             # wrong answers closed it
```

`reset` generates, because a secret the caller chose is a secret the caller
knows. `set` never takes the password as an argument — an argument is in the
shell history and in the process list, which is the same rule `roster init` and
`roster key add` are on. Neither can tell anybody what a password *was*: what is
stored is an argon2id hash.

`vouch.breached` applies to all three. A deployment that named a corpus has said
it will not hold a password somebody has already lost, and that is a fact about
the secret rather than about the door it came through.

### If an app already knows this organisation

`TenantAddRequest` takes an `id`, and there is a case where it must be given
one:

```sh
roster tenant add @newco '{"id":"019ff2ab-…"}'
```

An app served by this roster anchors its own rows on the identifier a credential
carries; when that app also has the tenant written down as a constant, the two
have to agree from the start.

**What happens without it is not an error**, which is why it is worth saying
here. Both sides come up, somebody signs in, and the app makes a *second* tenant
for them because the identifier it was handed is not one it has: two rows for
one organisation, and the rows that belong together split across them, with
nothing failing. It has to be a tenant-domain identifier and `Tenant.Add`
refuses anything else, so the check is payday's rather than one written here.

### The same thing from a console

An operator with no shell does it from the customers screen, which has a form
above the list: the same four writes in the same order, then a password or a key
on the person's own panel. `admin.addr` is the port, and it needs a control
plane because the session it reads names a holder of that plane.

Nothing is only there and nothing is only here. Both reach the same services
over the same rows, so which one an operator uses is about whether they have a
shell rather than about what they are allowed to do.

That path is not the local one with a UI on top. These commands go through
`Ungated`; the console goes over a port, as a session, through every rule — and
it works because `mayGrant` compares methods and **site** rather than tenants,
so the operator's binding in the control plane reaches a tenant that did not
exist a moment ago.

There is no fifth RPC that does all four, in either path, and there should not
be: each is held to the same rules every other write is, and a composite would
be a fifth thing to hold to them. It is not a transaction either, so a failure
part way leaves what came before it — a tenant with nobody in it, or somebody
with no role. Both are finishable, because whoever is writing is outside every
tenant.

`cmd/newcustomer_test.go` is the console's sequence end to end and
`cmd/customerkey_test.go` is this one.

## Locally, in one command

```sh
docker compose up --build
# http://localhost:8082/console/   admin / admin
# http://localhost:8090/           erin / correct horse battery staple
```

roster on Postgres, both planes, the console served by roster under
`/console/`, one customer already stood up (`contoso`, with `erin` in it), and
the account app fronting them on its own port — the way it is deployed, with a
key the `customer` service minted once. Not the sandbox: this is the pages
talking to a roster that is really there, which is where the differences from
SQLite show up — and they have shown up more than once. `ts/e2e/` runs against
it unchanged:

```sh
E2E_CONSOLE=http://localhost:8082/console/ E2E_ACCOUNT=http://localhost:8090 \
E2E_OPS_USER=admin E2E_OPS_PASSWORD=admin npx --prefix ts playwright test console account
```

For hot reload on a page, point a dev server at it instead -- the stack
already names `http://localhost:5173` as an origin both listeners accept:

```sh
VITE_ADDR=http://localhost:8082 VITE_ADMIN_ADDR=http://localhost:8081 npm --prefix ts run dev
npm --prefix ts run dev:account       # proxies to the account app on :8090
```

Open it as `localhost`. The console's cookie is `__Host-` and `Secure`, which a
browser accepts over plain http from `localhost` and from nowhere else -- the
right answer for a deployment, which has TLS, and the reason `PUBLIC_HOST` in
`compose.yaml` is for the account app rather than a way to reach the console
by another name.

| | |
| --- | --- |
| `8080` | the data plane over HTTP (`server.http`); `50051` is the same over gRPC |
| `8081` | the admin listener, which the console reaches from the browser |
| `8082` | the control plane, and the console under `/console/` |
| `8090` | the account app |

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
deployment: new databases, seeded again by `cmd.Seed`, nothing left over. It
signs in as `ops` with the password `sandbox`, and has one customer, `contoso`.

It is **two instances**, because one instance answers one message port and the
console reaches two listeners: `app.wasm` is `control.http` — the deployment
screen, the sign-in — and `admin.wasm` is `admin.http`, which the customers
screen opens beside it. Each has databases of its own, and `wasm/admin/main.go`
says why the page cannot tell.

The password is checked there by the same `vouch`, so a wrong one is refused —
but the **cookie** cannot work over a message port. So the instance remembers
who signed in and takes every later call to be them until a sign-out
(`wasm/sandbox`); the admin instance, which saw no sign-in, is `ops` from the
start. That is a sandbox being a sandbox; see `wasm/main.go`.

`ts/e2e/sandbox.spec.ts` opens it in a browser and `wasm/sandbox` has the same
sign-in without one, because it stopped working once with every other gate
green -- five things had drifted, from the page's base path to the name of an
in-memory database -- and nothing noticed until somebody ran it.

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

Expired rows are collected hourly, by `session.Sweep` — `keys.Sweep`'s shape,
which is what this paragraph asked for when it said nothing did. It deletes on
`date_expires` alone and on nothing else: a signed-out session is deleted where
it is signed out, and a row whose person is gone goes with them through the
edge. Nothing depends on the sweep for correctness — `authsession` checks both
clocks when it reads one, so an expired session is refused the moment it is
presented — so the sweep is about the size of the table and not about who may
sign in.


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

`admin` needs a control plane and is refused without one: the port takes a
session cookie and resolves it against **that** database's holders, so with no
control plane there is nobody to be. It used to open no listener and say
nothing.

`control.addr` takes **both**: a console's cookie and a service's `rk_`. Not
that a service introspects its callers' tokens here — those are `rt_`s, this
plane cannot see one, and that question goes to `server.addr` (§ tokens a
product app was handed) — but the port a deployment's own services are meant
to call has to authenticate their keys at all. It did not until 2026-08-28,
and the way it failed is worth recognising if you meet it on an older build:
every key was refused with

```
rpc error: code = Unauthenticated desc = who is asking?
```

from the gate, with no frame and nothing naming a key. The resolver behind the
port was reading its key rows from *its own* control plane, which is nil —
that nil is what stops the recursion — so the plane the keys are in answered
"this deployment has no keys" about all of them. Nothing caught it because
nothing dialled the port with a key; the tests and the `admin` port beside it
only ever used a cookie.

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

Sessions are held **in a table** — see "Where the console's sessions live"
above — so a cookie minted by one replica resolves on another. This paragraph
used to say the opposite, and said it below a section that already said the
right thing.

The **watch broker** is the same trap one seam over, and it is the one setting
that has to be named rather than left: with `broker: memory` the console's live
screens run on `Watch` per process, so a screen watching one replica never hears
about a write that landed on another, and nothing reports it — the stream stays
open and looks healthy. `broker: postgres` is what a second replica needs, on
both planes. See "Running more than one" below.

### How long the trail is kept

**Forever, until you say otherwise.** That is the default and it is deliberate:
a version upgrade is not the right thing to decide how long a deployment's
evidence lasts.

It is the one table that never stops growing. Every write is a row in it, and
unlike a session or a sign-in attempt there is nothing stale to collect — a
trail row is not expired, it is old.

The mechanism is **payday's** (`trail`, and `config.AuditConfig`), because the
`Audit` entity is payday's and every app on it has the same table and the same
problem. What is roster's is the values.

#### Two clocks, per kind of thing

```yaml
audit:
  profile: pipa                  # a starting point, with its arithmetic
  archive: /var/lib/roster/audit # where a row goes when it leaves
  every: 24h                     # how often the policy is applied
  by:
    holder:
      profile: gdpr              # people are under a privacy regime
    host:
      profile: forever           # a hostname is not personal data
```

`retain` is operational — what the console can show, what a query costs, how big
the disk is. `destroy` is the obligation, and it is normally years the longer of
the two. Between them the row lives in `archive`, one gzipped file per month
**per kind**.

And `by:` is why it is two clocks *per kind* rather than two clocks. A
deployment's obligations are not uniform across its entities: what was done to a
person has to stop existing eventually, and an operating record of what a
machine did usually has the opposite requirement. One clock over the table
forces the shorter of the two onto everything. The kinds are the names the
schema registered — `roster trail prune --kind nonsense` lists them.

#### The profiles carry their arithmetic

```sh
roster trail profiles
```

```
pci    retain=2160h destroy=8760h   PCI-DSS 10.5.1: one year of audit history, the last three months immediately available
hipaa  retain=2160h destroy=52560h  HIPAA 45 CFR 164.316(b)(2)(i): documentation retained six years
sox    retain=2160h destroy=61320h  SOX, via 17 CFR 210.2-06: audit records retained seven years
pipa   retain=2160h destroy=8760h   개인정보의 안전성 확보조치 기준: access records kept at least one year
gdpr   retain=2160h destroy=17520h  GDPR names no figure — Article 5(1)(e) asks for a stated limit rather than a particular one …
```

`61320h` is unreadable; *seven years, because 17 CFR 210.2-06 says seven years*
is a thing a reviewer can disagree with. That is all a profile is for.

It is **a starting point and not a compliance guarantee.** What a deployment is
obliged to keep depends on what it processes, for whom, and where — none of
which roster knows. Anything written beside a profile wins, because a deployment
that sets `destroy:` knows something the table does not.

#### A window with nowhere to put what leaves it is refused

```
audit.retain names a window and audit.archive names nowhere to put what
leaves it; set audit.archive, or audit.discard: true to say the rows are
meant to go
```

Because that configuration *works*. The sweep runs, the table stops growing,
every graph an operator watches improves — and what it is doing is destroying
the trail. `audit.discard: true` is how a deployment says it means that, and it
can be said per kind.

Read where the process comes up, not at the first pass a day later.

Nothing sweeps the **control plane's** trail. It is the record of the
deployment's own operations, it grows by the key rather than by the request, and
it is the last thing anybody wants a clock deleting from.

#### By hand

```sh
roster trail prune                                # apply the policy now, per kind
roster trail prune --older-than 2160h --dry-run   # a window of your own: how many
roster trail prune --older-than 2160h --kind holder
roster trail read --in /var/lib/roster/audit      # read an archive back
roster trail purge --older-than 61320h --dry-run  # which archives would go
```

`prune` with no window applies **the deployment's own policy**, which is what
somebody putting it in cron means. Give it `--older-than` and it is a manual act
instead — which is worth knowing, because a manual window does not consult
`by:`, so `--older-than 1ns` with no `--kind` reaches the kinds the policy keeps
forever.

It writes, `fsync`s and closes each file **before** it deletes anything, and it
deletes the rows that are in the file rather than re-running the query. So the
one failure it can leave is rows in both places, which is the direction to fail
in — `read` drops the duplicate.

Each run writes its own files, named `audit-2026-08.holder.<run>.jsonl.gz`.
Nothing takes a lock — the sweep does not, and neither does `prune` — so two
replicas, or an operator pruning while the process sweeps, would otherwise be
two writers inside one gzip stream. Read them as a set (`--in`, or several paths
at once) rather than one at a time: a row two runs both archived is dropped by
whichever read sees both.

`read` opens no database. That is the point of keeping the file: it outlives the
deployment that wrote it, and a reader that needed the deployment would be
answering the question at exactly the moment nobody can.

`purge` destroys by file and never by row. A file is named for the month and the
kind it holds, so January's goes once the cutoff has reached February — not on
the 31st, when most of it is still inside the window. There is nothing after
this.

#### A key that can read the trail can read everything

A key is the **deployment's**, and the deployment is every tenant in it — the
wall narrows nothing for one. That is the design, and a key allowed
`/roster.HolderService/List` reading every customer's people is what a service
that manages customers is for.

The trail is that property at a different size. `Audit.value` is the row as each
write left it, so one method answers **every table's contents, in every tenant,
across all time**, including rows long since deleted. It is the single widest
read this deployment has.

`roster key add` says so when a key's methods reach it:

```
NOTE: `/roster.*/*` reaches the audit trail, which holds the contents of every
write in this deployment, in every tenant, for as long as the retention policy
keeps them. A key is not walled by tenant.
```

No **role** reaches it that way — a person is walled to their own tenant, so the
same method asked by a holder is a different question. If you reach for a
wildcard because writing out eleven methods is tedious, this is the one to
notice.

#### There is no RPC for any of it

`AuditService` answers reads and refuses every write — *"the trail is written by
what happened, not by anybody asking"* — and a retention RPC beside it would be
the exception that makes the sentence false. What a trail is worth is that the
credential which lets somebody act is not the credential that lets them erase
the record of having acted. A key that prunes is a stolen key that prunes.

There is a second, sharper reason and it is roster's own wiring: `cmd/policy.go`
matches methods by **pattern**, so a role or API key holding `/roster.*/*` — and
`roster init` writes exactly that — picks up a new method the moment it is
generated, with nobody deciding. An `rk_` key skips `May` entirely and is
narrowed only by its own method list.

So both doors need the database: a shell on the box, or `serve` applying the
policy on its own clock.

### Destroying somebody, which an erase does not

`roster holder erase` makes somebody **unreachable and destroys nothing.** The
row keeps their alias, name and profile; their addresses and external identities
keep theirs; and the trail holds a copy of all of it — including the copy the
erase itself wrote, because `Audit.value` is the row as the event left it.

That is right for *this person has left*. For *destroy what you hold about them*
there is a second act:

```sh
roster forget @contoso/erin        # now, because they asked
roster forget                   # everybody whose grace has run out
roster forget --dry-run         # who that would be
roster restore @contoso/erin       # undo the erase, while there is one to undo
```

```yaml
holder:
  forget_after: 720h   # 30 days after an erase. empty is never
  every: 24h
```

**Two triggers and one act.** A request has no grace — they asked, and the clock
a regulator counts is already running (GDPR Article 12(3) gives a month;
개인정보보호법's 시행령 reads *지체 없이* as five days). An account closing has one,
and that window is **operational rather than legal**: a mistaken deletion, a
compromised account deleting things, a billing dispute. Thirty days is the
ordinary answer and it fits inside the month.

**`restore` is what makes the window a grace.** Without it, thirty days and then
destruction is a delay. There was no way to undo an erase at all — a patch
carries no `date_erased`, deliberately, since a caller who could write it could
un-erase anybody — so this goes through the database like everything else here.

It refuses somebody already forgotten. A forgotten holder has no alias, and that
is the honest answer: there is no name left to bring back.

#### What goes, and what stays

Everything that says *this person reaches here, signs in there, holds this* is
removed — addresses, external identities, verifiers, API keys, sessions,
attempts, links, and the rows that say what they may do.

The `Holder` row **stays, blank**. Its identifier is `Audit.actor_id` and twelve
foreign keys point at it, and what makes it personal data is that it *resolves*.
Emptied, it is a stable pseudonym reaching nothing: *the same someone did these
fourteen things*, with no way to say who.

The trail keeps its **events** and loses its **contents**, in the database and
in the archive both. The actor, the action, the object and the time stay;
`value` and `patch` go. Both halves matter and they pull against each other — a
version that destroyed the rows would let somebody erase the evidence of what
was done *to* them by asking to be forgotten.

Note the archive is reached only if `audit.archive` is set, and it is the one
place anything here rewrites a file. If you keep archives elsewhere as well —
backups, object storage — those are yours to reach.

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
| `/roster.DelegationService/Revoke` | ending one when somebody signs out |
| `/roster.FrontService/WhoseHost` | which tenant serves the name a browser arrived at |
| `/roster.FrontService/WhereFrom` | where the people at an address authenticate |
| `/roster.MeService/Get` | somebody's own record, through a delegation |
| `/roster.HolderService/Get` | who somebody still is — a name for a screen, and the periodic recheck that ends a session after somebody leaves |
| `/roster.SyncService/Watch` | one stream, held open, that says when somebody's sessions stopped being good — so the recheck above is a fallback rather than the mechanism |
| `/payday.TokenService/Introspect` | only if the app takes API tokens, or asks about a delegation it was given; see below |

Not `CredentialService/Set` — changing a password belongs to whatever account portal
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
| `rt_` | a tenant's | `ApiKeyService/Issue` on the data plane. Belongs to a holder, resolves to that **holder**, so the wall, the bindings and the sites all apply exactly as when that person calls |
| `rd_` | a **delegation** | a product app calling as somebody it just signed in. Resolves to that holder in the same way — but it does **not** go in `authorization`: it rides in `roster-as`, beside the app's own key |

#### A customer mints their own

```
ApiKeyService/Issue { holder: {...}, alias: "ci", methods: ["/roster.HolderService/List"] }
  → { token: "rt_…", key: {...} }
```

Served on the **data plane**, which is the port a product app already talks to,
and answered once — what is stored is a hash. `roster key add` is the other
plane's and mints `rk_`; which kind an instance mints is a fact about which
server answered rather than a field anybody sends, because a caller that could
name a prefix could ask the customer-facing port for a key of the deployment's
own kind.

Two rules run, and neither is written in the issuer — it mints through the
walled server, so a key is held to what every grant is held to:

- **nobody hands out a method they do not hold.** A key is the most direct grant
  there is: whoever holds the string calls whatever the column says.
- **nobody writes a way into an account wider than their own.** A key resolves
  to its holder, so a call made with it is made *as them* — minting one on the
  administrator's row carrying only a method you hold is a credential for the
  administrator, and the methods check alone would let it through.

Naming a holder who is not there is a refusal, not a creation. A customer's
people are the customer's.

Note a deployment with no `control:` wires `auth.Plain`, which reads no token —
so a key minted there is inert. That is the `auth.Plain` caveat, not a fact
about this call.

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
issuer is the key **row** rather than the service behind it. That is a reading
of "the caller it was issued to", and the deliberate one: a delegation lives
for minutes, and a caller whose credential has been replaced is not obviously
the same caller — a key pasted into a build log and rotated out the same
afternoon must not be able to spend the sign-ins its replacement performs, nor
the other way round. The issuer comment in `delegation.proto` says why.

What a tenant key costs is the trail: its writes are recorded as the person's,
so `Audit` says who and not which of their keys. Revoking still works, since the
row is what the token resolves through.

A `rt_` is minted over the wire by `ApiKeyService/Issue` on the data plane —
see "A customer mints their own" above. `ApiKeyService` stays unregistered, and
the issuing exception lives in its own RPC rather than as a hole in the rule
that keeps a verifier out of every answer.

A **delegation** is different and is minted over the wire: `Vouch.Delegate` is
`Verify` that also mints for the person it just proved, and `Vouch.Redeem` does
the same at the end of a magic link. A separate RPC rather than a field on
`Verify`, because a role here is a list of methods — so a Login App that must
check passwords and never mint is a different grant from a product app that
needs the token. See `delegation.proto`.

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

A `Group` is a set of people, and a binding to one reaches everybody in it — so
the rule is written once and the membership changes. Which is why **putting
somebody into a group is handing them what it holds**, and is refused on the
same terms as writing the binding was.

### You cannot hand out what you do not hold

**A grant is any write that changes what the gate will answer for somebody**,
which is a wider set than the writes that mention a role. These are all of them:

| | |
| --- | --- |
| `RoleService/Add`, `/Patch` | the methods the role names |
| `BindingService/Add` | that role, to a person or to a group |
| `TeamMembershipService/Add`, `/Patch` | that role, in that team |
| `GroupMembershipService/Add` | everything bound to that group |
| `ApiKeyService/Add`, `/Patch` | the methods on the key |
| `ApiKeyService/Issue` | the methods on the key — including one you mint for yourself, which is the same verb with your own reference |

Each is refused when it names a method you do not hold. The last one is a
person's own and is refused on the same terms, which is what lets a deployment
put a mint button on a self-service page: the widest key somebody can make for
themselves is exactly as wide as they are. The last two are the
ones that surprise people: neither says the word *role*, and each hands over as
much as one.

**What you hold, where you hold it.** A binding made in a site is a permission
held there, so it may be handed on **in that site** and nowhere wider. A site
administrator delegates inside their own site and cannot reach past it — and a
group bound across the tenant is not one they may put anybody into, even though
a group bound inside their site is.

A role held in a *team* is left out of what you may **hand out**: its scope is a
team, and the scopes here are the tenant and a site, so there is nothing to
compare. It is *not* left out of what you **hold** — see the next section, where
the difference is the whole point.

### And you cannot write a way into an account wider than yours

Resetting a password is a way to become somebody, so it is refused unless their
permissions are a subset of yours. Everything else somebody signs in with is the
same act:

| | |
| --- | --- |
| `CredentialService/Set`, `/Unlock`, `VouchService/Reset` | their secret |
| `CredentialService/Enrol` | their second factor |
| `IdentityService/Add` | an account at a provider that signs in as them |
| `EmailService/Add` | a mailbox a recovery link is sent to |
| `ApiKeyService/Add` | a key that **acts as** them |

`IdentityService/Add` and `EmailService/Add` are worth reading twice before
granting. They sound like keeping a directory tidy, and each is a way to sign in
as whoever the row is about: link an account you control to somebody's `Holder`,
or put a mailbox you read on it and ask for a link.

**What counts as theirs is wider than what they may hand out.** A person
provisioned as an administrator through a `TeamMembership`, or through a
`Group`, holds those permissions for this rule even though they may not bind
them anywhere. The two readings differ on purpose: missing a path in the first
refuses a grant somebody could have made, which is a conversation, and missing
one in the second lets an administrator be reset by anybody.

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
HostService/Add        contoso.example.com -> contoso
MailDomainService/Add  contoso.com -> entra          (optional; where they authenticate)
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
- **So registering one is your act, not a customer's.** roster does not resolve
  DNS — it is meant to run in an air gap — and it would be checking the wrong
  thing anyway: what makes traffic for a name arrive here is DNS and your
  ingress, both yours. A row naming a hostname nothing routes does nothing.

  What is left is a customer claiming a name they do not own, which takes it
  from whoever does and tells them only that somebody has it. **Do not put
  `/roster.HostService/Add` on a role a tenant's administrators hold.** Nothing
  enforces this and nothing can — it is a permission, and the wall does not
  narrow uniqueness. A mail domain needs no such care; see below.
- **A mail domain is unique within a tenant**, and two operators saying
  something about `@gmail.com` are two facts. It claims nothing, so nothing has
  to be proved: `FrontService/WhereFrom` takes the tenant the hostname resolved
  to, so what a customer writes here only ever changes where **their own**
  people are sent. That is the difference from a host, and it is why this one is
  safe to hand out.

And which provider one operator's people arrive through:

```
ConnectionService/Add  entra -> https://login.microsoftonline.com/contoso/v2.0
                       client_id, scopes, secret_ref: "env:CONTOSO_ENTRA_SECRET"
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
Vouch.Verify {who: {tenant: "contoso", address: "erin@contoso.example"}, secret: …}
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

**The rule about writing a *way in* costs the same, and does not look like it.**
`IdentityService/Add` and `EmailService/Add` are served here too and do carry
it, but it reads the control plane — where a customer's person holds nothing —
so it refuses nothing. Granting either of them on this port is granting the
account, exactly as granting `Vouch.Reset` is. That is the same waiver and it is
the silent one: on the data plane those two are guarded, and nothing about the
call says which port it arrived at.

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

Named and it is a **refusal**: `Credential.Set` and `Vouch.Reset` answer
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
a person. `roster vouch reset|set|unlock` at a shell, or the person's own panel
in the console; the same three RPCs either way.

| | |
| --- | --- |
| `/roster.VouchService/Reset` | a new password, generated here and answered with **once**. The operator reads it out |
| `/roster.CredentialService/Unlock` | opens an account ten wrong answers closed, without changing the secret (moved from `VouchService`) |
| `/roster.CredentialService/Set` | writes a password somebody chose — an account portal's, not an operator's |

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
  is an outage with nothing saying why. Erase the `ApiKey` row, which is a
  shell: `roster key revoke` reaches either plane's keys, and over the wire
  `ApiKeyService.Erase` is served on every port -- the verifier stripped on the
  way out, the row held to `mayReach` -- which the console and the account page
  draw beside the list.
- **They do not require a version.** Every other write here is a
  compare-and-swap; these take one if you have read the row and proceed without
  one if you have not, because a suspension that fails when somebody edits a
  profile is a suspension that editing a profile in a loop can prevent.
- **Nothing stops you suspending an administrator.** Escalation prevention
  covers everything that hands out a permission or writes a way in — the two
  tables above — and not this: suspending somebody is a denial of service rather
  than a way to become them. Roadmap.md's item 11.

What an app in front does with `date_invalidated` is its own half: roster
answers *invalid since when*, and the app answers *what is still alive*.

### How an app hears about it

```
POST /roster.SyncService/Watch   {}      # a stream, held open
```

One stream that takes no argument, and each message is three facts about one
person: `date_invalidated`, `date_disabled`, `date_erased`, with the tenant and
a `reason` beside them.

- **The app dials roster.** There is no subscription to register and no URL for
  roster to call back on. It is one more stream on the port your app already
  holds a credential for, and a dropped connection is a reconnect rather than a
  dead-letter queue.
- **It takes nothing because the wall is what narrows it.** A deployment `rk_`
  key hears every tenant; a credential that resolves to a person hears theirs.
  There is no field to say whose events you want, deliberately -- see
  `sync.proto`.
- **Compare, do not drop.** The message carries the instants, not an
  instruction. A session minted after `date_invalidated` is untouched; one from
  before it is not a session. `reason` is a word for your log and is
  `UNSPECIFIED` the first time this stream mentions somebody.
- **It replays nothing.** A reconnect is told what changes next, not what it
  missed, so treat a reconnect as a reason to stop trusting what you hold. The
  authority is `payday.TokenService/Introspect`, which answers synchronously;
  this stream is what saves you asking.
- **It needs a broker.** `watch.broker: none` answers `Unimplemented` here
  rather than opening a stream that will never carry anything, and `memory` is
  right for one replica and silently wrong for two. See *Running more than one*.

Allow `/roster.SyncService/Watch` on the app's key. It is a read and grants
nothing else; what it can tell the app about is exactly what that key could
already `Get`.

The same facts also arrive on `HolderService/Watch`, along with everything else
about a person -- which is why that is not the one to use here. It sends whole
rows, wakes on every rename, and refuses a subscription with no filters, so an
app would have to name people before they had ever signed in.

### What a page shows

```
POST /roster.MeService/Get   {}
```

Takes nothing, answers about the caller: their identifiers, addresses, teams,
every method they may call, and the three ways in that resolve to them — the
credentials roster holds, the provider accounts somebody else does, and the
keys that act as them. That method list is the union the server enforces, so
what a page shows and what it is allowed to do cannot drift.

It needs **no role** — requiring one to learn that you hold none is a deployment
where a new account cannot be told what it is for.

### And what a page lets somebody do about it

Four more methods on the same service, each with **no subject anywhere in it**.
That is what makes them safe to offer: none can be pointed at anybody else, so
the smallest role covering one means exactly what its name says.

| | |
| --- | --- |
| `MeService/Unlink` | take back one of their own provider accounts. Waived — no role needed |
| `MeService/SignOutEverywhere` | void everything issued to them before now. Waived |
| `IdentityService/Add`, with their own reference | attach a provider account they have just proved they control |
| `ApiKeyService/Issue`, `ApiKeyService/Erase`, with their own reference | mint an `rt_` that acts as them, and end one |

The first two need **no role**, for `Get`'s reason: they are what somebody must
be able to do with no permissions at all. The last two are features a deployment
chooses to offer, so they are named on a role like anything else.

The last two are the operator's verbs, called about your own row: a role
naming `ApiKeyService/Issue` means *mint a key for anybody no wider than you*,
and yourself is one of those. RBAC is not taught anything finer -- CLAUDE.md,
*no self-only twin of a verb* -- so the screen a person draws about themselves
is what passes only their own reference, and `mayGrant`/`mayWriteAWayIn` in
`server/core` are what keep that write no wider than they are. `Unlink` is the
one that stays a `MeService` method, because taking back your last way in is a
thing nobody may be refused for want of a role, and only a subject-less method
can be waived.

`examples/sso` draws all of it: `GET /me`, `POST /me/keys`, `DELETE
/me/keys/{id}`, `DELETE /me/ways/{id}`, `POST /me/sign-out-everywhere`.

## Signing somebody in

See [login.md](login.md) for the whole path. In short: a product app calls
`Vouch.Verify` with its key, gets yes and two identifiers, and sets its own
session cookie. roster never talks to a browser.

## Running more than one

Everything durable is in the database and nothing in `cmd/` or `server/` writes
to local disk, so a second replica needs no shared filesystem and holds nothing
the first one needs. Sessions, keys, delegations, failure counts, lockouts, the
TOTP replay window, continuations and magic links are all rows, re-read on every
request — the process holds no authoritative copy of any of them.

**`Watch` crosses replicas only if you say which broker.** There is no default,
deliberately: memory is right for one process and silently wrong for two, and a
setting that guesses would guess wrong exactly when a deployment grows.

```yaml
watch:
  broker: postgres
control:
  watch:
    broker: postgres
```

That is `LISTEN`/`NOTIFY` on the database the rows are already in — no second
address, nothing to stand up, and nothing to keep. It is scoped to a database,
so the two planes stay separate without either of them being told to: a key
being issued does not arrive looking like a person changing. Leave `watch.dsn`
empty unless the writes go through a pooler, which `LISTEN` cannot cross.

With `broker: memory` a client watching against one replica never hears about a
write that landed on another. Nothing reports it: the stream stays open and the
client looks connected. The console's live screens are the first thing this
affects, and a product app granted `HolderService/Watch` is the second.

`broker: none` is honest rather than a way out — it refuses `Watch` outright,
so a client is told rather than left listening. Another broker is
`config.RegisterBroker` in payday: a package that registers a name, blank
imported here, the same shape a database driver has and for the same reason.
Nobody has needed one.

What a broker does **not** promise is that nothing is missed. A notification
reaches whoever is listening at that moment and is then forgotten, so a
subscriber that falls behind or loses the connection is cut, and the client
re-reads a snapshot when it reconnects. That is the same contract `watch` had
in one process.

An **outbox** answers the other question, and it needs a broker to be worth
anything. It makes an event survive a crash between the commit and the publish,
by writing a row in the same transaction as the write; the drainer then
publishes into this process's broker. With `broker: memory` that reaches this
replica's subscribers and nobody else's — durability with no fan-out, which is
half an answer. With `broker: postgres` the drained event crosses like any
other.

### Testing against the database you actually run

The suite is SQLite unless `PDTEST_POSTGRES` names a server, and the two
disagree in the direction that hides mistakes: a second concurrent writer gets
`database is locked` on SQLite and dies, which makes a missing once-only
guarantee look like a working one. The single-use race, D34, is what that hid.

```sh
PDTEST_POSTGRES=postgres://roster:...@localhost:5432/roster?sslmode=disable \
  go test ./... -count=1
```

Worth running before anything that touches spending a handle -- a continuation,
a link, a delegation.

`postgres:17` as it comes is enough, which is worth saying because for a while
it was not: `Server.Close` closed one of the two planes, so every test that
built a control plane left its pool behind and the package ran out of
connections part way through. What came back was `sorry, too many clients
already` against whichever tests were running when the last one was taken -- a
different set each run, which reads exactly like a flaky suite. If that comes
back, something is holding connections rather than something needing more.

### The rest of the checklist

- **Both planes on a shared database.** The driver is named by what registers
  it, so it is `pgx` and not `postgres`; a name nothing registered is refused at
  startup rather than falling back.
- **`db.migrate: false` on every serving replica.** The default already is — it
  makes the process check the schema and refuse to start on a mismatch, which is
  what you want N of. Run the migration as its own step.
- **Seed once, out of band.** `docker/entrypoint.sh` keeps an "already seeded"
  marker on a local volume; that is a dev-image convenience and not a lock.
- **Set a maximum connection age.** `server.keepalive.max_connection_age`, and
  the same under `control` and `admin`. Unset means a gRPC client holds its
  connection forever, so a replica added to the pool gets no traffic until
  something else disconnects. The HTTP transcoders balance per request and are
  unaffected.
- **The same `vouch.keys`, in the same order, everywhere.** Any replica holding
  the whole set can read any wrapped seed, so order only decides which key new
  ones are sealed with. Rotate in two phases: give every replica the new key
  *second* in the list first, and only then move it to the front. A one-phase
  rolling change means a seed sealed by an updated replica cannot be read by one
  that has not restarted — loud rather than silent, but a sign-in failure either
  way.
- **The same breached-password corpus, or none.** It gates setting a password,
  never verifying one, so a replica without it cannot let anybody in that a
  sibling would refuse — the difference shows up as a password rejected on one
  attempt and accepted on the retry.
- **Rate limits are per process.** `grpcx.Limiter`'s memory implementation
  counts in one, so N replicas mean N times the limit. The interface is the seam
  if that matters.

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

- **`Binding` cannot be re-pointed.** Its edges are immutable, so changing who
  holds what is a delete and an add. That is the safe direction and it is worth
  knowing before writing a console screen that looks like an edit.
- **No second factor other than TOTP.** `Credential.Enrol` writes a seed, `Verify`
  and `Continue` check the codes, and the `continuation` between them is an
  opaque handle carrying *this person satisfied the first factor* — so an app
  serving two forms holds nothing but a string. [position.md](position.md),
  § "Second factors", is the why.

  Two things about it are the operator's. It needs `vouch.keys`
  (`ROSTER_VOUCH_KEYS`), because a seed is the one secret roster has to be able
  to read back, and a deployment with no key refuses to enrol rather than
  storing one in the clear. And deciding *when* to demand a second factor is
  not roster's either way — that belongs wherever the browser is; roster
  answers what is left to prove.

  **WebAuthn is here too.** `Credential.Enrol` with `kind: webauthn` takes what
  `navigator.credentials.create()` answered with, and `Verify`/`Continue` take
  an assertion — each in an envelope carrying the relying-party id, the origins
  and the challenge, because roster does not know which page a browser was on.
  What roster keeps is the public key and the **signature counter**, which is
  why verification is here at all: a counter that did not move forward is a
  replay or a clone, and a counter kept in two places is two answers.

  A key does **not** begin a sign-in, so somebody's only credential cannot be
  one. That is conservative rather than final: a passkey with user verification
  could, and telling it from a tapped security key means reading a flag in the
  assertion rather than the kind.
- **Nothing sends the magic link.** `Vouch.Link` mints one and answers with it
  once; `Vouch.Redeem` spends it, and a person with a second factor is still
  asked for it, because a link that skipped one would turn a mailbox into an
  account. What is outside roster is the **delivery** — D19 — and that is what
  makes the air-gapped case work at all: with no mail the somebody else is a
  person, and what they hand over is a password from `Vouch.Reset`.
- **Nothing here signs a token.** If several products need one sign-in, that is
  Hydra in front and roster answering it — login.md, "What changes when Hydra is
  in front". Do not reach for a JWT minted here; [position.md](position.md),
  § "The line, in one sentence", is why.
