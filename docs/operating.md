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

**A column that comes to a table which already has rows takes the default its
field declares** -- `(orm.field).default`, which the generator gives the column as
well as the server -- so every row already there holds it rather than NULL. A field
with no default is a NOT NULL column with none, and adding one to a table with
rows is refused by SQLite and by Postgres alike: that is a release that does not
start, so a field added to an existing entity says what the rows already there
should hold. CI migrates empty databases and cannot see this, which is what
`scripts/upgrade.sh` is for: it builds the release before the commit -- the nearest
`v*` tag -- lets it make a deployment's databases, puts a row in every table, and
starts the commit on them twice. **Tag what you deploy** and the `upgrade` job
follows; with no tag there is nothing to upgrade from, and it says so.

## The two planes

roster runs twice in one process, on two databases: the **data plane** holds
customers, their people and the apps acting in their tenants, and the **control
plane** holds who runs this deployment -- the roster operators, and one row per app
they run across tenants, owning that app's deployment key.
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

sign in to the admin console as admin. that password is shown once and is not stored -- write it down now.

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

## Signing in on the data plane

```yaml
sign_in:
  enabled: true     # default false
```

Off, `AuthService` is not registered on the data plane at all and a caller gets
`Unimplemented` -- not a refusal, which would be a method somebody can count
answers from. On, a **roster user** signs in to roster itself, and the cookie
they get names a holder of that plane, so every read it makes is narrowed by the
wall to their own tenant.

**Which tenant is the name they arrived at.** A `Host` row says which tenant
answers at which name, and a name nothing claims is a refusal that says so. That
is the only place the answer could come from: this request has no tenant field,
and one would be a caller naming the tenant it would like its guesses checked
against.

Behind a terminator the public name is `X-Forwarded-Host` and `Host` is whatever
the internal Service is called, so roster reads the first and falls back to the
second. A deployment whose proxy does not set it will resolve to the internal
name, and no `Host` row will claim that -- which fails loudly rather than
signing somebody in to the wrong tenant.

What it costs is what any public sign-in costs: whoever reaches the port may
guess passwords. The lockout in `vouch.lockout` is what makes that expensive,
and this being off by default is what keeps the surface off a port that is
reachable by design.

## The user console

```yaml
user_console:
  dir: /usr/share/roster/user       # empty serves no page
```

The page behind that door: a **roster user** signs in and sees their own tenant
-- their people, how those people arrive, the organisation, what each may do,
and the trail. `roster serve` serves the built page at `/` on `server.http` when
`dir` names it, and that listener is the whole of what the page calls.

**It is served where it signs in, and that is not a preference.** The session is
a `__Host-` cookie, host-only, so a page and every listener it calls have to be
one host -- and the name in the address bar is also what decides *which tenant*
the sign-in was about. So a tenant's people open the name their tenant claims,
and the page is for whoever claims it. There is nothing to configure and nowhere
to configure it.

**A tenant whose people arrive through a directory signs in through its front
door.** The page has no form for them and cannot run the round trip itself
([login.md](login.md) § *The user console, through the front door*). What it
can do is send them: the tenant writes the origin of its account app down as
`TenantConfig.front_door` -- on this page, under *what it says about itself* --
and the sign-in page draws a button per `Connection` pointing there. The front
door signs them in as it always has and hands the browser back with a link
roster minted for this name, which the page spends for the same session a
password ends in. Nothing is configured on the deployment's side: the front door
is the tenant's to name, as its providers are.

**`user_console.dir` with `sign_in.enabled` off is refused**, rather than
serving a page whose form is answered `Unimplemented` by a method that is not on
the wire. That combination looks like a working deployment from the outside,
which is the only kind of mistake worth refusing at startup.

**It is not the admin console with a filter.** The caller is the tenant's own
holder and the wall narrows every read to their tenant; the admin console's
caller is a roster operator on a port that waives two rules (below). Both pages
draw the same screens, from `ts/lib/tenant/`, and what differs is who is
calling.

## A tenant registers its own hostname

```yaml
host:
  resolver: ""      # the system's; "1.1.1.1:53" for one of its own; "none" to
                    # not ask at all
```

A `Host` row turns the name a browser arrived at into a tenant, and `name` is
unique **across the deployment** -- so the first writer took a name and the
rightful owner was refused. The conclusion used to be a permission nothing could
enforce: *do not put `/roster.HostService/Add` on a role a tenant's own
administrators hold*. Every other row a tenant needs is theirs to write, and this
one was not.

It is theirs now, and what makes it safe is a proof. **Two roads to a row, and
`Host.date_proved` is which one it was:**

| who | how |
| --- | --- |
| a tenant's own administrator | claim the name, publish what roster asks for, take it |
| a roster operator | write the row. Whoever routed the name is writing it |

Neither is second class. A front door resolves a name whichever road wrote it
(`FrontService.WhoseHost`, `cmd.Hosted`, `keys.At`), because an unproved row was
written by somebody who was trusted to.

### The exchange, which is two calls and one DNS record

The tenant's road, from the user console or from a terminal with their own `rt_`
in `client.auth`:

```sh
roster host-proof add '{"tenant":{"alias":"contoso"},"name":"contoso.example.com"}'
# token   roster-verify=8f3a...

# at whatever holds contoso.example.com's DNS:
#   _roster-challenge.contoso.example.com.  IN  TXT  "roster-verify=8f3a..."

roster host add '{"tenant":{"alias":"contoso"},"name":"contoso.example.com"}'
```

The second call is where the lookup happens -- **there is no third verb**. A
tenant who has not published yet is refused, saying which record is missing and
what it should say, so pressing the button again is the whole of what a retry is.

The operator's road is the same second call and nothing before it:

```sh
roster host add '{"tenant":{"alias":"contoso"},"name":"contoso.example.com"}'
```

Which is what the CLI run **locally** already is. A local command has no caller
to be -- it writes through the unwalled server, the way `init` and `key add` do
-- so the proof is not asked for and `date_proved` is left unset. That is the
same door every other rule in `server/core/escalate.go` waives itself at, and it
is a line of wiring rather than a permission: whoever holds the configuration
file the `db` block is in is already the deployment.

**Claiming holds nothing.** Two tenants may be claiming one name at once, each
with a value of its own, and whoever publishes theirs takes it. A claim is good
for a day (`prove.For`) and `prove.Sweep` collects the abandoned.

### A name can move, and the incumbent is not asked

Proving requires present control of DNS, and whoever has that has the name
whatever a row here says. So the latest proof wins: the incumbent's row is erased
and the new one written in one transaction. What that costs is worth knowing --
**their people stop being able to sign in at that name**, with no warning, and
where it is written down is the erase in the trail.

### Why roster resolves DNS at all, having said it should not

`proto/app/host.proto` used to say roster is meant to run in an air gap, which is
the same reason the breached-password corpus is a file and a magic link is
delivered by somebody else. That answered the wrong question: an air-gapped
deployment has no tenant registering its own hostnames either. What it has is an
operator with a shell, which is the second road above, and `host.resolver: none`
is how it says so -- a claim is then refused naming the setting rather than timing
out on a resolver that is not there.

**The query goes to the servers that hold the zone, not to a cache**, which is
not fastidiousness. A recursive resolver caches *negative* answers and the
negative TTL comes from the zone's own SOA minimum -- five minutes to an hour --
so somebody who publishes a record and presses the button is otherwise refused by
an `NXDOMAIN` their resolver remembered thirty seconds ago, twice, and concludes
roster is broken. ACME does it the same way for the same reason.

**Finding** those servers is an ordinary `NS` lookup, so a deployment whose
resolver answers for its customers' domains out of an internal zone names one
that does not: that is what `host.resolver` is for, and it is the only thing it is
for.

## The listeners

Five at most, and which open is what the configuration named.

| | who calls it | |
| --- | --- | --- |
| `server.addr` | product apps | gRPC, **walled** and gated. Keys only -- a cookie names nobody here |
| `server.http.addr` | anything that cannot speak gRPC, and a roster user's browser | the same, transcoded (Connect, gRPC-Web). Serves the **user console** and the sign-in behind it |
| `control.addr` | the roster operators' shells, and their jobs | who runs this deployment and the deployment keys of the apps they run. Takes an `rk_` |
| `control.http.addr` | the same, over HTTP | the RPCs a shell makes -- `roster control …`. **No page**, and no sign-in |
| `admin.addr` / `.http.addr` | a roster operator's browser | **customers**: the data plane with no wall, behind an operator's session. Serves the **admin console** and the sign-in behind it |

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

A browser cannot speak gRPC, so a port with no `http` block is a port no page
can reach. `server.http` is the wrong one for the **admin** console: it fronts
the walled data plane, where an operator's session names nobody. It is the only
right one for the **user** console, for the same reason read the other way --
that is where a roster user's session names somebody.

## The admin console

```yaml
admin:
  addr: "127.0.0.1:50053"
  http:
    addr: "127.0.0.1:8081"
    allow_web: true
  console:
    dir: /usr/share/roster/console    # empty serves no page
```

`roster serve` serves the built page at `/` on `admin.http` when `dir` names it,
and that listener is the whole of what the page calls: the sign-in, *who am I*,
and the customers.

**One host, and nothing to configure.** The session is carried in a `__Host-`
cookie -- host-only, no `Domain` -- so a page and every listener it calls have to
be the same host. The page used to be served by `control.http` and told to call
`admin.http` at an origin of its own, which a browser cannot do at all: the
cookie does not travel, every call arrives as nobody, and the screen is drawn
anyway.

Signing in is `AuthService.SignIn` on **this** listener, and the cookie travels
as `set-cookie` response metadata, which `web.Transcode` hands to the browser as
a header. It is opaque: 32 bytes from `crypto/rand` naming a row, `HttpOnly`,
`SameSite=Lax` and `__Host-` prefixed, so signing out is a delete that takes
effect at once.

**The control listener serves no page.** What it answers is what a shell asks --
`roster control holder ls`, `roster control key add`, the deployment's own rows
-- and it takes an `rk_` rather than a cookie. Managing who runs the deployment
is a terminal's job; a browser's is customers.

**The sessions are in a table**, on the control plane, because an operator is a
holder of that plane. In memory they were right for one replica and *silently
wrong* for two -- a cookie minted on one is unknown to the other, intermittently,
per request, with nothing in any log saying why -- and lost on restart besides,
so a deploy signed everybody out. Two properties of the table are worth knowing:
the cookie value is not in it (what is stored is a digest, so a copy of the rows
is not a set of live cookies), and a session dies with the person, because the
holder is an edge. Expired rows are collected hourly by `session.Sweep`, which
nothing depends on for correctness -- `authsession` checks both clocks when it
reads one.

### `admin.addr` is root, and a role on it is not a fence

Standing here comes from the **port** rather than from a role, which is what
lets an air gap have a roster operator instead of a mail server. Say what that
means plainly: **this port is root.** It is the data plane with no wall, and two
rules in `server/core` read bindings the caller has none of, so on this port they
refuse nothing:

- **writing somebody's credential** (`Credential.Issue`, `Set`, `Unlock`) is not
  held to *their permissions are a subset of yours*;
- **writing a way into an account** (`Identity.Add`, `Email.Add`) is not either,
  and this is the silent one -- those two carry the rule on the data plane, and
  nothing about the call says which port it arrived at.

So a role granted on this port is narrower than it looks: the methods it names
are the ones a caller may reach, and the rules that would have narrowed *whose
rows* are not running. Granting either of the two above is granting the account.
`cmd/admin.go` is where that is written down beside the wiring.

**What makes that acceptable is that it is no longer the only door.** A tenant's
own administrator signs in on the data plane (`sign_in.enabled`) and works
through the **walled** plane, where both rules are in force and the wall narrows
every read to their tenant. So the deployment's root is for what only somebody
outside every tenant can do -- making and unmaking tenants, and the recovery a
tenant with one administrator has nobody inside it for -- and a tenant's own
work has a door of its own.

It stays root rather than being trimmed to that list. A root that can be
surprised by what it cannot do is worse than one that is documented, and the
answer to *should an operator be doing this?* is a question about the deployment
rather than a method table.

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
  # key: left out -- one rk_ made at start, narrowed per request to the holder
  # each tenant nominated. A process of its own names it: file:… from
  # `roster account provision`. (`keys:` is a tenant running its own copy.)

ldap:
  addr: :389
  bind: key
  key: file:/run/roster/directory.key     # `roster control key add --allow … directory`,
                                          # and `roster app install … directory` per tenant

login:                                    # only with Hydra in front; see login.md
  addr: :8091
  hydra: { admin: http://hydra:4445 }
  # key: left out -- one rk_ made at start, narrowed per request. A process of
  # its own names it: env:ROSTER_LOGIN_KEY, or file:… from `roster login provision`

  consent: skip                           # ask draws a screen instead
  base: https://login.contoso.example     # the origin every connection's redirect is under
  enrol: invited                          # invited | expected | enrolling
  page: { dir: /usr/share/roster/login }
  remember: 1h
  seal: [env:LOGIN_SEAL]

scim:                                     # where a directory provisions people; see scim.md
  addr: :8092                             # a listener of its own, and /scim/v2 is all it serves
```

`account.terminal` is **off unless a deployment says so**, and it is the one
setting in that block that opens a door into an account: it lets a machine with no
browser ask for a key, approved by the person in a page they are already signed in
at ([usage/ways-in.md](usage/ways-in.md) § *A terminal, signed in from a browser*).
Off, those endpoints answer 501 and the page draws no form for them. The blunt
control is a different thing and turns off more: grant no role naming
`ApiKey.Issue` and **no** self-service key can be minted, the page's own app
passwords included.

### The front door's mail

The account app mails three links roster mints for it: one that **recovers** an
account (a password, shown once), one that **confirms** an address, and one that
**deletes** an account for somebody whose way in cannot say when it asked them
([usage/ways-in.md](usage/ways-in.md)). roster mints and sends nothing; the front
door delivers -- D19's line, held: the SMTP client is `package mail`, which only
the front door's process builds, from `account.mail`, in `roster account serve`
and in the account app `roster serve` runs alike.

```yaml
account:
  mail:
    addr: smtp.postmarkapp.com:587          # host:port; empty sends nothing
    username: env:SMTP_USER                 # or as itself: apikey
    password: env:SMTP_PASSWORD             # env:NAME or file:PATH, never the password
    from: "Contoso <no-reply@mail.contoso.example>"
    tls: starttls                           # starttls (default) | implicit (465) | none
    timeout: 30s                            # one message, dial to last answer
```

`username` is written as itself -- a provider's is often a fixed word like
`apikey` -- or as a reference in the same words as the password, for a provider
whose username is a credential too (SES's is derived from an access key);
`password` is a **reference** and nothing else, and a value written as the
password itself is refused at start. `tls: starttls` refuses a server that
does not offer STARTTLS rather than speaking to it in the clear; `implicit` is TLS
from the first byte, which is port 465; `none` is the clear, and reaches only this
machine or a host named in `plaintext_hosts` -- a sink in development, never a
provider -- and never with a password. A port left out is 587, 465 or 25 by the
mode. Every one of those is checked when the process starts, not at the first
message somebody waits for.

**Left out, the door cannot send, which is how it shipped.** The three flows
answer 501, `/providers` says `recover: false` and `leaveLink: false`, and the
page says to ask whoever runs the tenant. Nothing else changes.

**What goes out** is plain text in the account app's language: what the link is
for, the link on a line of its own, and what to do with a message nobody asked
for -- the link is the one thing in it to follow. A recovery link goes only where
it would work: to an address somebody proved, of somebody not suspended, in a
tenant that is open and offers passwords -- exactly what roster mints a working
link for, asked by the front door before it asks roster for one. The recovery
form answers the browser the same whether a message went or not, and a failure
to send is the log's, never the form's. At a tenant whose people have no
password, the form answers 403 instead, as `/providers` has already told the page
with `recover: false`: that is the tenant's configuration and says nothing about
anybody. The confirmation and deletion links are asked for by somebody signed
in, and a failure is said to them as *cannot send*.

**What the log carries** is the address cut to its domain and the server's code:
never the link, its token, or a whole address. A server that refuses a recipient
says who, and that is cut out too.

**How much it sends is limited.** The recovery form is one anybody may fill in,
and unlimited it would be a stranger filling somebody's mailbox from your
address -- and your provider suspending the account for the complaints. So:

- **An address** is sent three of one kind -- recovery, confirmation, deletion --
  and then one every twenty minutes, counted for each person signed in who asks,
  and for the recovery form by the address alone. A link lasts fifteen, so that
  is a fresh one about as often as the last runs out, and three is somebody
  asking again while the first is slow to arrive.
- **A person signed in** may have five sent, of every kind together, and then one
  every ten minutes: enough to confirm a few addresses and ask again, and the
  limit that holds one account, since `/verify` mails whatever address somebody
  writes onto their own row.
- **Eight at most are on their way at once**, and one more is dropped and logged
  rather than queued. A front door's mail is a trickle and a provider answers in
  well under a second; a server that stops answering then holds eight
  connections, not one per request.

Per person, because shared it could be held spent: somebody who wrote another's
address onto their own row and asked for its confirmation every twenty minutes
would keep its owner from ever being sent the confirmation that claims it back.
What one person has sent an address spends nothing of what another may send it,
and the person's own allowance is what bounds them. The recovery form has nobody
to count by, so a stranger there can spend an address's allowance -- with links
sent to its owner, which work for them. What is counted is a message that went,
never a request refused before one would, and the recovery form counts only what
it sends, so a stranger typing addresses adds nothing. It answers 202 whatever
the limits did, so a message held back there is the log's
(`not sent, its recovery messages are spent`). Confirming, claiming and the
deletion link are asked for by somebody signed in, and answer them 429 with
`Retry-After`, or 503 when eight are already on their way (`dropped`, in the
log). `account/outbox.go` holds the numbers.

**The limits are per process.** They are counted in memory, as the codes of a
terminal signing in are: a restart forgets them, and N replicas of the account app
allow N times as much. Nor do they bound a list: somebody holding many real
addresses can have each sent its allowance. Put the front door behind an edge that
limits each client, as you would any form a stranger can fill in.

**Deliverability is the provider's and yours.** Send from a domain of your own
through a transactional provider -- Amazon SES, Postmark, Mailgun, Resend, NHN
Cloud Email, or whichever your organisation already has -- and publish the SPF,
DKIM and DMARC records it gives you for that domain, so that what it signs is
what `from` says. A message from a domain that says nothing about who may send
for it goes to spam, or nowhere, and a recovery link that never arrives is a
person locked out. Gmail's SMTP is for development only: it caps how much a day,
its terms are a person's mailbox and not a deployment's, and it sends as the
Gmail account rather than as your domain.

**In development, Mailpit** catches everything and sends nothing. `compose.yaml`
runs one beside the account app and reads it at `http://localhost:8025`;
`scripts/e2e.sh` runs one for the browser specs, which read what was sent through
its API. The block for one:

```yaml
account:
  mail:
    addr: mailpit:1025
    from: "Contoso <no-reply@contoso.example>"
    tls: none
    plaintext_hosts: [mailpit]              # a sink on the network, named
```

**Named is a listener and empty is nowhere**, which is what `control` and `admin`
already do. `roster serve` opens whichever are named, in the same errgroup as the
server, so a front door that cannot come up is a start-up failure rather than a
deployment that is half there.

**And a key unsaid is a key made at start.** `account.key` and `login.key` left
out in one process are the rows `roster account provision` and `roster login
provision` would have written -- for each app, one `rk_` and, in each tenant with a
`Host` row, a holder, a role holding exactly what the app calls as itself, one
binding, and the nomination that answers the key as that holder -- with the tokens
kept in memory. That is one replica, and a restart is a rotation: the
account page asks everybody to sign in again, which is all a rotation costs, since
the key is a row like any other whichever way it was made. A tenant that registers
a name later is nominated in at the next start, and the account app finds it on
the first request for that name without one. In four processes nothing is made
for you: each `provision` writes its keys into a directory the app's own process
reads with `file:`, and a key that was written down is left exactly as written --
a reference that resolves to nothing is a refusal, never a fresh key made quietly
over it. The role is written as exactly the app's list on every run, so a release
whose app calls something more -- the account app's `EmailService/Attest`, which
it was missing -- is a restart in one process and `provision` run again in four.
The directory's key is still yours to write either way:
`roster control key add --allow /roster.NominationService/List directory`, and
`roster app install --tenant … --role … directory` in each tenant it serves.

**A seal unsaid is one too**, and costs the same kind of thing. `account.seal` and
`login.seal` left out are a key made at start, so a second replica opens none of
the sessions the first sealed and neither does the process after a restart. The
account page asks everybody to sign in again; the Login App asks each browser
Hydra still remembers for the form once more, at the next product it opens. It
used to wave that browser through and hand back a token carrying `sub` alone, for
as long as Hydra remembered ([login.md](login.md) § *The app's own session, and
why it is not shorter*). Name one -- 32 base64 bytes, as `env:` or `file:` --
where either of those matters.

Each of the three is a **consumer**: it reaches roster over the wire with its own
key and cannot reach past it, in one process exactly as in four -- each with one
`rk_` narrowed per request to the holder each tenant nominated for it
([apps.md](apps.md)), or, run by a tenant for itself, that tenant's `rt_`. `scripts/test.sh`
refuses the import rather than trusting anybody to remember. Their own designs are
[ldap.md](ldap.md) and [login.md](login.md); `account/`'s package comment is the
third.

`scim:` is a fourth, and holds no key at all: each request forwards the
directory's own tenant key and nothing else, so what it may do is that key's and
its role's. It is a listener of its own rather than a path on `server.http`,
because that listener answers every RPC over Connect and the one exposed to a
directory's cloud should answer SCIM alone ([scim.md](scim.md)).

`account.roster` and `account.connect` are left out above on purpose: in one
process they default to this deployment's own listeners, and writing them again in
the file that already says `server.addr` is one more place for two answers to
drift.

**A flag of their own commands is the setting it says.** `roster account serve`,
`roster ldap serve`, `roster login serve` and `roster scim serve` read the same
blocks, and each flag that says one of their settings is bound to it: `--listen`
is `ldap.addr` to `roster ldap serve`, and `--help` prints the variable beside it,
`[$ROSTER_LDAP_ADDR]`. The loader applies a flag over the file and the
environment, and only when it was given, so an empty value is a value --
`--listen=` clears what the file says rather than leaving it, and
`--insecure=false` turns off an `insecure: true` the file wrote. The same holds for
the flags elsewhere that say a setting: `--hydra` of `roster login doctor` is
`login.hydra.admin`, `--file` of `roster resources apply` is `resources`, and
`--in` of `roster trail read` is `audit.archive`. What is
not a setting is read as it was: `--key alias=rt_…` and the `--base` of
`roster ldap serve` add to the block's map rather than replace it.

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

Two halves, and neither takes a list of tenants.

**Once:** one deployment key (`rk_`), on a control-plane holder, written to
`<out>/login-app.key` -- so `login.key` is `file:/run/roster-login/login-app.key`
and there is no Secret at all. What it allows is the three reads that work out
whose flow this is and nothing else.

**Per tenant that registered a name:** this deployment's own front door inside
that tenant -- a `login-app` holder, a role holding what the app calls as itself,
one binding -- and a `Nomination` saying the key's control-plane holder is
answered as it there, which is what roster narrows the one key **to** on every
call. Found off the `Host` rows rather than a list, because a tenant that
registered a name is a tenant this app fronts (#42). Once per tenant, whatever
number of names it has, and it touches no other app's nomination: two
roster-hosted apps arriving at one name are two nominations, not one field to
fight over (#73).

A deployment with no names yet is **said and not refused**, because this runs on
every start and a fresh volume has nothing to nominate. It replaces rather than
adds -- a key cannot be read back, so a restart is a rotation.

**A tenant's first name declared in `resources:` is nominated in on the next
start, not this one.** This runs *before* the server, and the server is what
applies `resources:` -- so the pass that would nominate in a tenant that has just
got its first `Host` row happens before the row exists, and the tenant has no
`Nomination` for the key. A flow for it then resolves to a tenant and is refused
at `Vouch.Delegate`, because `keys.At` will not answer as the key for a tenant
that nominated nobody for it. One restart fixes it. A further name for a tenant
already nominated in works on the start that applies it, and a deployment that
adds names by hand rather than by file does not have it at all: write the row,
and the next start nominates.

**Upgrading from `Host.acts_as`.** The field is gone (`host.proto` reserves 8).
On Postgres its column is left where it was; on SQLite the migration rebuilds
the table without it. Nothing reads it either way, and going back a version
puts the column back empty and its `login provision` fills it again. The first
start of this version's `login provision` writes the nominations from the
names, so a deployment that runs it as an init container -- `deploy/` does --
needs nothing done. A roster-hosted app other than the Login App that relied on
a name's `acts_as` is refused from the upgrade on, until `roster app install`
nominates it in each tenant: the table nominations live in arrives with the
upgrade, so there is nothing to write ahead of it.

Beside the process is where it belongs: an `initContainer` in Kubernetes, a line
before `ExecStart` on a box. `deploy/` is that, as manifests.

### Which to run

Four processes, when the blast radius is worth the pods: the account app and the
Login App face the internet and hold the credentials for every tenant they front,
while the control plane holds every key and the database. In one process a bug in the first
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
    alias: contoso
    name: Contoso
    config:                              # how the tenant signs in; see below
      password: false                    # everybody arrives through a directory
      front_door: https://account.contoso.example
      profile: { fill: true }            # a sign-in fills a blank name and picture
      leave: false                       # whether people may delete their own account
  - kind: Connection
    tenant: contoso
    name: entra
    issuer: https://login.microsoftonline.com/<tenant>/v2.0
    client_id: <the app registration>
    scopes: [email, profile]
    secret_ref: env:ENTRA_SECRET     # roster stores this and never reads it
    subject_claim: oid               # Entra's sub is one per app; oid is the person
  - kind: Host
    tenant: contoso
    name: contoso.example
  - kind: MailDomain
    tenant: contoso
    name: contoso.example
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
- **What it writes, it owns.** A declared row carries `roster.declared` and is
  **read-only** at a port: an edit is refused, and so is an erase, which used to
  pass and came back at the next start. The cost is real -- fixing a declared row
  during an outage becomes a git round trip -- so a deployment that would rather
  have the text field declares fewer things.

**A tenant's settings are applied field by field.** `config.password` and
`config.front_door` are how a tenant signs in -- the switch `Vouch.Verify`
enforces, and the origin the user console sends a browser to -- and they are
configuration by the same test a `Connection` passes. A key absent from the file
leaves that setting as it is: `password` has presence on purpose (*unset is yes*),
so a file that does not mention it never turns passwords off by omission, and
`config:` left out altogether touches nothing. A `front_door` is an origin and
nothing more, from a file exactly as from a form. `config.leave` is whether the
tenant's people may delete their own account (`MeService.Leave`), off unless a
file or the admin console says so, and like the others a file that does not mention it
leaves it as it is.

What is **not** declarable is `Holder`, `Credential`, `Identity` and `Email`:
people and the ways into their accounts. A file that made those is a file that
grants access to whoever can write it. `Role` and `Binding` are the same argument
one step out and are left out for now rather than refused.

### Where a provider sends people back

Each connection has a redirect of its own, under the front door's origin
(`login.base`, `account.base`):

```
https://login.contoso.example/callback/contoso/entra
                                       ^tenant ^connection
```

That URL, exactly, is what is registered at the provider as the redirect URI
-- for each front door that offers the connection, since the account app's
origin is its own. A sign-in sent to any other is refused by the provider before
it reaches roster, and one that comes back to a URL naming another connection
than the flow went to is refused by the front door.

**One redirect per connection is the point.** It was one URL for every tenant
and every connection, `/callback`, and a tenant's administrator may connect a
server of their own: a browser sent there could be sent on to another tenant's
provider, whose code then came back to the one URL every flow shared and was
exchanged at the first server's token endpoint -- a mix-up (RFC 9700 § 4.4).
Beside it, every round trip carries a **PKCE** challenge (S256), so a code is
worth nothing without the verifier only the front door that asked holds, and an
OpenID one a **nonce** the ID token has to carry back.

Upgrading from a release that used `/callback`, every connection's provider
registration changes: register each connection's own URL, then roll the front
doors. A provider that still has only the old one answers *redirect URI
mismatch* at the sign-in, before anybody is signed in as anybody.

### GitHub

GitHub signs people in with OAuth 2.0 and no OpenID Connect -- there is no
discovery document and no ID token -- so a front door asks GitHub's API who
signed in instead (`arrives/github.go`). A connection is GitHub's when its
**issuer** is GitHub's, the same fact the sign-in page draws GitHub's mark
from; the name is still yours:

```yaml
  - kind: Connection
    tenant: contoso
    name: github
    issuer: https://github.com           # GitHub's, and nothing else makes it so
    client_id: <the app's client id>
    secret_ref: env:GITHUB_SECRET        # GitHub wants the secret at its token endpoint
```

- **Register a GitHub App or an OAuth app** with the connection's callback --
  `https://<the Login App>/callback/<tenant>/<connection>`, and the account
  app's when it offers GitHub too. One connection is one client id for both
  front doors, and an OAuth app has one callback URL, so a deployment whose two
  front doors both offer GitHub registers a GitHub App, which takes several.
  Register the URL itself and not a path above it: GitHub sends a browser to
  any path **under** a registered callback, so a callback of `.../callback`
  would answer for every connection of every tenant, which is the one thing a
  redirect per connection is there to stop. A GitHub App needs the *Email
  addresses* account permission (read-only); its permissions are its scopes.
- **The person is their account's `id`**, the number GitHub never changes or
  hands to somebody else -- not their username, which they may change and
  somebody else may then take. `subject_claim` names a claim of an ID token, so
  it says nothing for GitHub, and a sign-in through a GitHub connection that
  says `oid` is refused.
- **Their address is the primary one**, from `GET /user/emails`, and verified
  only when GitHub says so. That is the condition `expected` matches an
  invitation under, and the stamp on the address roster keeps. The connection
  asks for `user:email` whatever `scopes` says; an account whose addresses
  GitHub will not say -- a GitHub App without the permission -- signs in with
  none.
- **github.com.** A front door told its GitHub is elsewhere
  (`login.Config.GitHub`, `account.Config.GitHub`) reads that host as GitHub
  Enterprise Server, with the API at `/api/v3` beside it; nothing in the
  configuration file says so yet.
- **The token is spent at the sign-in**, for the two questions and the
  picture, and kept nowhere: roster needs who somebody is, not a credential for
  their GitHub.

### Any Microsoft account

One Entra directory is the connection at the top of this section. *Sign in
with Microsoft* for anybody is Microsoft's authority for more than one
directory, and it is a connection too:

```yaml
  - kind: Connection
    tenant: contoso
    name: microsoft
    issuer: https://login.microsoftonline.com/common/v2.0   # any account
    client_id: <the app registration>
    scopes: [email, profile]
    secret_ref: env:MICROSOFT_SECRET
```

| issuer | who arrives | the registration's *supported account types* |
| --- | --- | --- |
| `.../common/v2.0` | any work, school or personal account | any directory and personal accounts |
| `.../organizations/v2.0` | any work or school account | any directory |
| `.../9188040d-6c67-4c5b-b112-36a304b66dad/v2.0` | personal accounts only | personal accounts |

The first two publish a document whose issuer says `{tenantid}` where a
directory goes, and each token names the one its person is in, as `iss` and as
`tid`. A front door accepts that one difference and holds every token to it --
its `iss` has to be the template with its own `tid` in it -- and refuses any
other document that names an issuer the connection does not. Personal accounts
alone are not this shape: `consumers` publishes the one directory they are all
in, so the connection names that directory's issuer, which is the third row.

- **Who somebody is**: `sub` by default, one per app registration, or
  `subject_claim: oid`, the same for every app and needing the `profile` scope.
  Microsoft issues both as GUIDs that are unique on their own, so either names
  one person whichever directory they came from.
- **What their address is worth.** Microsoft sends no `email_verified`. What it
  sends instead is `xms_edov`, *the address's domain is one the person's
  directory has proved it owns* -- an optional claim the registration has to ask
  for, in the manifest's `optionalClaims.idToken` beside `email`. An address is
  verified on that and on nothing else -- and on that only in a token
  Microsoft's own sign-in issued (`login.microsoftonline.com`, or one of its
  national clouds): the same claim from any other issuer is whatever that
  issuer meant by it. Without it, any directory's
  administrator can put any address on a user of their own, and a front door
  that matched an invitation by the bare `email` of a token from any directory
  would hand that user whichever account carries it -- the mistake known as
  nOAuth. So with no `xms_edov` every address from Microsoft is kept
  unverified, and `expected` finds nobody through it by address. Microsoft's
  manifest reference says a registration for personal accounts as well as
  directories cannot use optional claims; where that holds, nothing a `common`
  connection hands over is verified, and people are found by their subject.
- **Leave `acceptMappedClaims` off.** It lets a claims-mapping policy rewrite
  what the token says, and on a registration for any directory, any directory's
  administrator can write one for it -- `xms_edov` included. Microsoft's own
  warning says the same; a guide that adds `xms_edov` by turning it on is
  trading the check for the claim.
- **A directory is not a tenant here.** Everybody who arrives through the
  connection is in the tenant it belongs to, whichever directory they came from;
  which tenant is the front door's to say, as it is for every connection.

### Google

Google's sign-in is an OpenID provider, and a connection to it is one like any
other:

```yaml
  - kind: Connection
    tenant: contoso
    name: google
    issuer: https://accounts.google.com
    client_id: <the OAuth client's id>
    scopes: [email, profile]
    secret_ref: env:GOOGLE_SECRET
```

- **Register an OAuth client** of the *Web application* type in the project's
  Google Auth Platform (*Clients*), with the connection's callback as an
  authorized redirect URI -- `https://<the Login App>/callback/<tenant>/<connection>`,
  and the account app's when it offers Google too. One client takes several.
- **The person is their `sub`**, which Google never hands to anybody else, and
  their address is verified on Google's `email_verified`.
- **When Google asked them** is the one thing to set up beyond the client.
  Somebody deleting their own account (`MeService.Leave`) has to have proved who
  they are a few minutes ago, which through a directory is when the directory
  says it asked them (`auth_time`). Google says it only to an app that asks for
  it by name -- every round trip to Google's issuer does, in the `claims`
  parameter -- and only where the project is published, verified, and has
  *Session age claims* turned on (Google Auth Platform, *Settings*, *Advanced
  Settings*). It is off until somebody turns it on, and while it is off nobody
  deletes their account, or gives themselves a first password, on a Google
  sign-in: the account page mails them a link that deletes the account where
  it can send mail, and otherwise it is you who deletes it
  ([usage/ways-in.md](usage/ways-in.md#deleting-your-own-account)).
- **Google does not ask again when asked.** Its guide says it does not support
  reauthentication requests; it lists no `max_age` among the parameters it
  takes, and its `prompt` has no `login`. So a front door sends it neither, and a
  Google sign-in is a moment ago only when Google has just asked them -- which it
  does in a browser it has not signed in, such as a private window.

### A picture from the directory

A tenant may have a sign-in fill what its people's profiles lack: a display
name, and a picture, which roster keeps at the sizes a screen draws
(`Holder.portrait`, [entity.md](entity.md)). It is the **tenant's** setting,
`config.profile`, because tenants differ on it -- one wants nothing copied out of
its directory, one keeps its pictures in Entra, where Teams puts them, one
keeps them in Slack:

```yaml
  - kind: Tenant
    alias: contoso
    config:
      profile:
        fill: true                       # unset is no
        slack: env:CONTOSO_SLACK_TOKEN   # optional; see below
```

Or the checkbox in either console's tenant settings, for a tenant no file
declares. Only blanks, once: what a person or an administrator wrote stays,
nothing watches a source afterwards, and emptying a field is how somebody asks
for the source's again at their next sign-in. The front doors read the setting
at each sign-in through a provider and write with `HolderService.Fill`, which
their roles hold whatever a tenant says -- it writes only blanks and reads
nothing back.

Without `slack`, the source is the directory the person signed in through, and
what the connection needs for the picture depends on where that directory keeps
one:

| directory | where the picture is | what the connection needs |
| --- | --- | --- |
| Google | the token's `picture`, a URL anybody can fetch | `profile` among its scopes, which a Google sign-in asks for anyway. Nothing to consent to beyond the sign-in. The URL is kept as `profile.picture`, beside roster's copy |
| Entra | behind Microsoft Graph, where userinfo points | `User.Read` among its scopes, **consented for the whole directory first** (below). The URL is not kept, since no browser holds the token it needs |
| GitHub | the account's `avatar_url`, which the sign-in already read | nothing. github.com's is a URL anybody can fetch and is kept as `profile.picture`; GitHub Enterprise Server's is on its own host, so it is fetched with the token and the URL is not kept |
| anything else | the token's `picture`, else userinfo's | `profile`. A picture on the provider's own API host is fetched with the token, as Entra's is; one anywhere else only over https from an address on the internet -- so a directory inside the network that keeps pictures on another inside host gives none |

**Entra: the consent before the scope.** A scope added to a connection is asked
for at the next sign-in, and Entra asks each person for a permission the
directory has not consented to as a whole. Where its user consent is off, that
is *admin approval required*, and nobody signs in through that directory until
an administrator consents -- and with `password: false`, nobody signs in at all.
So, in this order:

1. The app registration's **API permissions**. Microsoft Graph `User.Read`
   (delegated) is there by default, since the portal adds it to every
   registration; **Grant admin consent** for the directory, and its status reads
   *Granted*. The button consents to everything in that list for everybody in
   the directory. `User.Read` is the signed-in person's own profile and photo,
   while they are signed in, and nobody else's. Adding the OpenID permissions
   `openid`, `profile` and `email` to the list first makes the sign-in itself
   ask nobody, somebody new included.
2. Then `scopes: [email, profile, User.Read]` on the connection.

`fill` with the scope still unchanged is safe either way: the name is filled,
and a picture Graph refuses is a warning in the log naming `User.Read`, with the
next sign-in trying again.

**From Slack, where people keep theirs.** A directory has whatever was typed
when the account was made; Slack has the photograph its owner chose. With
`slack`, the tenant is filled from its workspace and its directory is asked for
nothing: at a sign-in whose profile has a blank, the front door asks Slack who
it has under the address the person signed in with (`users.lookupByEmail`), and
fills the blank with their **real name** and the **picture they chose** --
Slack's placeholder is nobody's picture and is left out. A person with nothing
blank costs Slack nothing; somebody Slack does not have, or has deactivated, is
left as they were.

`slack` is a reference, in a `Connection.secret_ref`'s words: where the
deployment keeps the token -- `env:NAME`, in the front door's environment --
and not the token. And it is **the deployment's to write**: what it names is
one of the deployment's secrets, which the front door sends to Slack, so roster
refuses a new one from a tenant's own administrator, who may keep it or take it
away. The file declaring the tenant is where it is written, or an operator at
the admin port. A connection's `secret_ref` is held to the same rule, and so is
moving the issuer of a connection that has one: a reference is the deployment
naming its own secret, and the issuer is where that secret is sent.

The token is a Slack app's, installed in that workspace with two bot scopes
and nothing else:

```yaml
# The app's manifest, at api.slack.com/apps -> Create New App -> From a manifest.
display_information:
  name: roster
oauth_config:
  scopes:
    bot: [users:read, users:read.email]
```

*Install to Workspace* hands over the *Bot User OAuth Token* (`xoxb-…`), which
goes in the front door's environment under the name `slack` names. It reads
everybody's profile in the workspace, address included, so it is kept like any
other secret; a token short of a scope is a warning at the sign-in naming the
one it lacks.

### Declared rows

Two things declare rows: this file, and the configuration that turns on one of
roster's own front doors -- `login provision` and the in-process account app
write the front door's holder, role, binding and nomination in every tenant with
a name, labelled `roster.declared: config: login` / `config: account`. Either
way the row is read-only to everybody who reaches it through a port: a tenant
administrator, the user console, the admin console.

Turning one off is two steps, and neither is at a console:

1. Take it out of where it is declared -- the resource out of the file; the front
   door out of the configuration and its manifests (the init container
   included) -- and deploy.
2. Erase what is left, from a shell on the box: `roster ... erase` reading the
   database directly is the deployment's own work, which a declared row allows.

### The CLI on the box is the deployment

`roster <entity> <verb>` with `--config` opens the databases itself and calls
the servers in the same process, with no wall and no gate -- there is no caller
to ask anything of, so there is no frame, and the trail names nobody for what it
writes (`resources apply` is the exception: it frames itself as `provisioner`).
It is what a deployment does before anybody can sign in -- `init`, migrations,
`login provision` -- and the way out of a broken one.

It is not a door that can be locked from inside roster: whoever has a shell in
the pod has the database files and the configuration, keys included, and could
write the rows without the CLI. What guards it is who may open that shell
(`kubectl exec` on the namespace). Day-to-day work goes over the wire instead --
`client.addr` with your own credential (`roster sign-in` makes one for a
terminal) -- where the gate decides and the trail names you.

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
console -- is [usage/ways-in.md](usage/ways-in.md). An operator who has lost
their own console password is `roster control vouch reset @admin` from a
shell on the box; with one operator there is no other way back. Two rules run over all of
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

**A directory that provisions people suspends them too**, through verbs of its
own -- `HolderService/Deactivate` and `Activate` -- and it is the same suspension:
one `date_disabled`, refused everywhere `Disable` is. What differs is whose it is
(`Holder.directory`). The directory lifts what it put there and nothing else, since
restarting its provisioning says `active` for everybody in scope; an operator's
`Disable` or `Enable` makes a suspension theirs, and somebody the directory deleted
is an operator's to bring back ([scim.md](scim.md)). An erase it does not hear at
all: unassign somebody in the directory before erasing them here, or it can
neither find nor make them until they are forgotten, and makes them again after.

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
  checkpoints:
    key: /run/secrets/roster-checkpoints.pem  # what signs the archive's account; required with an archive
    witnesses: ["log:", "store:/var/lib/roster-witness"]  # who hears each checkpoint outside both
  every: 24h                     # how often the policy is applied
  by:
    holder:
      profile: gdpr              # a holder may be a person, so this is the strict one
    host:
      profile: forever           # a hostname is not personal data
```

`retain` is operational -- what the admin console can show, what a query costs, how big
the disk is. `destroy` is the obligation, normally years the longer of the two.
Between them the row lives in `archive`: a store in that directory with a namespace
per tenant, a gzipped chunk per kind and month, and a namespace of its own for rows two
tenants may both read. A directory a version before this one wrote files into is the
same setting -- the first pass takes the files in.

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
roster trail purge --older-than 61320h --dry-run  # which chunks would go
roster trail verify --full                        # the archive against the database's account of it, and the digest of what it found
roster trail accept --of sha256:… --full --why "a pass the 10-21 crash stopped"  # once somebody has looked at that
roster trail rows                                 # the live rows against what the trail says they were left as, and its digest
roster trail rows --accept --of sha256:… --why "the 10-20 migration set them"  # once somebody has looked at each
roster trail owed                                 # the erasures owed and not paid yet: how many, since when, and what a legal hold keeps
```

`prune` with no window applies **the deployment's own policy**, which is what
somebody putting it in cron means; `--older-than` makes it a manual act instead,
and a manual window does not consult `by:`, so `--older-than 1ns` with no `--kind`
reaches the kinds the policy keeps forever. It writes each chunk whole **before** it
deletes anything, and deletes the rows that are in the chunk rather than re-running
the query -- so the one failure it can leave is rows in both places, which is the
direction to fail in. Nothing takes a lock, so `read` reads the archive as a whole
(`--in`, or the files named on the command line) and a row two runs both archived is
dropped by whichever read sees both. `read` opens no database, which is the point
of keeping the archive. `purge` destroys by chunk and never by row, and there is
nothing after it.

**The database keeps an account of what the archive holds**, written before every
change to it, and what a pass and `purge` destroy is decided by that account: by
the month it recorded for a chunk rather than the labels on the chunk, and never a
chunk it does not account for, which is left where it is and logged. So `purge`
opens the database, where `read` still does not -- and `read` checks nothing, which
is what `verify` is for. It compares the archive with the account and with the
latest checkpoint, `--full` reads every chunk whole, and it fails when it found
something, which makes it the one to schedule. What it finds is a reason to look;
`accept` is somebody who has, saying the archive is right -- a chunk restored from
a backup, a pass a crash stopped. Every pass ends with a checkpoint of the
account: keep them apart from the archive, `audit.checkpoints.dir` on a disk that
refuses deletion.

**An acceptance is of what was looked at** (payday#115). `verify` prints, on
stderr, the digest of what it found and the latest acceptances the checkpoints
keep, newest first:

```
digest sha256:…
accepted at checkpoint 41, 2026-10-21T03:12:09Z, by ops@bastion: a pass the 10-21 crash stopped (unaccounted 2)
```

`accept` takes that digest back, `--of`, and verifies again -- `--full` when the
one looked at was -- and is refused when that finds anything else: a finding
that turned up after somebody looked is not taken in beside the ones they
did. Run `verify` again, look at what it finds now, and accept its digest. Who
accepts is `--by`, the user and host of the shell when it does not say, and
`--why` is required. Both are kept where the archive's own word is: in the
checkpoint the acceptance ends in, which every checkpoint after it names, so
that what was accepted is held to the key as the archive is, and in a receipt
of its own (`accept`). An acceptance nobody can explain later is a finding
hidden, so it says who, why and of what.

**An archive is kept with a key** that signs its account and its checkpoints
(payday#115): `audit.checkpoints.key`, a file or the PEM document itself in
`ROSTER_AUDIT_CHECKPOINTS_KEY`. `serve` and every `trail` command refuse an
`audit.archive` without one, and the refusal says how to make one:

```sh
openssl genpkey -algorithm ed25519 -out checkpoints.pem   # mounted as a secret, apart from the database's and the archive's credentials
```

There is no key everybody knows to fall back on: one would sign forgeries for
anybody the day it reached a deployment. For a deployment being tried, `roster init`
gives a configuration that names an archive and no key one of its own,
`<archive>-checkpoints.pem` beside the archive, and prints where; name it before
`serve`. `.gitignore` keeps it out of a commit, since a key that signs is never
committed.

The same key signs **every row of the account** as it is written, and each
checkpoint names the one before it (lesomnus/payday#58), so a row changed
after the latest checkpoint is found too. Every act ends in a checkpoint of its
own, and while the rows do not add up to the latest one nothing is checkpointed:
every act still does what it does and then fails at the checkpoint after it, and
says so, until `accept` (payday#66). `verify` is clean as soon as `accept` is
done.

**The live rows can be checked too** (payday#102). With `audit.digest.key`, every
row of the trail keeps a keyed digest of the row its write left, bound to the
row of the trail that keeps it (payday#115). The key is a file of `openssl rand
-base64 32` mounted as a secret, or `base64:` and the key in
`ROSTER_AUDIT_DIGEST_KEY`, and keys rotated out go in `audit.digest.previous`.

`trail rows` digests every row again -- live, or erased and not yet forgotten --
and compares it with the newest row of the trail about it: in the database, or
in the archive once `prune` has moved it there, read through the archive's
account as `read` reads it. Then it goes the other way, over everything the
trail is about that is no row. It fails on any finding, and prints each:

- **differs**: a row somebody changed in the database past the servers, or
  brought back from an erase;
- **forged**: a row whose newest line of the trail keeps a digest that is not
  that line's -- copied onto it from another, or not one roster made;
- **no-digest**: a row whose newest line keeps none, though it was written
  after digests began;
- **not-on-trail**: a row nothing on the trail is about -- one somebody added in
  the database past the servers;
- **key-not-held**: a row digested under a key `trail rows` was not given, and
  `unheld-key` names the key;
- **missing**: a row the trail says is there and is not -- one somebody removed
  in the database past the servers.

A check run with a key other than the one `serve` writes with, or against a
deployment that writes with none, finds every row, rather than passing. Two
things it may count instead, and only because the deployment says so beside
the key:

- **`audit.digest.since`** is when the deployment began keeping digests, RFC
  3339 or a date (`ROSTER_AUDIT_DIGEST_SINCE`). A row of the trail written before
  it with no digest is counted as from before, not found. A deployment that had
  a key from its first write leaves it out. One that was given its key later
  sets it to when -- the earliest row of the trail that keeps a digest is no
  later, and a since later than that is refused by the check -- and takes it
  out once those rows are accepted, since a row made to look as old as that is
  counted with them.
- **`audit.digest.retired`** is the fingerprints of keys let go, as
  `unheld-key` prints them: what was digested under one is counted, not found.

**What a check covers is every field** (payday#115). The digest covers every
field but the ones declared personal -- a credential's verifier and a sign-in
binding among them: a keyed MAC of a secret is no copy of it, and no way to test
a guess at it without the key. A line of the trail is copied to the archive and
kept for years, past the row and past the person, so it keeps no MAC of what
is personal; that is **the row's seal**, a MAC of the whole row kept beside it
in the database alone (`rowseal`), for exactly as long as the row: written in
the transaction of every write that leaves it, removed with it, and never on the
trail or in the archive. Its key is derived from `audit.digest.key`, so there
is nothing more to configure or rotate. A personal field changed past the
servers **differs**, and so does a row whose seal was taken off; a seal put back
from an earlier write is **forged**. A person forgotten leaves a seal that says
so and nothing of what they were, and the check counts the row as **forgotten**
and vouches for what its digest covers, until the next write seals it again.

It prints the kinds kept off the trail (`not checked:`) with why, every run.
What roster itself writes or removes past its servers is on the trail as
that act, so it is no finding: a tenant destroyed and what went with it, a
person forgotten and their rows, a person brought back, and what the sweeps
collect -- a session, an attempt, a link, a delegation or a claim past its
expiry. Before a pass destroys the newest row of the trail about a row that is
still there, it carries that row's digest forward, so a row nobody wrote since
its window is still checked.

A row a migration rewrote is a finding until somebody has looked. `trail rows`
prints, on stderr, the digest of what it found, and `--accept --of <that digest>
--why` checks again and puts what it found on the trail as accepted, one row
each -- gone ones as gone -- and is refused when the check now finds anything
else: a row somebody changed after the check that was looked at is not accepted
with the ones that were (payday#115). Each row is on the trail as the act it is
(`roster.trail.rows.accept`), and the digest and the why are logged with it. A
forged row and a key not given are not accepted: look at the row of the trail,
or give the key. A deployment upgrading to this runs it once, for erasures
recorded before erasures kept a digest.

**Once, after upgrading to a roster with seals.** Every row of a kind with a
personal field -- a holder, an address, an identity -- was written by a build
that sealed nothing, and a check counts it **from before seals**: no finding,
since nothing can be said of what it was, and nothing vouches for its personal
fields either. `trail rows` says how many, every run, and an acceptance of that
check is what seals them -- run when it found nothing else, since rows from
before seals are no finding:

```sh
roster trail rows                                                   # `1520 row(s) from before seals: ...`, and its digest
roster trail rows --accept --of sha256:… --why "sealing what was written before seals"
roster trail rows                                                   # no rows from before seals
```

Each is on the trail as the act it is, and the acceptance is of what was looked
at, as every one is: the digest names the rows it seals, and a row changed
after the check is refused with it. Until it runs, a personal field changed
past the servers in one of those rows is not found. Nothing else changes on the
upgrade: a line written before it says which fields its digest covered, and is
checked over those.

**A database restored from a backup** older than the latest checkpoint has
numbered fewer checkpoints than the store keeps: `verify` finds it `behind`
(and `unaccounted`, the chunks written since the backup), and nothing is
checkpointed either. Its own latest checkpoints are from before the backup, so
a chunk taken away since would look like one the backup never had -- and so
would one somebody took away and then hid by rolling the database back. So a
database behind the store is also checked against the newest checkpoint the
deployment vouches for: each chunk that one says the archive held, and it holds
no more and the database has no row of, is `lost`. Look at every `lost` and
every `missing` before accepting: the restore accounts for what was written
since the backup, and a chunk gone since is either one a pass destroyed at the
end of its window, which the deployment's log says, or one somebody took.

Once somebody has looked, `accept` takes it on only as the restore it is:
`--restored` names the checkpoint the backup holds, which is the number `behind`
says the database has numbered (0 for a backup from before the first), and an
acceptance with none, with another number, or of a database that is not behind
is refused. Its checkpoint is numbered after the store's newest and names it as
the one before, so the checkpoints from after the backup stay what the archive
held (payday#70).

```sh
roster trail verify --full   # `behind` (the database has numbered 38 checkpoints, and the store keeps one numbered 41), `unaccounted`, `lost`
roster trail accept --of sha256:… --full --restored 38 --why "the database restored from the backup of 10-01; each lost chunk looked at"
roster trail verify --full   # clean
```

A row with no signature is the same whether it was written before rows were
signed or had its signature taken off, so with a key every such row is a finding.
A deployment that already had a key when it moved to a roster with signed rows
runs `accept` **once** after the upgrade, as somebody who has looked:

```sh
roster trail verify                                                         # `unsigned`, for every row from before
roster trail accept --of sha256:… --why "written before rows were signed"   # once
roster trail verify                                                         # clean
```

**An archive kept without a key until now** (an earlier roster kept one that
nothing signed) is the same once the key is set, and more of it. Every row and
every checkpoint it has is `unsigned`. Nothing is destroyed on their word, and no
act takes a checkpoint after them: each does what it does and then says so in its
error (`no trusted key signed the checkpoints the store keeps`). Set
`audit.checkpoints.key`, look, and run `accept`. It signs the rows as they
stand and names the checkpoint the database numbered last as the one before its
own, which takes in the history from before the key. Signing the rows brings
their chunks into the comparison, and what that finds -- a chunk a row nobody
signed said the archive held, and it does not -- is not what was looked at:
the next `verify` finds it, and it takes an acceptance of its own. So such an
archive may take two:

```sh
roster trail verify                                                         # `unsigned`, every row and checkpoint
roster trail accept --of sha256:… --why "kept without a key until 10-22"
roster trail verify                                                         # clean, or what the signed rows brought to light
roster trail accept --of sha256:… --why "the chunks the 10-22 acceptance brought to light, looked at"   # only if it was not
```

A deployment that changed its key without trusting the old one is told the same.
It puts the old key's public half in `audit.checkpoints.trust` instead of
accepting.

`prune --to`, and `--in` of `purge` and `accept`, name the archive
`audit.archive` already is, or one for a deployment that names none. The account
is of one archive: rows written into a second would be rows the first is found to
be missing, and an acceptance or a purge read against a second would take every
chunk of the first for missing. Each is refused.

A **legal hold** is the one thing a manual window does answer to: what one is on is
left by `prune --discard` and by `purge`, which say how much they kept and go ahead
for everything else. A hold is a tenant's: an operator places one with `roster hold
place --why … @tenant` (§ "A tenant leaving"), and the trail keeps what it holds of
that tenant from then until it is lifted.

### Who heard the newest checkpoint

A checkpoint names the one before it, so the chain below the newest is checked.
The newest itself is known only to the database's count and to the checkpoint
store, and whoever can write the database and take objects out of the store can
cut the chain short there: take the newest checkpoints away, wind the count back,
and the next act numbers past what is left. Nothing in the two says so. A
**witness** is somewhere neither reaches that heard each checkpoint as it was
taken (payday#115):

```yaml
audit:
  checkpoints:
    witnesses: ["log:", "store:/var/lib/roster-witness"]
```

- **`store:<dir>`** keeps every checkpoint it hears, signed as it was, in a
  directory of its own, and answers a verification. Its worth is where it is: a
  volume of its own, mounted with credentials neither the database's nor the
  archive's, and best on storage that refuses deletion -- a `store:` on the disk
  the archive is on is no witness, since whoever takes checkpoints out of the one
  takes them out of the other.
- **`log:`** writes a line for every checkpoint, `trail: a checkpoint, heard`
  with `k`, `digest` and `prev`, and records the gauge `payday.trail.checkpoint`
  at its number. It answers nothing: the monitoring outside is the witness, and
  it is one only with the alerts below on it. It needs no volume, which makes it
  the one a box with a single disk can name.

A witness that cannot hear stops nothing: the act says so in the log, and the
next pass tells it again, oldest first, with every checkpoint taken before it
was named -- a hundred a pass. `serve` and the `trail` commands refuse a scheme
this binary does not open, a witness named twice, and witnesses with no archive
to hear. `deploy/`'s `trail` component and `compose.yaml` name both, each store
on a volume of its own; `deploy/`'s base and `roster.yaml` name none, because
they keep no archive (the component and the file say why).

`verify` asks each witness what it heard last, holds the chain to every one that
answers, and prints on stderr what each did:

```
heard by store:/var/lib/roster-witness: checkpoint 41, sha256:…, at 2026-10-22T03:12:09Z
```

No act takes a checkpoint under a number a witness has heard and the database
has not numbered, since that is the count wound back and the number about to be
taken again; an acceptance is numbered past every number a witness heard, and a
database behind one is accepted only as a restore (`--restored`), as one behind
the store is. What a verification can find of them, and what to do about each:

- **`unkept` -- "the chain was cut short there"**: a witness heard a checkpoint
  past the newest the store keeps. Somebody took the newest checkpoints away, or
  the store or the database came back from an older copy. Look before anything
  else -- while the database is behind what a witness heard, no act takes a
  checkpoint, and each says so in its error. If the checkpoint store is on
  storage that keeps versions, the checkpoints are still there behind a delete
  marker: recover them and verify again. If the database or the store was
  restored from a backup and the deployment's own records say so, look at every
  `lost` and `missing` and accept it as the restore it is -- `--restored` when
  `verify` says it is the database that is behind. If neither, it is an
  incident: keep the witness's store and the logs as they are, which are what
  the chain said, take the credentials that reach the database and the archive
  away from whoever had them, and accept only once it is understood, saying what
  happened in `--why`. An `unkept` below the newest is a checkpoint taken out of
  the middle, and is the same.
- **`rewritten` -- "heard it as …, and again as …: the count was wound back and
  the number taken again"**: a witness heard one number as two statements. Every
  witness was away, or kept from hearing, while the newest checkpoints were
  taken away and numbered again -- told all of it once it was back, a witness's
  newest is the deployment's newest, so only the pairs below the head say it.
  Each pair is a finding of its own until an acceptance takes it in, and an
  acceptance takes in only the pairs it was shown: one wound back after it is
  found after it. Find out when the witnesses could not hear (the log says, at
  every act that told one) and what took the numbers away -- a restore is the
  answer that needs no incident -- then accept the pairs, naming it.
  `rewritten` is also a checkpoint the store keeps as other than a witness heard
  it, which is a checkpoint written over: an incident, as above.
- **`unheard`**: the newest checkpoint the store keeps is older than
  `audit.checkpoints.hear_within` (twice `audit.every` when it is empty), and no
  witness that answers heard it. Either the witnesses cannot hear -- the log says
  which and why at every act: an unmounted volume, a full disk, credentials gone
  -- or somebody is keeping them from hearing so that a cut would go unseen.
  Mend the witness; the next pass tells it everything it missed, and the finding
  is gone once it has heard the newest. There is nothing to accept. A run before
  the first checkpoint is heard says a witness heard `nothing yet`, which is no
  finding.
- **`forged` -- "witness … answers that it heard it, and its signature is not
  good"**: what a witness answered is not what the deployment signed, so
  somebody wrote to the witness. The witness's storage is meant to be written by
  roster alone: find who else can, take that away, and put the store back from
  its storage's own versions. Until then the chain is not held to that witness,
  and the others still are.

**And the alerts, which make `log:` a witness.** Wherever the deployment's logs
and metrics go (`otel:`, § "What it prints"), alert on two things, and a third
for a deployment that takes a checkpoint at least every pass:

- the gauge going down -- `payday_trail_checkpoint <
  max_over_time(payday_trail_checkpoint[30d])`, or that system's spelling of it:
  a count wound back;
- the logs holding two lines `trail: a checkpoint, heard` of one `k` with two
  `digest`s: a number taken again;
- and the gauge not moving for longer than two passes: a deployment whose
  witnesses were stopped from hearing.

Without them `log:` tells nobody. `store:` needs none, since `verify` asks it.

### Who read whose data

Beside the trail, and kept by it: payday's access record (payday#111). The trail
is what was **written**; this is who **saw** what -- a row for each call that
read, wrote or carried somebody's personal data, naming who asked, from where,
the RPC, whose rows, and how it ended. What an operator reads of a customer's
people through `admin.addr` is filed under that customer and names the operator.
A read's row is written a second after it, by a loop every process runs, and the
loop writes what it holds once more as the process stops; a process killed
outright loses its last second.

It is a kind of the trail's, `access`, so the same passes keep it, on a window of
its own:

```yaml
audit:
  by:
    access:
      profile: pipa-sensitive   # two years
```

Nothing serves it yet: `AccessService` is registered on no port, and `roster
trail read` passes over its rows, so for now it is read in the database.

### A key that can read the trail can read everything

A deployment key is the **deployment's**, and unnarrowed it is every tenant in it
-- the wall narrows nothing for it. `Audit.patch` is what each write set -- and
`Audit.value` the row it left, for a kind kept at `audit.content: value` -- so one
method answers every table's contents, in every tenant, across all time, including
rows long since deleted. What it does not answer is what the schema declared
personal or secret: a person's names, addresses and identities are on the trail as
having been set, and never as what to (payday#92). It is the single widest read this deployment has, and
`roster control key add` says so when a key's methods reach it. No **role** reaches
it that way, because a holder is walled to their own tenant -- and a deployment key
narrowed with `roster-at` is answered as one of those.

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
keeps their alias, name and profile, and their addresses and identities keep
theirs. The trail holds that each was set and when, and not what to -- they are
declared personal (payday#92), so the row is the only copy. That is right for
*this person has left*.
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
the rows that say what they may do, and any app's nomination to act as them. The `Holder` row **stays, blank**: its
identifier is `Audit.actor_id` and thirteen foreign keys point at it, and what makes
it personal data is that it *resolves*. Emptied, it is a stable pseudonym reaching
nothing.

The trail keeps its **events** and loses its **contents**, in the database and in
the archive both -- the actor, the action, the object and the time stay; `value`
and `patch` go. Most of what they held of the person was never there: a field
declared personal is on the trail as a value at no level. What `forget` blanks is
the rest -- what was written about them in free text, and rows written before
payday#92. Both halves matter and they pull against each other: a version that
destroyed the rows would let somebody erase the evidence of what was done *to*
them by asking to be forgotten. The archive is reached only if `audit.archive` is
set; archives you keep elsewhere are yours to reach.

**One transaction, and the archive's half owed in it.** The person's rows, the
holder blanked and the trail's rows about them in the database are one transaction,
and the archive, which takes part in no transaction, is *owed* in it (payday#115): a
row of payday's `owed` table for each thing forgotten, which a rollback takes with
the rest. It is paid as soon as the transaction commits; a payment that fails, or a
process that stops before it, leaves the debt to the trail's next pass, which pays
what is owed before it moves anything, and `roster forget` says how much is still
owed. A trail row a legal hold is on stays as it was, and its debt with it:
`roster forget` says how many and whose, and the pass after the hold is lifted erases
them -- nobody runs anything again. `roster trail owed` says what the deployment owes
at any moment: how many, since when, and how many each tenant's hold keeps. The rows'
seals are owed with the archive's half and paid with it (payday#144), so the
transaction takes no turn with a pass.

**What somebody consented to stays past them.** An app's record of their
consents (`ConsentService`, [apps.md](apps.md) § *What an app keeps here*) is
what shows afterwards that they agreed, so neither the erase nor `forget` takes
it: the blank holder is what it points at. It is destroyed `consent.keep` after
the erase, by the deployment and not by any app, with each row on the trail as
destroyed:

```yaml
consent:
  keep: 26280h   # three years after an erase. empty is for good
  every: 24h
```

A tenant an operator holds keeps them as it keeps everything, a restore inside
the window gives them back with the person, and a tenant destroyed takes them
with it.

## A tenant leaving

A person's lifecycle one level up, in four steps (#98):

```sh
roster tenant close @newco                    # nobody of it gets in; nothing is destroyed
roster restore @newco                         # while tenant.restore_within lasts
roster forget @newco                          # destroy it now; it has to be closed first
roster forget                                 # and every one whose grace has run out
roster forget --reuse @newco                  # free its alias now rather than in a year
roster hold place --why "Contoso v. Fabrikam, 2026-123" @newco   # nothing of it is destroyed
roster hold lift @newco
roster hold ls
```

```yaml
tenant:
  restore_within: 720h   # 30 days after a close it is destroyed. empty is never: destroying is by hand
  alias_hold: 8760h      # its alias stays taken a year after. empty is a year
  every: 24h
```

**Close** stamps `Tenant.date_closed` and destroys nothing. From then nobody of the
tenant gets in, on every door at once: `Vouch.Verify` and every other sign-in say
no; a session, a tenant key and a delegation of its people stop working, at roster
and at `Introspect` for the apps in front; a deployment key nominated into it acts
as nobody; and its hosts answer `FrontService.WhoseHost` with *this tenant is
closed*, which is what its people at a bookmark need to read. Apps hear of it on the
sync stream, as an event for each person in it carrying `date_closed`, and the Login
App makes Hydra forget each of them. `TenantService.Close` is the verb -- a role or a
key that names it **through a binding across the tenant** may close a tenant over the
wire, including the tenant's own administrator, who is then locked out with everybody
else; one that names it only in a site or a team is refused, since a close reaches
everybody ([usage/permissions.md](usage/permissions.md) § *A site*). A tenant a file
declared is the file's to close, and the control plane's one tenant cannot be.

**Restore** is what makes the window a grace, as for a person: the tenant comes back
exactly as it was, keys and all, until `restore_within` has run out -- and refused
after it even if the sweep has not got there yet, because what decides is the window
and not when something ran. With no window, a close can be restored until somebody
destroys the tenant.

**Destroy** takes what is left: every person forgotten as `roster forget` forgets
anybody, every other row of the tenant removed -- roles, bindings, groups, sites and
teams and their memberships, connections, mail domains, hosts and their proofs,
nominations -- and the tenant row blank. What stays is its identifier, which the trail
names it by, and the blank holders, for the same reason. The trail keeps its events
and follows its own windows; its people's contents go as each is forgotten. The first
write is `date_destroyed`, so a restore cannot get between, and running it again
finishes one that stopped -- which the sweep does on its own. Each of those writes is
made on what was decided: the stamp only on a tenant still closed and held by nobody,
and a restore only on one not yet stamped, so the two cannot cross.

**A hold stops all of it.** `roster hold place` writes `Tenant.date_held` and what it
is for; while it is there `forget` refuses the tenant, the sweep passes it by, its
people whose own grace runs out are not forgotten on the clock, and the trail keeps
what it holds of it. A hold placed while a tenant is being destroyed stops it at the
next person: those forgotten before it stay forgotten. Lifted, everything carries on
from where it waited. It is a
command and not an RPC on purpose: a tenant's administrator lifting the hold on
their own records is what a hold exists to prevent. Placing and lifting are in the
trail.

**The alias is held.** A name that found somebody still finds them after the rows
are gone -- former people typing it into a sign-in form, bookmarks, mail, a QR
sticker with it printed on -- and a tenant that took it the next day would be that
name to all of them; the sharp case is somebody doing it on purpose. So the
destroyed row keeps it for `alias_hold`, and then it becomes `gone-<identifier>` and
anybody may take it. A restore never meets the hold, because the alias never left.
`--reuse` frees one sooner on purpose, and the trail records that it did. A hostname
is freed at once: taking one again needs proof (#42), which is the rule an alias hold
only approximates.

## What it prints

One line per call, on stderr, pretty-printed: the method, the status, how long it
took, and the trace and span ids when a caller sent some. Health checks are left
out. That is `otel:` with nothing written in it -- write a `logger` provider there
and the same records go wherever it says, with traces and metrics beside them
(`roster config env` for the variables). The trail is not this: the trail is what
was written and by whom, kept; this is what was asked, as it happens. One thing
in it is the trail's to be watched for, with a deployment that keeps an archive:
the checkpoints the `log:` witness tells, and the alerts on them in § "Who heard
the newest checkpoint".

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

**The background loops run in one replica only if you say how.** The sweeps,
the trail's retention, the forget window and the outbox drain are each a loop,
and `spin.lease` says which process runs it -- named, like the broker, and for the
same reason: `local` runs every loop in every process, which is right for one and
silently wrong for two.

```yaml
spin:
  lease: postgres
```

Each loop is then held by a session advisory lock on the database the rows are
in, and runs in whichever replica holds it; when that replica goes, its locks go
with its connection and another takes each loop within a few seconds. A host
that vanishes rather than exits is noticed by TCP keepalives the lease asks for,
within about a minute. Both planes' loops are under the one lease, named
`roster.*` and `roster.control.*`; who holds what is the database's to say:

```sql
SELECT a.pid, a.client_addr, a.backend_start, l.objid
FROM pg_locks l JOIN pg_stat_activity a USING (pid)
WHERE l.locktype = 'advisory' AND l.classid = 1885631344 AND l.objsubid = 2;
```

`ROSTER_SPIN_LEASE` in an environment. Leave `spin.dsn` empty unless the
database is reached through a pooler in transaction mode, which a session lock
does not survive. `none` is a replica that runs no loop at all. What no lease
promises is that a loop never runs twice at once: a replica cut off from the
database goes on for up to a ping, five seconds, after another has taken its
loops. roster's are fine with that. The expiry sweeps delete what is already past
its time, the two forget windows act one person or one tenant at a time and log a
failure and carry on, and payday's drain and retention are written for it. A loop
added here has to be too.

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
  matters. The account app's limits on mail are counted the same way (*The front
  door's mail*, above).
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
link, a delegation. `postgres:18` as it comes is enough.

And under the race detector, for anything two callers can reach at once. The
tests that race callers against each other -- in `cmd` -- check what the database
ends with, and that can be right while the code races on the way there; CI's
`race` job runs them this way on every push:

```sh
PDTEST_POSTGRES=postgres://roster:...@localhost:5432/roster?sslmode=disable \
  go test -race -count=1 -timeout 30m ./cmd/
```

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

roster on Postgres, both planes, both consoles, one customer already stood up
(`contoso`, with `erin` in it), the account app, the directory, and -- for the
shape a deployment with several products has -- Hydra with the Login App beside
it, plus two demo relying parties. The two consoles are the two callers: the
admin console at `:8081` is the operator who runs it, and the user console at
`:8080` is somebody inside `contoso` looking at their own organisation.
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

The trail in that stack is kept as § "The audit trail" describes: an archive on
the state volume, signed with a key of the deployment's own that `init` writes
beside it on the first start -- the entrypoint names it to `serve` and not to
`init` until it is there -- and heard by `log:` and by a store on a volume of its
own (§ "Who heard the newest checkpoint"). `roster trail verify`, run in the
`roster` container, says what the store heard.

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
  password from `Credential.Issue`. The same holds for every link: one that
  confirms an address, and one that deletes an account (`Vouch.LeaveLink`), are
  minted here and delivered by a front door -- the account app's `Config.Mail`,
  which `roster account serve` does not fill, so the front door as shipped
  sends none of them and says so.
- **Nothing here signs a token.** If several products need one sign-in, that is
  Hydra in front and roster answering it: [login.md](login.md). Do not reach for a
  JWT minted here -- [position.md](position.md) § "The line, in one sentence" is
  why.
