# Running roster

The deployment's page: which database, what opens a port, how many processes,
what grows and what deletes it. What to **type** to manage customers, ways in and
permissions is [usage/](usage/); the fastest way to see it all working is the
quick start in [../README.md](../README.md#quick-start).

## Which database

Either, and the trade is size rather than capability. Everything roster generates
is SQL, and both are exercised by the suite.

| | |
| --- | --- |
| **SQLite on a volume** | one machine, one process, nothing else to run. What `deploy/` ships with, and enough for a deployment whose whole population is one company's people |
| **Postgres** | more than one replica, or a backup story somebody else already owns. Required for `watch.broker: postgres`, which is what a second replica needs |

```yaml
db:
  driver: sqlite3
  dsn: "file:roster.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate"
```

Four things in that DSN, and each is there because leaving it out fails quietly.
`foreign_keys` is off in SQLite unless asked and the schema relies on it.
`busy_timeout` is how long a connection waits for a lock before failing the
statement -- the driver's own default is a **minute**, so a lock held by a shell
command reads as a request that hangs rather than an error naming the statement.
`journal_mode(WAL)` lets readers and writers proceed together; under the default
rollback journal a long read blocks every write, and it is a property of the file,
set once and kept.

And `_txlock=immediate`, which is not a pragma at all -- it is how Go begins a
transaction, and the one of the four an operator has no way to reason about from
the database's own documentation. Two overlapping write RPCs, each reading the row
it is about before changing it, are the one case SQLite refuses to *wait* for, so
the second is refused at once and `busy_timeout` is never consulted. payday adds
it to any SQLite DSN that does not say it (`config.DbConfig.Open`, which carries
the measurements); it is written out here because a DSN is the one place an
operator can see what their database was asked for.

The driver is named by whatever registers it, so Postgres is `pgx` and not
`postgres`, and a name nothing registered is refused at startup rather than
falling back.

**`db.migrate` is off by default**, and `serve` then looks at the database before
it answers anything and refuses one that is not the shape `internal/ent`
describes, printing the SQL that is missing. That is what you want N replicas of;
run the migration as its own step. `control.db.migrate` is the same switch for the
other plane, and both are read.

## The two planes

roster runs twice in one process, on two databases: the **data plane** holds
customers and their people, and the **control plane** holds who may call this
deployment -- the operator's own people, the services, and the keys under those.
A key must not live in the tables it protects, which is why the second is a
database and not a reserved tenant. There is no query from one to the other.

```yaml
control:
  db:
    driver: sqlite3
    dsn: "file:roster-control.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate"
  # Where a console reaches it, and empty is nowhere. Bind it somewhere only a
  # console can reach: it serves `ApiKeyService`, which every other port refuses.
  addr: "127.0.0.1:50052"
```

**Leave `control` out and the deployment believes its callers** -- `auth.Plain`,
which says so once in the log. That is right for a checkout and for the sandbox,
and is not something to serve anywhere else.

`roster init` refuses a configuration with no `control.db`, and the reason is what
happens to the deployments that add one later. `ApiKey.Issue` works perfectly
under `Plain`: a name is written, a row lands on the data plane, and nothing reads
it because `auth.Bearer` is not in the chain. An expiry is optional, so the row
stays. Name a control plane afterwards and every key minted while nobody was
checking becomes a working credential at once, issued by nobody. That is not a
migration.

Writing the block with no `db:` in it is refused by name for the same reason: what
decides whether the plane exists is `control.db.driver` and nothing else, so an
address alone built a server that read no key, honoured no delegation and opened
no port, while the only line in the log was the `Plain` warning.

## The first operator

```sh
roster init
```

```
control plane
  holder admin is 019ff2...
  bound to role "everything" = /roster.*/* -- every RPC roster serves, now and after an upgrade
  password  kQ9x...

sign in to the console as admin. that password is shown once and is not stored -- write it down now.

there are no customers yet, which is the right state to start in.
```

One person -- the operator who runs this deployment, in the control plane -- and
the role and binding that let them. Nothing in the data plane at all. `--operator`
names them something other than `admin`.

That row cannot arrive over the API: a tenant is not put up from inside one, so
the first row of a deployment has nowhere else to come from, and `init` writes it
through the server instance the process holds. The role and binding are not a
convenience either -- permissions are deny-by-default, and there is no way out of
that from the API, because writing the first role needs a binding only writing the
first role could give.

The role is `everything`, and what it holds is a **pattern**. A list written at
`init` is a snapshot: the next release adds an RPC the first operator cannot call
and cannot grant themselves either, since granting is refused for anything the
granter does not already hold. It is still an ordinary row -- unbind it and it is
gone.

The password is shown once and stored as an argon2id hash. There is no
`--password` flag on purpose -- an argument is in the shell history and the
process list, which is the same rule `roster key add` is on -- and
`--password-stdin` reads one from a pipe, which is what a container entrypoint
uses. **Running `init` twice is an error** rather than a no-op: one that quietly
did nothing is one somebody runs against the wrong deployment and believes.

`init` writes no customer. A tenant *is* a customer, and one written by a command
is a customer nobody asked for -- so the first act after `init` is the same act as
the hundredth: [usage/customers.md](usage/customers.md).

## The listeners

Five at most, and which open is what the configuration named.

| | who calls it | |
| --- | --- | --- |
| `server.addr` | product apps | gRPC, **walled** and gated. Keys only -- a cookie names nobody here |
| `server.http.addr` | anything that cannot speak gRPC | the same, transcoded (Connect, gRPC-Web) |
| `control.addr` | a console, and the deployment's own services | who runs this deployment, which services call it, their keys. Takes a session cookie **and** an `rk_` |
| `control.http.addr` | a console in a browser | the same, transcoded. This is what the console talks to, and where `AuthService` is registered |
| `admin.addr` / `.http.addr` | a console | **customers**: the data plane with no wall, behind an operator's session |

```yaml
admin:
  addr: "127.0.0.1:50053"
  http:
    addr: "127.0.0.1:8081"
    allow_web: true
    origins: ["http://localhost:5173"]
```

`admin` needs a control plane and is refused without one: the port takes a session
cookie and resolves it against *that* database's holders, so with no control plane
there is nobody to be.

Three ports rather than two because it can be nothing else. The product port is
walled and an operator has no tenant in that database, so it would show them
nothing; and the control port already registers `roster.HolderService` over its
own rows, so the customer-facing one cannot join it under the same name. The rule
that falls out is one sentence:

> **Who is calling** and **what they hold** are control plane questions. What they
> are operating on is the data plane.

A browser cannot speak gRPC, so a port with no `http` block is a port a console
cannot reach. `server.http` is the wrong one for a console: it fronts the walled
data plane, where an operator's session names nobody.

## The console

```yaml
control:
  console:
    dir: /usr/share/roster/console    # empty serves no page
    admin: https://admin.example      # where the browser reaches admin.http
```

`roster serve` serves the built page at `/` on `control.http` when `dir` names it.
The customers screen talks to `admin.http` directly, which is what `admin:` is
for, and the operator who signed in on one is the caller on the other.

Signing in is `AuthService.SignIn` on `control.http` and nowhere else -- a service
is registered per listener -- and the cookie travels as `set-cookie` response
metadata, which `web.Transcode` hands to the browser as a header. It is opaque: 32
bytes from `crypto/rand` naming a row, `HttpOnly`, `SameSite=Lax` and `__Host-`
prefixed, so signing out is a delete that takes effect at once.

**The sessions are in a table**, on the control plane. In memory they were right
for one replica and *silently wrong* for two -- a cookie minted on one is unknown
to the other, intermittently, per request, with nothing in any log saying why --
and lost on restart besides, so a deploy signed everybody out. Two properties of
the table are worth knowing: the cookie value is not in it (what is stored is a
digest, so a copy of the rows is not a set of live cookies), and a session dies
with the person, because the holder is an edge. Expired rows are collected hourly
by `session.Sweep`, which nothing depends on for correctness -- `authsession`
checks both clocks when it reads one.

### What the admin port waives, and it does not look like it

An operator's standing on `admin.addr` comes from the **port** rather than from a
role, which is what lets an air gap have an operator instead of a mail server. The
cost is that two rules in `server/core` read bindings the caller has none of, so
on this port they refuse nothing:

- **writing somebody's credential** (`Credential.Issue`, `Set`, `Unlock`) is not
  held to *their permissions are a subset of yours*;
- **writing a way into an account** (`Identity.Add`, `Email.Add`) is not either,
  and this is the silent one -- those two carry the rule on the data plane, and
  nothing about the call says which port it arrived at.

So granting either of them on this port is granting the account. `cmd/admin.go`
is where that is written down beside the wiring.

The dev servers, the sandbox, and how the pages are built are
[development.md](development.md).

## One process, or four

`roster serve`, `roster account serve`, `roster ldap serve` and `roster login
serve` are four commands of one binary. A deployment that wants them all can be
four containers, or one:

```yaml
account:
  addr: :8090
  base: https://account.contoso.example
  page: { dir: /usr/share/roster/account }
  terminal: true                          # `roster sign-in`; off unless said
  keys:
    contoso: env:ROSTER_ACCOUNT_KEY_CONTOSO

ldap:
  addr: :389
  bind: key
  keys:
    contoso: env:ROSTER_LDAP_KEY_CONTOSO

login:                                    # only with Hydra in front; see login.md
  addr: :8091
  hydra: { admin: http://hydra:4445 }
  keys:
    contoso: env:ROSTER_LOGIN_KEY_CONTOSO
  clients:
    contoso: [contoso-web, contoso-mobile]
  consent: skip                           # ask draws a screen instead
  base: https://login.contoso.example     # one redirect URI for the whole app
  enrol: invited                          # invited | expected | enrolling
  page: { dir: /usr/share/roster/login }
  remember: 1h
  seal: [env:LOGIN_SEAL]
```

`account.terminal` is **off unless a deployment says so**, and it is the one
setting in that block that opens a door into an account: it lets a machine with no
browser ask for a key, approved by the person in a page they are already signed in
at ([usage/ways-in.md](usage/ways-in.md) § *A terminal, signed in from a browser*).
Off, those endpoints answer 501 and the page draws no form for them. The blunt
control is a different thing and turns off more: grant no role naming
`ApiKey.Issue` and **no** self-service key can be minted, the page's own app
passwords included.

**Named is a listener and empty is nowhere**, which is what `control` and `admin`
already do. `roster serve` opens whichever are named, in the same errgroup as the
server, so a front door that cannot come up is a start-up failure rather than a
deployment that is half there.

Each of the three is a **consumer**: it reaches roster over the wire with a tenant
key and cannot reach past it, in one process exactly as in four. `scripts/test.sh`
refuses the import rather than trusting anybody to remember. Their own designs are
[ldap.md](ldap.md) and [login.md](login.md); `account/`'s package comment is the
third.

`account.roster` and `account.connect` are left out above on purpose: in one
process they default to this deployment's own listeners, and writing them again in
the file that already says `server.addr` is one more place for two answers to
drift.

**The keys are references, not tokens.** `env:NAME` and `file:PATH` are both
understood, `ROSTER_<APP>_KEY_<ALIAS>` still works and is merged with them, and
`--key alias=rt_…` takes a literal that is in the process list and says so. Do
**not** name a `ROSTER_` variable in a reference: the configuration loader claims
those for the fields of the same name, so `env:ROSTER_LOGIN_SEAL` is read as
`login.seal` itself -- a key where a list was expected, and a process that refuses
to start.

### The key cannot exist before this has run once

`roster login serve` refuses to start without a tenant key, and minting one takes
a tenant, which somebody makes after the first boot.

```sh
roster login provision --out /run/roster-login
```

It ensures this deployment's **own** front door inside each operator named in
`login.clients` -- a `login-app` holder, a role holding exactly what the app calls
as itself, the binding, and a key -- and writes the key to `<out>/<alias>.key`, so
`login.keys` is `file:/run/roster-login/<alias>.key` and there is no Secret at
all. It makes no customer: a tenant that is not there is **skipped and said**,
because this runs on every start and a fresh volume has no customers. It replaces
rather than adds -- a key cannot be read back, so a restart is a rotation.

Beside the process is where it belongs: an `initContainer` in Kubernetes, a line
before `ExecStart` on a box. `deploy/` is that, as manifests.

### Which to run

Four processes, when the blast radius is worth the pods: the account app and the
Login App face the internet and hold one tenant key per operator, while the
control plane holds every key and the database. In one process a bug in the first
reaches the second; in four that is a kernel boundary rather than a code one.

One process, when it is not. A deployment that is four containers to run one
binary against one database pays for it in configuration, upgrades and things to
watch, and gets a boundary it may never have needed. `compose.yaml` runs the
four-process shape; this file's own configuration is the one-process one.

## Declaring the rows that are configuration

A `Connection` is a customer's directory: an issuer, a client id, the scopes, and
a reference to a secret roster stores and never reads. It is configuration by
every test one can put to it -- written once, the same on every replica, rebuilt
from what somebody wrote down -- and a row typed into a console is a deployment
that cannot be stood up twice the same way.

```yaml
# resources.yaml
resources:
  - kind: Tenant
    alias: hday
    name: Holiday Robot
  - kind: Connection
    tenant: hday
    name: entra
    issuer: https://login.microsoftonline.com/<tenant>/v2.0
    client_id: <the app registration>
    scopes: [email, profile]
    secret_ref: env:ENTRA_SECRET     # roster stores this and never reads it
  - kind: Host
    tenant: hday
    name: hday.dev
  - kind: MailDomain
    tenant: hday
    name: hday.dev
    routes: entra
```

`serve` applies these before it serves, and `roster resources apply --dry-run`
says what a file would do before a restart does it. In Kubernetes that is GitOps
without roster knowing what Kubernetes is: the file is in a ConfigMap, editing it
changes the hash, the pod is replaced, and the new one applies it.

Three properties, each deliberate:

- **It never erases.** A resource dropped from the file leaves its row where it
  is, because erase-and-add on a provider is a gap in service and, under a
  mistyped name, every identity through it orphaned silently. Removing a row is a
  person's act.
- **It writes as somebody.** A `provisioner` holder in the control plane, framed
  as the actor of every write, so the trail names which rows a file wrote. It has
  no password and no key.
- **What it writes, it owns.** A declared row carries `roster.declared` and an
  edit from a port is refused. The cost is real -- fixing a declared row during an
  outage becomes a git round trip -- so a deployment that would rather have the
  text field declares fewer things.

What is **not** declarable is `Holder`, `Credential`, `Identity` and `Email`:
people and the ways into their accounts. A file that made those is a file that
grants access to whoever can write it. `Role` and `Binding` are the same argument
one step out and are left out for now rather than refused.

## Passwords, and what closes an account

```yaml
vouch:
  breached: /var/lib/roster/leaked.txt   # unset checks nothing
  keys: [one:<32 bytes, base64>]         # required before anybody enrols a factor
  lockout:
    failures: 10        # wrong answers in a row that close an account
    for: 15m
  password:
    min_length: 8
    no_reuse: 0         # former passwords refused; 0 keeps none
```

Those are the defaults, so a file that says nothing gets them, and all of them are
checked by roster and nowhere else because only roster sees a password. The
lockout counts on the credential row, so a wrong sign-in and a wrong *current*
password on a change count together, and a second factor's wrong answers count
against the first.

There is no composition rule -- an uppercase, a digit, a symbol -- on purpose: the
guidance that once asked for those (NIST 800-63B) now asks not to. A length and
the corpus below are what it recommends instead. And there is no *never lock*: an
account that cannot be locked can be guessed at forever, so a deployment that
wants that writes a number large enough to say so.

**The corpus** is SHA-1, uppercase hex, one per line, **sorted** -- the format the
well-known list is published in, and `sort -u` is enough to make one. A file
rather than a service, because the deployment this is most careful about has no
network at all; the lookup halves the file rather than loading it, so its size
costs nothing but disk. Named, it is a **refusal**: `Credential.Set` answers
`FailedPrecondition` and the person picks again. The order is verified at startup
rather than trusted, because an unsorted file answers *no* to things that are in
it -- the quiet direction in the one feature whose whole job is to say yes.

**`vouch.keys`** is what a second factor's seed is sealed under, and a deployment
with no key refuses to enrol rather than storing one in the clear. Every replica
holds the whole set, so order only decides which key new seeds are sealed with:
rotate in two phases, giving every replica the new key *second* first, and only
then moving it to the front.

What to type -- `roster vouch reset|set|unlock`, and the same three RPCs from a
console -- is [usage/ways-in.md](usage/ways-in.md). Two rules run over all of
them, and they are the reason that page exists as well as this one: nobody hands
out a method they do not hold, and nobody writes a way into an account wider than
their own ([usage/permissions.md](usage/permissions.md),
`server/core/escalate.go`).

## Stopping somebody

Three facts an operator writes about a person, and three methods because a role is
a list of methods:

| | |
| --- | --- |
| `HolderService/Disable` | they are not to sign in, and their rows stay. A session, a tenant key and a delegation they already held all stop working |
| `HolderService/Enable` | the other direction, and a separate grant on purpose |
| `HolderService/Invalidate` | everything issued **before now** is void. No undo, and no time to give -- the server stamps it |

Neither is a lockout, which is temporary, automatic and belongs to a password, and
neither is `Erase`, which is deletion. Three things to know before handing them
out: **`Invalidate` does not touch an API key** (a key is named, listed and
revoked one at a time, because killing somebody's scripts silently under *sign out
everywhere* is an outage with nothing saying why); they **do not require a
version**, because a suspension that fails when somebody edits a profile is one
that editing a profile in a loop can prevent; and **nothing stops you suspending
an administrator**, which is a denial of service rather than an escalation and is
deliberately not covered by the escalation rules.

How an app in front hears about it is one stream, `SyncService.Watch`, and that is
the app's half: [login.md](login.md) § *Signing out reaches the issuer* and
`sync.proto`.

## The audit trail

**Forever, until you say otherwise.** That is the default and it is deliberate: a
version upgrade is not the right thing to decide how long a deployment's evidence
lasts. It is also the one table that never stops growing -- every write is a row,
and unlike a session there is nothing stale to collect; a trail row is not
expired, it is old.

The mechanism is payday's (`trail`, `config.AuditConfig`), because the `Audit`
entity is payday's and every app on it has the same problem. What is roster's is
the values.

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

`retain` is operational -- what the console can show, what a query costs, how big
the disk is. `destroy` is the obligation, normally years the longer of the two.
Between them the row lives in `archive`, one gzipped file per month **per kind**.

`by:` is why it is two clocks *per kind* rather than two clocks: what was done to a
person has to stop existing eventually, and an operating record of what a machine
did usually has the opposite requirement, so one clock over the table forces the
shorter of the two onto everything. The kinds are the names the schema registered;
`roster trail prune --kind nonsense` lists them.

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

`61320h` is unreadable; *seven years, because 17 CFR 210.2-06 says seven years* is
a thing a reviewer can disagree with, and that is all a profile is for. It is **a
starting point and not a compliance guarantee** -- what a deployment is obliged to
keep depends on what it processes, for whom, and where, none of which roster knows.
Anything written beside a profile wins.

**A window with nowhere to put what leaves it is refused**, at startup:

```
audit.retain names a window and audit.archive names nowhere to put what
leaves it; set audit.archive, or audit.discard: true to say the rows are
meant to go
```

Because that configuration *works*: the sweep runs, the table stops growing, every
graph an operator watches improves, and what it is doing is destroying the trail.

Nothing sweeps the **control plane's** trail. It is the record of the deployment's
own operations, it grows by the key rather than by the request, and it is the last
thing anybody wants a clock deleting from.

```sh
roster trail prune                                # apply the policy now, per kind
roster trail prune --older-than 2160h --dry-run   # a window of your own: how many
roster trail read --in /var/lib/roster/audit      # read an archive back
roster trail purge --older-than 61320h --dry-run  # which archives would go
```

`prune` with no window applies **the deployment's own policy**, which is what
somebody putting it in cron means; `--older-than` makes it a manual act instead,
and a manual window does not consult `by:`, so `--older-than 1ns` with no `--kind`
reaches the kinds the policy keeps forever. It writes, `fsync`s and closes each
file **before** it deletes anything, and deletes the rows that are in the file
rather than re-running the query -- so the one failure it can leave is rows in both
places, which is the direction to fail in. Nothing takes a lock, so read archives
as a **set** (`--in`, or several paths at once) and a row two runs both archived is
dropped by whichever read sees both. `read` opens no database, which is the point
of keeping the file. `purge` destroys by file and never by row, and there is
nothing after it.

### A key that can read the trail can read everything

A key is the **deployment's**, and the deployment is every tenant in it -- the wall
narrows nothing for one. `Audit.value` is the row as each write left it, so one
method answers every table's contents, in every tenant, across all time, including
rows long since deleted. It is the single widest read this deployment has, and
`roster key add` says so when a key's methods reach it. No **role** reaches it that
way, because a person is walled to their own tenant.

### There is no RPC for any of it

`AuditService` answers reads and refuses every write -- *the trail is written by
what happened, not by anybody asking* -- and a retention RPC beside it would be
the exception that makes the sentence false. What a trail is worth is that the
credential which lets somebody act is not the credential that lets them erase the
record of having acted. There is a second, sharper reason in roster's own wiring:
`cmd/policy.go` matches methods by **pattern**, so a role or key holding
`/roster.*/*` -- which `init` writes -- picks up a new method the moment it is
generated, with nobody deciding. So both doors need the database: a shell on the
box, or `serve` applying the policy on its own clock.

## Destroying somebody, which an erase does not

`roster holder erase` makes somebody **unreachable and destroys nothing**: the row
keeps their alias, name and profile, their addresses and identities keep theirs,
and the trail holds a copy of all of it. That is right for *this person has left*.
For *destroy what you hold about them* there is a second act:

```sh
roster forget @contoso/erin        # now, because they asked
roster forget                      # everybody whose grace has run out
roster forget --dry-run            # who that would be
roster restore @contoso/erin       # undo the erase, while there is one to undo
```

```yaml
holder:
  forget_after: 720h   # 30 days after an erase. empty is never
  every: 24h
```

**Two triggers and one act.** A request has no grace -- they asked, and the clock a
regulator counts is already running (GDPR Article 12(3) gives a month;
개인정보보호법's 시행령 reads *지체 없이* as five days). An account closing has one, and
that window is **operational rather than legal**: a mistaken deletion, a
compromised account deleting things, a billing dispute.

**`restore` is what makes the window a grace.** Without it, thirty days and then
destruction is a delay. It refuses somebody already forgotten, which is the honest
answer: a forgotten holder has no alias, so there is no name left to bring back.

What goes: everything that says *this person reaches here, signs in there, holds
this* -- addresses, identities, verifiers, API keys, sessions, attempts, links, and
the rows that say what they may do. The `Holder` row **stays, blank**: its
identifier is `Audit.actor_id` and twelve foreign keys point at it, and what makes
it personal data is that it *resolves*. Emptied, it is a stable pseudonym reaching
nothing.

The trail keeps its **events** and loses its **contents**, in the database and in
the archive both -- the actor, the action, the object and the time stay; `value`
and `patch` go. Both halves matter and they pull against each other: a version that
destroyed the rows would let somebody erase the evidence of what was done *to*
them by asking to be forgotten. The archive is reached only if `audit.archive` is
set; archives you keep elsewhere are yours to reach.

## What it prints

One line per call, on stderr, pretty-printed: the method, the status, how long it
took, and the trace and span ids when a caller sent some. Health checks are left
out. That is `otel:` with nothing written in it -- write a `logger` provider there
and the same records go wherever it says, with traces and metrics beside them
(`roster config env` for the variables). The trail is not this: the trail is what
was written and by whom, kept; this is what was asked, as it happens.

## Running more than one

Everything durable is in the database and nothing in `cmd/` or `server/` writes to
local disk, so a second replica needs no shared filesystem and holds nothing the
first one needs. Sessions, keys, delegations, failure counts, lockouts, the TOTP
replay window, continuations and magic links are all rows, re-read on every
request.

**`Watch` crosses replicas only if you say which broker.** There is no default,
deliberately: memory is right for one process and silently wrong for two, and a
setting that guessed would guess wrong exactly when a deployment grows.

```yaml
watch:
  broker: postgres
control:
  watch:
    broker: postgres
```

That is `LISTEN`/`NOTIFY` on the database the rows are already in -- no second
address, nothing to stand up. It is scoped to a database, so the two planes stay
separate without either being told to. Leave `watch.dsn` empty unless the writes go
through a pooler, which `LISTEN` cannot cross.

With `memory`, a client watching one replica never hears about a write that landed
on another, and nothing reports it: the stream stays open and the client looks
connected. `none` is honest instead -- it refuses `Watch` outright, so a client is
told rather than left listening. What no broker promises is that nothing is missed:
a notification reaches whoever is listening at that moment and is then forgotten,
so a subscriber that falls behind is cut and re-reads a snapshot when it
reconnects. An **outbox** answers the other question -- it makes an event survive a
crash between the commit and the publish -- and needs a broker to be worth
anything.

The rest of the checklist:

- **Set a maximum connection age.** `server.keepalive.max_connection_age`, and the
  same under `control` and `admin`. Unset means a gRPC client holds its connection
  forever, so a replica added to the pool gets no traffic until something else
  disconnects. The HTTP transcoders balance per request and are unaffected.
- **The same `vouch.keys`, in the same order, everywhere** -- see above for why the
  rotation is two phases.
- **The same breached-password corpus, or none.** It gates setting a password and
  never verifying one, so the difference shows up as a password rejected on one
  attempt and accepted on the retry.
- **Rate limits are per process.** `grpcx.Limiter`'s memory implementation counts
  in one, so N replicas mean N times the limit. The interface is the seam if that
  matters.
- **Seed once, out of band.** `docker/entrypoint.sh` keeps an *already seeded*
  marker on a local volume; that is a dev-image convenience and not a lock.

### Testing against the database you actually run

The suite is SQLite unless `PDTEST_POSTGRES` names a server, and the two disagree
in the direction that hides mistakes -- a missing once-only guarantee looks like a
working one when the second writer dies instead of racing.

```sh
PDTEST_POSTGRES=postgres://roster:...@localhost:5432/roster?sslmode=disable \
  go test ./... -count=1
```

Worth running before anything that touches spending a handle: a continuation, a
link, a delegation. `postgres:17` as it comes is enough.

## Talking to it over TLS

A key travels on **every call**, so a cleartext connection between two machines has
given it away. On the client side that is payday's `DialConfig`:

```yaml
roster:
  addr: roster:50051
  token: ${PRODUCT_ROSTER_TOKEN}     # the app's own variable, not one of roster's
  tls:
    ca_file: /etc/ssl/private-ca.pem
```

Nothing written down is plaintext, and it warns once. Serving TLS is the
deployment's: either roster's own listeners (`tls:` beside an `addr`) or a
terminator in front, which is what `deploy/` assumes and what
`scripts/cluster.sh` exercises.

## Locally, in one command

```sh
docker compose up --build
```

roster on Postgres, both planes, the console, one customer already stood up
(`contoso`, with `erin` in it), the account app, the directory, and -- for the
shape a deployment with several products has -- Hydra with the Login App beside
it, plus two demo relying parties.
[../README.md](../README.md#quick-start) has the ports and the passwords, and
[relying-party.md](relying-party.md) is what the two demos are for.

```sh
./scripts/hydra.sh          # up from nothing, one OAuth flow to a token, down
./scripts/hydra.sh --hold   # leave it up to look at
./scripts/e2e.sh            # the pages, in a browser, against a real roster
./scripts/cluster.sh        # deploy/ in k3d, with TLS ending in front of it
```

The first operator in that stack comes from the environment, applied **once** by
the image's entrypoint rather than by the CLI: `ROSTER_ROOT_USER` and
`ROSTER_ROOT_PASSWORD`, handed over on a pipe the way `POSTGRES_PASSWORD` is. A
password in an environment variable is visible in `docker inspect` and in the
compose file; this image is a development image and has no `_FILE` variant.
`ROSTER_ADMIN_*` is *not* that prefix -- that one is roster's own, for the admin
listener.

## What is not here

- **`Binding` cannot be re-pointed.** Its edges are immutable, so changing who
  holds what is a delete and an add. That is the safe direction, and it is worth
  knowing before writing a console screen that looks like an edit.
- **No second factor beyond a code and a key.** TOTP and WebAuthn, and nothing that
  has to be *delivered* -- no SMS, no push, no emailed code -- because sending is
  not roster's. What roster keeps for a key is the public key and the **signature
  counter**, which is why verification is here at all: a counter kept in two places
  is two answers. A key does not begin a sign-in, so somebody's only credential
  cannot be one.
- **Nothing sends the magic link.** `Vouch.Link` mints one and answers with it
  once; `Vouch.Redeem` spends it, and a person with a second factor is still asked
  for it, because a link that skipped one would turn a mailbox into an account.
  With no mail the *somebody else* is a person, and what they hand over is a
  password from `Credential.Issue`.
- **Nothing here signs a token.** If several products need one sign-in, that is
  Hydra in front and roster answering it: [login.md](login.md). Do not reach for a
  JWT minted here -- [position.md](position.md) § "The line, in one sentence" is
  why.
