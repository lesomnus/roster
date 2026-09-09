# roster

**The store that answers who somebody is.** People, the external identities they
sign in with, the addresses they use, and the organisations, sites and teams
they belong to -- held here, in our schema, so that changing identity provider
is changing a login screen rather than changing the record of who works here.

It is one layer of an identity system and not the whole of one: the protocol is
[Ory Hydra](https://www.ory.sh/hydra/)'s and the login flow is a Login App's.
roster owns the records they both ask about, and owns `sub`.

The line, so that it is on the first screen:

> **roster stores facts and verifies claims about them. It never issues anything
> a third party verifies.**

So it checks a password and a second factor, and it does not run a login flow,
mint a session for somebody else's browser, or sign a token another system
verifies alone. [docs/position.md](docs/position.md) is that applied, including
why it is worded as a test rather than as a list.

| | |
| --- | --- |
| [docs/position.md](docs/position.md) | what roster is, and **where it stops** |
| [docs/entity.md](docs/entity.md) | the twenty-three tables, drawn, with a paragraph each |
| [docs/operating.md](docs/operating.md) | running one: keys, roles, TLS |
| [docs/login.md](docs/login.md) | what happens when somebody signs in |
| [docs/roadmap.md](docs/roadmap.md) | what is being built next, in order, and how far it has got |

## What it does

- **Verifies a password** without handing the hash out -- argon2id, timing-safe,
  with attempt counting and a lockout, all in the one place that holds the row.
- **Answers to API keys.** A second roster runs in the same process on its own
  database, holding the deployment's own services and what each may call.
- **Roles bound at a scope**, in the shape Kubernetes settled on: a `Site` is a
  namespace, a role with no site is a `ClusterRole`, and nobody may grant what
  they do not hold.
- **`/me`** -- who the caller is and every RPC they may call, in one round trip,
  from the same union the server enforces.
- **Answers about a token it issued.** `TokenService/Introspect`, so a product
  app handed an `rt_` key learns which person it stands for and what it was
  narrowed to. Opaque on purpose: revoking is a delete, and it works now.

It is also the second app [payday](https://github.com/lesomnus/payday) is tried
against, and the more demanding one.

## Built with payday

The generated messages live in `rstr/`, which is why the binary is in
`cmd/roster/` rather than beside them: an app is **one** Go package for
everything generated -- the ent schemas of two packages cannot have an edge
between them, and the tenant wall is an edge.

To put them somewhere else, change `option go_package` in `proto/app/*.proto`
and regenerate. It is read rather than configured, so there is one place that
says it and it is a place you own:

```proto
option go_package = "github.com/lesomnus/roster/api";   // -> api/thing.pb.go, api.Thing
```

Everything else stays where it is -- `internal/ent`, `server/bare`, `server/pd`
are named from the module root, not from the messages. Every entity has to say
the same one, and `pd gen` refuses rather than generating an app whose wall has
no edge to stand on. What it will not do is rewrite the Go you wrote: the
imports in `cmd/` are yours, and moving the package is a compile error until you
follow it.

Two names in that arrangement are choices worth knowing the reason for. The Go
package is `rstr` and not `api`, because these messages are meant to be
**imported by other apps** -- `api` is what every app calls its own generated
package, and a product importing roster's would be aliasing one of the two on
every file that mentions both. And the proto package is `roster` and not `app`:
protobuf's file registry and payday's `pdid` domains are per **process**, so
two payday apps can be linked into one binary only if their proto packages
differ -- which the reference app does, and which a product app embedding
roster's login flow will do too.

## What is where

Two rules, and between them they say whether a file is yours.

| | |
| --- | --- |
| `proto/app/*.proto` | **yours** -- the entities, and any service you write |
| `proto/ext/**` | **yours** -- overlays, merged into a generated contract |
| `proto/**/*_svc.g.proto` | generated: the contract of an entity |
| `proto/roster/payday/` | generated in whole; payday's own entities, copied in |
| `internal/ent/`, `server/bare/`, `server/pd/` | generated |
| `*.g.go`, `*.pb.go` | generated, wherever `go_package` puts them |
| `ts/gen/` | generated in whole |
| everything else | yours |

So: **`.g` means a generator wrote it**, and `proto/roster/payday/` is the one
directory where that is true of every file rather than of the ones marked.
Nothing else needs remembering; `pd gen` rewrites all of it and `pd gen --check`
says when a commit did not carry it.

The `.g` stops at the schema on purpose. A contract is `thing_svc.g.proto` and
the Go generated from it is `thing_svc.pb.go`, because by then the marker is
true of everything in sight and says nothing.

## What to read first

`cmd/serve.go`. It is the stack written out -- which layers, in which order, and
which server the wall is on -- and it is deliberately not hidden behind a
`payday.Serve(cfg)`. Everything else in `cmd/` is small.

`cli/` is the other half of the same program: the commands, the database engines
a process opens, and ent's migration engine. It is a package rather than a build
tag because the linker follows imports and `cmd` is what the Wasm sandbox
imports -- what is in `cli` is thirty-five megabytes the browser would otherwise
download to run a database that is already in the page. `cli/cli.go` says it at
length.

`proto/app/identity.proto` is the other half. The `(payday.entity)` option at the
bottom of it is where the domain byte, the tenant wall, the `List` and the
`Watch` all come from.

## Generating

```sh
go tool pd gen .          # messages, servers, ent schema, layers
go tool pd gen --ts .     # and the TypeScript half
go tool pd doctor .       # what would go wrong before it does
```

`pd gen --check` regenerates and fails if anything moved, which is what CI runs:
a generated file that was not regenerated compiles perfectly and is wrong.

`pd gen` also pins the buf dependencies the first time, and that is not a
convenience. `buf dep update` compiles the workspace before it writes the lock,
and this app's schema names `Tenant` — which does not exist until a
generation has copied payday's entities into `proto/roster/payday/`. So run by hand it
fails on a tree nothing has generated yet, and there is exactly one moment it
can run: inside `pd gen`, between those two things. Once `buf.lock` has entries
in it they are yours, and nothing here moves them.

## Upgrading payday

```sh
go get -u github.com/lesomnus/payday
go tool pd gen .
```

payday owns some of this app's schema — `Tenant`, `Holder`, `Audit`, `Outbox` —
so a field added to one of them there arrives in `internal/ent` here the next
time you generate. **Nothing about that is loud on its own.** It compiles, the
tests pass against a database the tests just created, and the first sign of
trouble is a column that is not there in the one handler that reads it.

So two things refuse rather than trusting anyone to remember:

- **`pd gen` that did not happen.** The generated `server/pd/pd.g.go` carries
  the payday it came out of, and `pd.NewSink` refuses a binary linking a
  different one. `pd gen --check` fails on it too, which is what turns "we
  upgraded and forgot" into a red build rather than a strange afternoon.
- **the migration that did not happen.** `serve` looks at the database before it
  answers anything and refuses one that is not the shape `internal/ent`
  describes, printing the SQL that is missing. Writing that migration is yours —
  payday's entities and yours are in one database and one ent client, so payday
  cannot own it.

Neither says anything when it cannot: a `replace` to a checkout, a workspace, a
build with no version — all of those are somebody developing, and a guard that
refused them would be a guard nobody could work under.

While developing, `db.migrate: true` swaps the second refusal for
`ent.Schema.Create`. That is a decision about who may alter tables and it should
be made on purpose, not left on.

## Adding an entity

```sh
go tool pd entity add --tenanted --watch Widget .
go tool pd entity list .
```

It picks a domain nothing else has and writes the tenancy out, which are the two
things that are cheap to get wrong here and expensive to find later.

## Upgrading a deployment

There is no `migrations/` directory: `db.migrate: true` lets ent bring the
database to the shape the schema says, and without it `serve` refuses to start
until somebody has. What a change means for a running deployment is written
beside the change -- the proto comment, the roadmap row -- and not in a log.

## Running

```sh
go run ./cmd/roster init          # the operator who runs this deployment
go run ./cmd/roster serve

go run ./cmd/roster config        # what this deployment is configured with
go run ./cmd/roster config env    # every variable it can be told through
```

Or the whole thing on Postgres with a customer already in it, which is what
`compose.yaml` is: `docker compose up --build`, the console at
`http://localhost:8082/` and the account app at `:8090`.
`docs/operating.md` § "Locally, in one command" says what comes up.

`init` writes the **operator** and nothing else, so a fresh deployment has no
customers. The first one is four local writes and a key, from a terminal:

```sh
roster tenant add @newco
roster holder add @newco/admin
roster role   add @newco/everything '{"methods":["/roster.*/*"]}'
echo '{"role":  {"slug":{"alias":"everything","tenant":{"alias":"newco"}}},
       "holder":{"slug": {"alias":"admin",     "tenant":{"alias":"newco"}}}}' \
  | roster binding add -

roster key add --tenant newco --holder admin --allow '/roster.*/*'
```

The key is printed once, resolves to that person, and is walled to their tenant.
For a person rather than a machine, `roster vouch reset @newco/admin` prints a
password instead.

**[docs/usage/](docs/usage/)** is this at length: what to type, page by page,
with a [tutorial](docs/usage/tutorial.md) that goes from an empty directory to a
person signing in. `docs/operating.md` is the operator's version — ports, the
console, the trail, retention, upgrades.

`init` is there because there is nowhere else it could be. A tenant is not put
up from inside one, so the first row of a deployment cannot arrive over the API
— what puts it there is `Server.Ungated`, which is not a privilege anybody
holds but a server instance this process was handed. Running it twice is an
error rather than a no-op, because an `init` that quietly did nothing is one
somebody runs against the wrong deployment and believes.

A customer is nobody's to seed. `init` writes no tenant, because a tenant is a
customer and one written by a command is a customer nobody asked for — so the
first act after `init` is the same act as the hundredth, whether it is typed at
a shell or done from a console.

It needs a `control:` database, and that is the one thing it refuses over.
Without one a deployment serves `auth.Plain` — every caller is whoever they type
— and adding a control plane afterwards is not a migration: an `rt_` minted
while nobody was checking sits inert in the data plane until the day something
reads it, and then all of them work at once. `cmd.Seed` is not asked this, which
is where a test and the Wasm sandbox live.

`key` and `trail` are there for the same kind of reason and it is worth knowing
which kind. `roster key` writes to the control plane, which is not served.
`roster trail` applies the retention policy, and no server offers those acts at
all: `AuditService` refuses every write, because what a trail is worth is that
the credential which lets somebody act is not the credential that lets them
erase the record of having acted. Both need the database, which is the boundary
being asked for. The policy itself is payday's — `config.AuditConfig` and
payday's `trail` — because the trail is payday's table and every app on it has
the same problem. See `docs/operating.md`.

The configuration is a file, then the environment over the top of it. It is read
on the **root** command, so it has happened whichever subcommand runs, and
`--config` names a file when the default is not wanted — which is how one app
runs as two deployments. What an app can be told is what `cmd/config.go`
declares; nothing is documented separately, because a documented list goes out
of date on the commit that adds a field.

## The page

```sh
go tool pd gen --ts .
cd ts && npm install && npm run dev
```

`ts/` is a React app, and it is small enough to read in one sitting. What is
worth reading is what is **not** in it: nothing declares which query a write
invalidates, nothing pushes a new row into a list, and nothing tells the tenant
shown next to a row that it is the same tenant shown at the top of the page.

That falls out of the reads going through the framework. `useQuery` makes the
call, so the store knows which rows were drawn; when one of them changes —
from a write here, from another query, from a `Watch` the server is streaming —
everything drawing it re-renders, at once, with nothing joining them up.
`useCall` is the same trick backwards: the row a write answered with goes into
the store, and the lists over that entity are read again, because a create can
change what belongs in one and only the server knows which.

The store is opened for a **credential** and mirrored to IndexedDB under it, so
a reload draws the page it had rather than a spinner for it, and signing out
takes the copy with it. `ts/lib/store.ts` is the whole of that, and deleting two
lines of it turns the mirror off.

React is a **peer** dependency of payday and an optional one. `payday/store` and
`payday/query` know nothing about it; `payday/react` is thirty lines of
`useSyncExternalStore` over them, and the same file for Vue or Svelte is the
same length.

## A browser

gRPC is not a protocol a page can speak — not a library that is missing, frames
the platform does not let anything write — so `roster.yaml` has a second
listener in it:

```yaml
server:
  http:
    addr: ":8080"
    allow_web: true
    origins: ["http://localhost:5173"]
```

What answers there is the **same** server: the same interceptors, the same
credential, the same wall, speaking Connect and gRPC-Web instead. A Connect call
is a POST with a JSON body, so it is also what to reach for from a shell:

```sh
curl -sX POST http://localhost:8080/roster.ThingService/List \
  -H 'Content-Type: application/json' -H 'Connect-Protocol-Version: 1' -d '{}'
```

Under TLS it carries **native gRPC** as well — HTTP/2 arrives by ALPN and a
`grpcurl` reaches it. The two listeners are a decision about the transport gRPC
brings, not about what is reachable where.

Whatever else this app serves over HTTP goes on the same mux, in `serve.go` —
the built console page at `/`, and nothing else. **The sign-in is not a route.**

It was one: `POST /session` and `DELETE /session`, mounted on every listener
that had HTTP, on the reasoning that issuing a credential is HTTP because `auth`
reads one and never makes one. That is half true. Making a session is indeed
not something `auth` does, but a cookie is a **response header**, `set-cookie`
is response metadata, and `web.Transcode` hands one to the browser as the other
— so the console's sign-in is `AuthService.SignIn`, an RPC like every other call
the page makes, generated for whatever speaks to it next.

```sh
curl -sX POST https://console.example/roster.AuthService/SignIn \
  -H 'Content-Type: application/json' -H 'Connect-Protocol-Version: 1' \
  -d '{"alias":"admin","password":"…"}' -i    # -> 200, set-cookie
```

Which also removed what the route cost. A service is registered per listener and
`AuthService` is on the control plane's alone, while the route ran wherever there
was HTTP — so the customer-facing port answered an operator's password with a
204 and a cookie that opened nothing, a trap that had to be documented rather
than closed.

## Three UIs, one library

`ts/` builds three pages over one `ts/lib/` and one `ts/gen/`: the **console**
(`ts/console/`), which an operator opens, the **account** page
(`ts/account/`), which a customer's people sign in at, and the **Login App's**
(`ts/login/`), which is where a browser Hydra redirected ends up. Same store,
same generated clients, same `covers()` deciding what is worth drawing; what
differs is the transport.

Two of them draw a sign-in, so it is one component: `ts/lib/signin.tsx` -- the
password form, the second factor, and the operator's provider buttons, in both. What is
shared is what `frontdoor/web/frontdoor.js` says is worth sharing, having tried
and refused the component library D22 asked for -- *three answers where a page
expects two, and a second form that must not be drawn from anything the server
said to call it.* The markup around it is each page's own. The console speaks Connect to `control.http` and `admin.http`;
the account page speaks Connect to its own origin and `roster account serve`
hands each call on to roster **as the person** (`frontdoor.Door.Proxy`), so a
browser never holds a roster token.

They are served by two processes on purpose. `roster serve` serves the console
at `/` on `control.http` when `control.console.dir` names the build
(`control.console.admin` tells the page where `admin.http` is). `roster account
serve` is its own process: it holds one tenant key per operator it fronts, faces
the internet, and reaches roster only over the wire -- `account/` imports the
generated clients and nothing of the server, which `scripts/test.sh` checks.
Providers come from the `Connection` rows an operator wrote in the console, and
the client secret from wherever `secret_ref` says (`env:NAME`).

```sh
roster account serve --roster roster:8080 --connect https://roster:8443 \
  --base https://login.example.com --static ts/dist/account \
  --key contoso=rt_… --key fabrikam=rt_…       # or ROSTER_ACCOUNT_KEY_<ALIAS>
  --seal env:ACCOUNT_SEAL                       # 32 bytes, base64; every replica the same
```

It keeps no sessions. The cookie **is** the session, sealed under `--seal`'s
key (`authsession.Sealed`), and what it holds is roster's delegation for that
person -- which roster ends, so nothing here has to be able to. Two replicas
under one key are one app; without `--seal` a key is made at start, which is
one replica and a restart that signs everybody out.

`roster ldap serve` is a third process of the same kind: roster as a
directory, for the clients on a network that speak LDAP and nothing else. It
holds one tenant key per operator, reads roster over the wire (`ldap/`, held
to it by the same check), and translates -- a bind into `Me.Get` bearing the
app password the client presented or `Vouch.Verify` with the person's own, a
search into `Holder.Search`, `Holder.List` and the membership lists. The
protocol itself is `ldap/wire`, five messages on BER and nothing borrowed.
`docs/ldap.md` is the design and its reasons; `docs/operating.md` § A
directory, over LDAP is how to run it.

`roster login serve` is a fourth, and the one for a deployment with **several
products**: the box Ory Hydra hands a `login_challenge` to, and the one that
answers `acceptLoginRequest{subject}` with a `Holder.id` -- so the `sub` every
product's token carries is a row here rather than a provider's own identifier.
It imports `frontdoor` for the forms and the delegation and `arrives` for the
provider round trip, like the account app, and differs in one hop: where a login
app ends in a cookie of its own, this ends at Hydra -- for a password and for an
account at a directory alike, which is why the same person is one `sub` whether
they came through Entra on Monday or a password on Saturday. Which operator a flow belongs to comes from the challenge's OAuth
client rather than from a hostname, and one `rt_` per operator follows from it.
So does the one thing that is not the account app's shape: **one** redirect URI
(`login.base`) for every operator, because Hydra sends every browser here under
one name -- which operator a provider's callback belongs to is the state's to
say.
It holds `SyncService` open too, so somebody signed out everywhere in roster is
somebody Hydra is told to forget. `go.mod` gains nothing for it: Hydra's admin
API is six endpoints of JSON, spoken with `net/http` in `login/hydra.go`.
`docs/login.md` § What changes when Hydra is in front is when to want it, and
`./scripts/hydra.sh` walks a whole authorization-code flow through the real
thing.

The sandbox (`npm run dev:sandbox`) is the console with the server compiled
into the page: `wasm/` serves the control listener and the admin one from one
instance, under two entry points the page dials by name. `wasm/sandbox` is the
memory of who signed in, standing in for the cookie a message port cannot
carry. Two more entry points serve the stacks with no wall, for payday's
devtools panel and its *past the wall* switch -- possible there because the
server is inside the page, and nowhere else. `ts/vendor/` holds the two libraries this depends on ahead of their
releases -- grpc-dgram for the second entry point, payday for the entity
declarations and the devtools panel -- as tarballs; its README says how they
were built and when to remove them.

Both pages are driven in a browser by `./scripts/e2e.sh`: it builds the binary
and the pages, stands a deployment up in a scratch directory the way
`docs/operating.md` says to, seeds a customer, starts `roster serve` and
`roster account serve`, and runs the Playwright specs in `ts/e2e/` against
them. It is the one gate `scripts/test.sh` does not run, because it needs a
browser and a minute -- and it is a gate, because a page that posts the wrong
field is green on every other one. `--hold` leaves the deployment up.

`go tool pd gen --ts .` writes the messages and service descriptors into
`ts/gen`, along with `entities.ts` — one declaration per entity, which is what
the local store is built from. Nothing there is behaviour, and nothing is
generated per service: `ts/lib/client.ts` turns a descriptor into a client in one
line, for whatever does not want to go through the store.

## What is enforced, and why

Every one of these is refused at generation, and every one of them is something
that fails **quietly** when it is left out:

| | what it costs to forget |
| --- | --- |
| `domain:` | identifiers that say nothing about what they name |
| a domain twice | an identifier that lies about what it names |
| tenancy unsaid | every row outside the wall, with nothing failing |
| `watch:` with no version | a stale answer overwriting a fresh one, on the client |
| `watch:` with no `ref` filter | a stream that cannot say which rows it is about |
| an overlay on payday's own field number | `alias` quietly becoming whatever the overlay said |
| a list order not ending in the key | a page that repeats a row or skips one |

And one that is a warning rather than a refusal, because a small table is a
real thing: a list order no index covers. An alias that is not a name is
refused, at generation and again at run time.
