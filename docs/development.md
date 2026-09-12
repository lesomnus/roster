# Working on roster

The rules are in [CLAUDE.md](../CLAUDE.md) -- what is generated, what is yours,
what to run before pushing. This page is the part that is background rather than
rule: why the packages are arranged as they are, what the generation loop is, and
what the generator refuses.

## Where to start reading

| | |
| --- | --- |
| `cmd/serve.go` | the stack written out -- which layers, in which order, and which server the wall is on. Deliberately not hidden behind a `payday.Serve(cfg)` |
| `proto/app/identity.proto` | an entity. The `(payday.entity)` option at the bottom is where the domain byte, the tenant wall, the `List` and the `Watch` come from |
| `cli/cli.go` | why the commands and the database drivers are a package of their own, in megabytes |
| `server/core/` | every rule no schema can state, each in a file named after it |

## Two names, and the reason for each

The generated messages are Go package `rstr` and proto package `roster`, which is
why the binary lives in `cmd/roster/` rather than beside them.

**`rstr` and not `api`**, because these messages are meant to be imported by
other apps, and `api` is what every payday app calls its own generated package --
a product importing roster's would be aliasing one of the two in every file that
mentions both.

**proto package `roster` and not `app`**, because protobuf's file registry and
payday's `pdid` domains are per **process**: two payday apps link into one binary
only if their proto packages differ. The reference app does, and a product
embedding roster's sign-in flow will.

To put the messages somewhere else, change `option go_package` in
`proto/app/*.proto` and regenerate. Every entity has to say the same one -- an
app is one Go package for everything generated, because the ent schemas of two
packages cannot have an edge between them and the tenant wall is an edge.
`pd gen` refuses the alternative rather than generating an app whose wall has
nothing to stand on.

## Generating

```sh
go tool pd gen .          # messages, servers, ent schema, layers
go tool pd gen --ts .     # and the TypeScript half
go tool pd doctor .       # what would go wrong before it does
```

`pd gen --check --ts .` is what CI runs: a generated file that was not
regenerated **compiles perfectly and is wrong**.

`pd gen` also pins the buf dependencies the first time, which is not a
convenience. `buf dep update` compiles the workspace before it writes the lock,
and this app's schema names `Tenant` -- which does not exist until a generation
has copied payday's entities into `proto/roster/payday/`. So there is exactly one
moment it can run: inside `pd gen`, between those two things.

## Upgrading payday

```sh
GOPROXY=direct go get github.com/lesomnus/payday@<sha>
go tool pd gen . && go tool pd gen --ts .
```

By commit and not `@main`: the module proxy caches what `@main` resolves to, so a
`go get` right after a push reports success and changes nothing.

payday owns some of this app's schema -- `Tenant`, `Holder`, `Audit`, `Outbox` --
so a field added to one of them there arrives in `internal/ent` here the next
time you generate, and **nothing about that is loud on its own**. It compiles,
the tests pass against a database the tests just created, and the first sign of
trouble is a column that is not there in the one handler that reads it.

Two things refuse rather than trusting anybody to remember:

- **a `pd gen` that did not happen.** `server/pd/pd.g.go` carries the payday it
  came out of, and `pd.NewSink` refuses a binary linking a different one.
- **a migration that did not happen.** `serve` looks at the database before it
  answers anything and refuses one that is not the shape `internal/ent`
  describes, printing the SQL that is missing.

Neither says anything when it cannot -- a `replace`, a workspace, a build with no
version are all somebody developing. While developing, `db.migrate: true` swaps
the second refusal for `ent.Schema.Create`; that is a decision about who may
alter tables, so it should be made on purpose rather than left on. There is no
migrations directory, and what a change means for a running deployment is written
beside the change.

## Adding an entity

```sh
go tool pd entity add --tenanted --watch Widget .
go tool pd entity list .
```

Rather than writing the file: it picks a domain nothing else has and writes the
tenancy out, which are the two things that are cheap to get wrong and expensive
to find later. CLAUDE.md has the field-number convention that is read by name
across every entity.

## The pages

```sh
cd ts && npm install
npm run dev            # the console, against a running roster
npm run dev:sandbox    # the console with the whole server compiled into the page
npm run dev:login      # the sign-in pages, with a made-up server behind them
```

`ts/` builds three pages over one `ts/lib/` and one `ts/gen/`: the **console**
(`ts/console/`), the **account page** (`ts/account/`) and the **Login App's**
(`ts/login/`). Two of them draw a sign-in, so it is one component --
`ts/lib/signin.tsx`, the password form, the second factor and the operator's
provider buttons. What is shared is what `frontdoor/web/frontdoor.js` says is
worth sharing; the markup around it is each page's own.

What is worth reading is what is **not** in them: nothing declares which query a
write invalidates, nothing pushes a new row into a list, and nothing joins up the
tenant shown beside a row with the one at the top of the page. That falls out of
the reads going through the framework -- `useQuery` makes the call, so the store
knows which rows were drawn, and everything drawing a changed row re-renders at
once. `ts/lib/store.ts` is the whole of the local store, including the IndexedDB
mirror that makes a reload draw the page it had; deleting two lines turns the
mirror off.

`pd gen --ts .` writes `ts/gen/`, including `entities.ts` -- one declaration per
entity, which is what the store is built from. Nothing there is behaviour, and
nothing is generated per service: `ts/lib/client.ts` turns a descriptor into a
client in one line.

React is a **peer** dependency of payday and an optional one. `payday/store` and
`payday/query` know nothing about it; `payday/react` is thirty lines of
`useSyncExternalStore` over them.

`ts/vendor/` holds two libraries ahead of their releases, as tarballs; its README
says how they were built and when to remove them.

### The sandbox

`npm run dev:sandbox` compiles the server into the page -- `GOOS=js GOARCH=wasm`,
SQLite in a Worker, a message port instead of HTTP/2. A reload is a fresh
deployment. One instance serves **two** servers, because the console reaches
`control.http` and `admin.http`, and the page dials the second by name on the
same socket. The cookie cannot work over a message port, so `wasm/sandbox`
remembers who signed in -- a sandbox being a sandbox, and `wasm/main.go` says how
far that goes.

It does not migrate: `wasm/schema` is the same tables as one SQL script, kept
true by `TestTheScriptIsThisSchema`.

## A browser cannot speak gRPC

Which is why every listener can have an `http` block beside it:

```yaml
server:
  http:
    addr: ":8080"
    allow_web: true
    origins: ["http://localhost:5173"]
```

What answers there is the **same** server -- the same interceptors, the same
credential, the same wall -- speaking Connect and gRPC-Web. A Connect call is a
POST with a JSON body, so it is also what to reach for from a shell:

```sh
curl -sX POST http://localhost:8080/roster.ThingService/List \
  -H 'Content-Type: application/json' -H 'Connect-Protocol-Version: 1' -d '{}'
```

Under TLS the same listener carries native gRPC as well, by ALPN.

**The sign-in is not a route.** It was `POST /session` on every listener that had
HTTP, on the reasoning that issuing a credential is HTTP because `auth` reads one
and never makes one. Half true: a cookie is a response header, and `web.Transcode`
hands `set-cookie` metadata to the browser as one -- so the console's sign-in is
`AuthService.SignIn`, an RPC like every other call the page makes. It also
removed what the route cost, which was the data plane's port answering an
operator's password with a cookie that opened nothing.

## What the generator refuses

Every one of these fails **quietly** when it is left out, which is why none of
them is a convention:

| | what it costs to forget |
| --- | --- |
| `domain:` | identifiers that say nothing about what they name |
| a domain twice | an identifier that lies about what it names |
| tenancy unsaid | every row outside the wall, with nothing failing |
| `watch:` with no version | a stale answer overwriting a fresh one, on the client |
| `watch:` with no `ref` filter | a stream that cannot say which rows it is about |
| an overlay on payday's own field number | `alias` quietly becoming whatever the overlay said |
| a list order not ending in the key | a page that repeats a row or skips one |

And one warning rather than a refusal, because a small table is a real thing: a
list order no index covers. An alias that is not a name is refused at generation
and again at run time.

## See also

- [CLAUDE.md](../CLAUDE.md) -- the rules, and the gates to run before pushing
- [docs/baseline.md](baseline.md) -- the promises, each pinned to its tests
- [docs/roadmap.md](roadmap.md) -- what was built, in order, and what it cost
- payday's [guides](https://github.com/lesomnus/payday/tree/main/docs) -- the
  framework this is written against
