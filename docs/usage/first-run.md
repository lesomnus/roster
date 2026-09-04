# First run

What to type to get a roster that answers, and what exists when you have.

## The configuration file

`roster.yaml` beside the binary, or `--config <path>`. The shipped one is a
working single-machine deployment on SQLite and needs no edits to try.

Four blocks matter on a first run:

```yaml
db:                       # the data plane: customers, and their people
  driver: sqlite3
  dsn: "file:roster.db?_pragma=foreign_keys(1)"

server:                   # where product apps call
  addr: ":50051"
  http: { addr: ":8080", allow_web: true }

control:                  # who may call this deployment, and who runs it
  db:
    driver: sqlite3
    dsn: "file:roster-control.db?_pragma=foreign_keys(1)"
  addr: "127.0.0.1:50052"
  http: { addr: "127.0.0.1:8082", allow_web: true }

admin:                    # where an operator administers customers
  addr: "127.0.0.1:50053"
  http: { addr: "127.0.0.1:8081", allow_web: true }

watch:
  broker: memory          # named rather than defaulted; see below
```

**Two databases, deliberately.** A key must not live in the tables it protects,
so the control plane is a database and not a reserved tenant, and there is no
query from one to the other.

`control` is not optional: `roster init` refuses a configuration without it. See
[`../operating.md`](../operating.md) § the two planes for why adding one later
is not a migration.

`watch.broker` is the one setting with no default. `memory` publishes inside
this process, which is right for one replica and silently wrong for two; a
deployment of two needs `postgres`, which is `LISTEN`/`NOTIFY` on the database
the rows are already in.

Anything in the file can be overridden from the environment — `ROSTER_DB_DSN`
beats what is written. `roster config` prints what came out and
`roster config env` lists every variable there is.

## init

```sh
roster init
```

```
control plane
  holder ops is 019ff2...
  bound to role "everything" = /roster.*/* -- every RPC roster serves, now and after an upgrade
  password  kQ9x...

sign in to the console as ops. that password is shown once and is not stored -- write it down now.

there are no customers yet, which is the right state to start in.
```

It writes **one person**: the operator who runs this deployment, in the control
plane, and the role that lets them. Nothing in the data plane at all.

The password is shown once and stored as an argon2id hash. There is no
`--password` flag and there will not be — an argument is in the shell history
and in the process list — but `--password-stdin` reads one from a pipe, which is
what a container's entrypoint uses.

`--operator <alias>` names them something other than `ops`.

**Running it twice is an error**, not a no-op. An `init` that quietly did
nothing is one somebody runs against the wrong deployment and believes.

### Why this cannot be an RPC

A tenant is not put up from inside one, so the first row of a deployment has
nowhere to arrive from. What writes it is a server instance this process holds —
not a privilege anybody has, but wiring that no request can reach.

### And why there are no customers

A tenant *is* a customer. One written by a command is a customer nobody asked
for, and it would be the same fake company in every deployment. Making one is
the first thing you do next, and it is the same thing you will do for the
hundredth: [customers.md](customers.md).

## serve

```sh
roster serve
```

Five listeners at most, and which ones open is what the configuration named:

| | | |
| --- | --- | --- |
| `server.addr` | product apps | gRPC, walled and gated |
| `server.http.addr` | anything that cannot speak gRPC | the same, transcoded |
| `control.addr` | a console | the deployment's own rows: services, keys, operators |
| `control.http.addr` | a console in a browser | the same, transcoded |
| `admin.addr` / `.http.addr` | a console | **customers**, with no wall, behind an operator session |

`db.migrate` is off by default, and `serve` refuses a database that is not the
shape the schema describes rather than starting and failing per request. `init`
creates that shape, so a fresh deployment needs nothing else.

## Checking it

```sh
roster config              # what this deployment is configured with
roster tenant ls           # nothing yet, and that is correct
```

`roster tenant ls` prints a line on stderr saying it is reading the database
directly. That is the local CLI telling you which of its two modes it is in.

## The CLI's other mode, which is the one most people are in

Every entity command runs locally by default: it opens the database in `db` and
reads it with no wall, no gate and no rules. That is right for a shell on the
box and wrong for anything else.

Name an address and the same commands become an ordinary caller — the wall
narrows what comes back, the gate decides what is allowed, and the credential
says who is asking:

```yaml
client:
  addr: "dns:///roster.internal:50051"
  insecure: true              # for a network only this deployment is on
  auth:
    scheme: bearer
    credential_file: /run/secrets/roster-key
```

**This is not only for operators.** A customer's own person runs the same binary
against the same address with their own `rt_`, and their configuration has no
`db:` block at all — there is nothing for them to open:

```yaml
client:
  addr: "roster.internal:50051"
  auth: { scheme: bearer, credential_file: ~/.roster/key }
```

```sh
roster holder ls          # the people in their tenant
roster me get             # who this credential is, and what it may call
```

`--HAL` on the root forces the local one whatever the file says, for somebody
with a shell who wants to look at the rows under a deployment configured for the
wire.

Naming `auth` with no `addr` is refused: a credential with nowhere to send it
would otherwise read the database directly while you believed you were calling a
server.

`roster init`, `roster key add` and `roster vouch reset|set|unlock` have **no
remote form**: what they write is not served, which is the whole reason they are
commands. The rest of `roster vouch` (the sign-in surface -- `verify`,
`delegate`, `link` and the others) and `roster issue` are the other way round,
**remote only**, because those are a caller's calls and a local run has none.

## Next

[customers.md](customers.md) — a tenant and the first person in it.
