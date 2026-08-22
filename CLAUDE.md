# Working on roster

A [payday](https://github.com/lesomnus/payday) app. Most of it is **generated
from `proto/`**, so the usual shape of a change is: edit the schema, regenerate,
then write the part no schema can state.

`PLAN.md` is what roster is for and every decision taken so far.
`docs/ROADMAP.md` is what is being built next, in order, and how far it has got
-- **update its progress table in the same commit as the work.** `README.md` is
the long version of the mechanics below.

## The one rule

> **When payday is in the way, stop and fix payday. Do not work around it here.**

roster is a proving ground for payday as much as it is a product, and it is the
more demanding of the two apps that try it. A workaround written here is a
defect left in payday for its next user, and it is invisible afterwards.

That means: fix it in the payday checkout, push it, then move the pin here. It
does not mean "file an issue and carry on".

**Move the pin with the commit, not with `@main`.** The module proxy caches what
`@main` resolves to, so `go get github.com/lesomnus/payday@main` right after a
push reports success and changes nothing -- and the next hour is spent chasing a
bug that is already fixed. `GOPROXY=direct go get github.com/lesomnus/payday@<sha>`.

## What roster is

The store that answers who somebody is: people, their external identities, their
addresses, and the tenants, sites and teams they belong to. It owns `sub`.

It is **not** the identity provider. Hydra speaks the protocol and a Login App
runs the flow; roster is what they ask. So its callers are machines -- the Login
App, admin consoles -- and its own authentication is mTLS or an API key, never
`authoidc`. See `PLAN.md`.

### The other rule

> **roster stores facts and verifies claims about them. It never issues anything
> a third party verifies.**

Apply it by asking **who checks this?** Only roster can -- a password, a magic
link, a TOTP code, an `rt_` key somebody introspects here -- then it is roster's
to hold. Somebody else has to be able to check it without asking -- a signed
token, a session cookie for another app's browser -- then it is not roster's to
make, and the answer is Hydra or the app's own `authsession`.

Do not restate this as a list of things roster does not implement. That version
existed, said "no providers, no MFA", and was already false: `VouchService`
checks a password. `PLAN.md` D19 and D20, `docs/POSITION.md`.

## Regenerate after touching the schema

```sh
go tool pd gen .          # messages, ent schema, servers, layers
go tool pd gen --ts .     # and the TypeScript half
go tool pd gen --check --ts .  # what CI runs: fails if anything moved
```

A generated file that was not regenerated **compiles perfectly and is wrong**.
If you edited anything under `proto/`, you are not done until
`pd gen --check --ts .` exits 0. With `--ts` because that is what CI runs: the
check without it passes while `ts/gen` is a schema behind, which is a green
local run and a red one on the branch.

```sh
go tool pd doctor .       # what would go wrong, before it does
```

## Do not edit — regenerate

| | |
| --- | --- |
| `*.g.go`, `*.pb.go` | wherever `go_package` puts them |
| `server/bare/`, `server/pd/`, `internal/ent/` | in whole |
| `proto/roster/payday/` | in whole — payday's entities, **copied** in |
| `proto/**/*_svc.g.proto` | the generated contract of an entity |
| `ts/gen/` | in whole |

**`.g` means a generator wrote it.** Everything else is yours — including
`proto/app/*.proto`, `proto/ext/**` (overlays), `cmd/`, and `ts/src/`.

To add a field to one of payday's entities, write an **overlay** in
`proto/ext/payday/`. Editing `proto/roster/payday/` directly is undone by the next
generation.

## Adding an entity

```sh
go tool pd entity add --tenanted --watch Widget .
```

Use this rather than writing the file. It picks a free domain number and writes
the tenancy out — the two things that are cheap to get wrong and expensive to
find later.

Field numbers are read by name across every entity:

- **1** key · **2** tenant · **3** *yours, a set smaller than a tenant* ·
  **4** `alias` · **5** `name` · **6** `desc` · **7** `labels` ·
  **13/14/15** the timestamps
- **8–12 and 16+** are yours. An entity that does not want 4–7 **leaves those
  numbers empty** rather than spending them on something else.

## Writing a layer

A layer embeds `Overlay` — and must also write `WithDriver`, which nothing
inherits:

```go
func (s Core) WithDriver(drv dialect.Driver) (api.Server, error) {
	next, err := enttx.Rebind(s.Next(), drv)
	if err != nil {
		return nil, err
	}

	return New(next), nil
}

var (
	_ api.Server               = Core{}
	_ enttx.Binder[api.Server] = Core{}
)
```

Leave it out and **nothing fails until a transaction is opened** — a batch, or a
multi-write RPC — because `enttx.Rebind` asks at run time. `pd doctor` finds it.

Note it is `enttx.Rebind(s.Next(), ...)` and not `s.Next().WithDriver(...)`:
`Next()` answers the generated `Server` interface, which deliberately has no
such method.

**Do not edit the generated `Gate`.** Your authorization goes in your own layer
in front of it, or into the `gate.Policy` you inject.

## Two servers, and one of them has no wall

`cmd/serve.go` builds `Walled` and `Ungated`. `Ungated` is not a privilege — it
is an instance the wall was never installed on, for work that cannot be done
from inside a tenant (`init`, resolving who is calling).

**Never hand `Ungated` to anything a caller can reach.** There is no superuser
flag to check; the wiring is the whole of the control.

## The wall is a predicate, so it only applies to reads

`Add` has no row to narrow, so it is gated in the generated `Gate` layer
instead. If you are reasoning about who may create something, that is where it
is decided.

## Running

```sh
go run ./cmd/roster init          # the first tenant, and somebody in it
go run ./cmd/roster serve
go run ./cmd/roster config env    # every variable this can be told through

cd ts && npm install && npm run dev
```

## `auth.Plain` is not for production

It believes what the caller writes. It is right for tests and a sandbox, and it
is what `serve.go` wires **when there is no `control:` plane** — an app with
nowhere to keep keys has nothing else it could check.

Name a control plane and the same wiring reads API keys instead
(`auth.Seq(keys.Acting(…), auth.Bearer(keys.Store(…)))`), which needs no
certificate authority and no HTTP endpoint: `roster key add` mints one from a
shell, and the control plane's `IssueService` mints one over the wire. mTLS is
the other answer and is a deployment's to configure.

What is HTTP is the console's **session cookie**, because that is a credential
a browser holds and `auth` reads credentials rather than making them.

## Reference

- `README.md` — the same ground at length, including upgrading payday
- <https://github.com/lesomnus/payday/tree/main/docs> — the guides and the
  references behind them
