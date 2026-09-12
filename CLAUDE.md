# Working on roster

A [payday](https://github.com/lesomnus/payday) app. Most of it is **generated
from `proto/`**, so the usual shape of a change is: edit the schema, regenerate,
then write the part no schema can state.

`docs/position.md` is what roster is for and where it stops. `docs/roadmap.md`
is how it was built, what is open, and how far new work has got -- **update its
progress table in the same commit as the work.** A decision's why is written
beside the thing it decides -- a file comment, a proto comment, the relevant doc
-- never in a central log; there no longer is one. `README.md` is the long
version of the mechanics below.

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
`authoidc`. See `docs/position.md`.

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
checks a password. `docs/position.md`, § "The line, in one sentence" and
§ "Second factors".

## Regenerate after touching the schema

```sh
go tool pd gen .          # messages, ent schema, servers, layers
go tool pd gen --ts .     # and the TypeScript half
go tool pd doctor .       # what would go wrong, before it does
```

A generated file that was not regenerated **compiles perfectly and is wrong**.
If you edited anything under `proto/`, you are not done until
`pd gen --check --ts .` exits 0 -- with `--ts`, because the check without it
passes while `ts/gen` is a schema behind, which is a green local run and a red
one on the branch.

## Before pushing

```sh
./scripts/test.sh         # everything CI decides on
```

**Run this rather than the parts of it.** It is gofmt, the build, the vet, the
tests, `pd doctor`, `pd gen --check --ts`, the wasm build and the console, in
that order -- and the two that have actually caught something are the two no
compiler complains about. A list of commands to remember is a list somebody
forgets one of, which is how this repository learned to want a script.

It takes what it is handed (`./scripts/test.sh -run TestWall`), and the other
half of CI is the same command with `PDTEST_POSTGRES` set. The one thing it
leaves to CI is `buf breaking`, which is about the branch rather than about the
checkout.

```sh
./scripts/e2e.sh          # the three pages, in a browser
./scripts/hydra.sh        # one OAuth flow, through a real Hydra, to an id_token
./scripts/cluster.sh      # deploy/ in k3d, and the class a fresh process hides
```

**Touching `ts/` means running this too.** It stands roster up the way
`docs/operating.md` says to, seeds a customer, and drives the console and the
account app with Playwright (`ts/e2e/`) -- and the Login App's page against
**nothing**, because `ts/vite.login.ts` is that app made up and so it costs a
port. It is not in `test.sh` because it needs a browser and a minute; it found
three defects on its first run that every other gate was green on, so it is a
gate. `./scripts/e2e.sh --hold` leaves the deployment up to look at.

**Touching `examples/product` or `deploy/` means running `cluster.sh`.** It
stands `deploy/` up in k3d against a Hydra with **no `--dev`**, walks
`docker/itself.sh` as a Job inside it, and then changes the declared client
**under the running pods** and walks it again -- because the one class every
other gate here is blind to is behaviour that only appears when configuration
changes under a process that is already up (#12). What it pins about that app is
that the client authentication method is **said in code**: a restart must not
cure a registration mismatch, and putting the registration back must cure it
with no restart. Take `AuthStyle` out and the rig fails on that line.

**Touching `login/` means running `hydra.sh`.** It stands `compose.yaml` up
from **nothing** and walks the flow four ways. Three are `curl` against the
protocol, once per consent mode and once with a second factor: to an
`id_token` whose `sub` is the `Holder.id` roster holds, a second flow Hydra
skips the form for -- **and the claims in the token that one hands back** --
then `Holder.Invalidate`, then the form asked for again, then the person
signing **out**, with the token and without it. The fourth and fifth are the
two demo relying parties, which is where every defect a local gate could not
see has come from: `docker/behind.sh` is `oauth2-proxy` in front of a page,
`docker/itself.sh` is `examples/product` holding its own token and building
its own URLs -- `docs/relying-party.md` is what each of the two is and which
defects only its own shape produces. `login/`'s own tests use a fake Hydra,
deliberately, and every defect this has found was one that fake was green on.
`--hold` leaves it up.

**And a route added to `login/login.go` is a contract change.** That app's HTTP
surface is what a deployment writing its own sign-in page builds against
(`docs/login.md` § *A page of your own*, `login.page.dir`), so the table there and
those routes are kept in step by `login/contract_test.go` -- in both directions,
because a mounted endpoint nobody promised to keep is how an internal shape
becomes an accidental API.

## Do not edit — regenerate

| | |
| --- | --- |
| `*.g.go`, `*.pb.go` | wherever `go_package` puts them |
| `server/bare/`, `server/pd/`, `internal/ent/` | in whole |
| `proto/roster/payday/` | in whole — payday's entities, **copied** in |
| `proto/**/*_svc.g.proto` | the generated contract of an entity |
| `ts/gen/` | in whole |

**`.g` means a generator wrote it.** Everything else is yours — including
`proto/app/*.proto`, `proto/ext/**` (overlays), `cmd/` and `cli/`, and `ts/console/`,
`ts/account/`, `ts/lib/` (the two UIs and what they share).

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

## Before answering "roster cannot do that"

**The general half of this is payday's**, in its guide: `docs/guide/server.md`,
§ *Writing a layer* -> *More than one layer, and which one a caller enters at*.
A layer takes the request it was given and calls `Next()` with a different one,
so a field means one thing where a caller reaches it and another by the time the
row is written. And `app.Build` takes a list, so a rule that must hold for one
caller and not another is a link of its own that the other one does not enter.
Read that before writing that something is impossible; two of this repository's
"cannot"s were that shape.

What is roster's own, and is not a wall either:

- **"That method is closed on the wire."** `closed()` in `cmd/serve.go` is a
  deployment's gate list. `Credential.Add` is shut because a caller sending a
  hash would walk past the corpus check and the length rule, not because it
  could not be given other rules.
- **"There is no field for it."** `proto/ext/**` is roster's and the overlay
  redefines the message. A field number and `pd gen`.
- **"The two planes need different rules."** They are two stacks built from two
  lists in `cmd/serve.go` (`Walled` and `Ungated`), and `cmd/admin.go` builds a
  third. Nothing says a plane's list must match.

`server/core/self.go` is the worked example: one rule -- a caller writes their
own password and nobody else's -- in a link of its own, so a plane that does not
want it leaves that link out of its list rather than the rule growing a
condition.

It is also the example of the **cheaper** answer, which is worth reading before
building a stack. That rule had to be got past by exactly one verb, and for a
while the way past was a whole second build of the same list with the link
missing (`Operable`). It did not need one: the verb was `Vouch.Reset`, a
hand-written service reaching *through* `Credential.Set` from **outside** the
layers, and the moment it became `Credential.Issue` -- a method on the entity,
calling its own `Set` from inside `server/core` -- it was already below the
link, because `Self` overrides `Set` and nothing else. A second stack is for a
caller that genuinely enters somewhere else; a verb that belongs to the layer
should be *in* the layer.

The refusals that are **real** have a reason written beside them: the wall is
payday's and not configurable, a grant is any write that changes what the gate
answers, nobody writes a way into an account wider than their own. Those live
next to what they decide. Everything else is a thing to write.

## Overlay before service, layer before overlay

Most of what looks like a new service is a method on an entity that already has
one. Before writing a `*_svc` proto, ask **which single entity's rows this
reads or writes**:

- **It transforms or guards a generated verb** — hashes the secret on
  `Credential.Add`, strips a column out of a `Get`, refuses a field on `Patch`.
  That is a **layer** in `server/core`, in front of the generated `Gate`; it
  needs no new RPC. `pd.Secret` is the shape (`cmd/serve.go`), and per-target
  rules live here because the gate sees only the method, not the request.
- **It is a new verb on those same rows** — `Verify` a secret, `Unlock` a
  lockout, `Disable` somebody. That is an **overlay** in `proto/ext/`: a method
  added to the entity's own service. `HolderService`'s `Disable`/`Enable`/
  `Invalidate`/`Update`/`SignsIn`/`Reaches` are the pattern — *a second
  service would be one more name for the same rows.*

A new service is justified only when the operation belongs to **no single
entity**: it reads the caller from the frame and holds only what a role cannot
be asked for (`MeService`: `Get`, `Unlink`, `SignOutEverywhere` -- waived, so
subject-less by necessity), it answers before
anybody is resolved to a tenant so it reads the unwalled server and hands back
one identifier and no row (`FrontService`), or it mints/spends secrets across
several rows in one flow (the sign-in flow, `AuthService`). Each such service
carries a `// Why it is not XService` paragraph naming which case it is, and
that paragraph is **required**: a service that cannot write it is the smell this
section exists to catch. `Vouch.Set` was `Credential.Add` with a hash, invisible
once written; the writes that hid there belong on `CredentialService`.

**And no self-only twin of a verb.** RBAC stays what it is: a role grants a
method, tenant-wide, and the gate is not taught about rows. *Whose row* is a
layer -- `mayReach` in `server/core`: yourself always, anybody no wider than
you, nobody wider -- and anything finer is the deployment's own layer or app,
never a `ChangeMine` beside `Set`. A person's self-service is the operator's
verb with the person's own reference, and the app that serves them is the
layer that passes only that reference. What such a twin was reaching for
belongs on the verb as a rule about your own row (`Credential.Set` asks for
`current` when `ref` is you), not as a second name for the same rows.

## Writing a layer

A layer embeds `Overlay` — and must also write `WithDriver`, which nothing
inherits:

```go
func (s Core) WithDriver(drv dialect.Driver) (api.Server, error) {
	next, err := enttx.Rebind(s.Next(), drv)
	if err != nil {
		return nil, err
	}

	return New(next, s.rules), nil
}

var (
	_ api.Server               = Core{}
	_ enttx.Binder[api.Server] = Core{}
)
```

A layer that holds anything hands it over as well -- `server/core` carries its
`Rules`, and a rebuild that dropped them would answer inside a transaction with
a stack that refuses everything a frame carries.

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

## `cmd` is wiring, `cli` is a process

`cmd` is what both entry points share — `Build`, `Server`, `Config`, `Seed` —
and the Wasm sandbox imports it. `cli` is everything only a process does: the
commands, the blank-imported database drivers, and ent's migration engine.

> **Nothing in `cmd` may name a driver, `entmigrate` or `entschema`.**

The linker follows imports, so one of those in `cmd` is thirty-five megabytes
added to the module a browser downloads — and nothing fails, the page just gets
slower. `scripts/test.sh` checks the Wasm dependency graph for it. A new command
goes in `cli/`; a helper both a command and the sandbox need goes in `cmd/`
(`cmd/service.go` is the one that had to move back).

The sandbox does not migrate: `wasm/schema` is the same tables as one SQL
script, kept true by `TestTheScriptIsThisSchema`.

## The wall is a predicate, so it only applies to reads

`Add` has no row to narrow, so the generated `Gate` layer stands in front of it
-- and what it decides is smaller than it sounds: that there **is** a caller,
and that the rows an `Add` hangs off are ones that caller can see. Who may
create a thing at all is the policy, in the interceptor above it. Neither is
where *what this write hands out* is decided.

> **A grant is any write that changes what the gate will answer for somebody.**

Which is wider than the writes that name a role, and is the sentence three
separate holes were found behind. `cmd/policy.go` answers from three sets --
bindings written to a person, bindings written to a group they are in, and roles
they hold in a team -- so a write that adds a row to any of the three hands out
whatever that row reaches. `GroupMembership.Add` names no role and grants as
much as `Binding.Add` does.

And beside it, the rule that is not about permissions at all:

> **Nobody writes a way into an account wider than their own.**

A password, a second factor, an account at a provider, a mailbox a recovery link
goes to, a key that acts as them. `Identity.Add` and `Email.Add` sound like
keeping a directory tidy and each is a way to sign in as whoever the row is
about.

If you are adding a write, ask both questions of it before asking anything else.
`server/core/escalate.go` is where the answers live; its file comment is how
they were arrived at.

## Running

```sh
go run ./cmd/roster init          # the first tenant, and somebody in it
go run ./cmd/roster serve
go run ./cmd/roster config env    # every variable this can be told through

cd ts && npm install && npm run dev            # the console, cross-origin
npm --prefix ts run dev:login                  # the sign-in pages, no backend at all
go run ./cmd/roster account serve --roster … \
  --connect … --key contoso=rt_… --static ts/dist/account   # the front door
go run ./cmd/roster ldap serve --roster … --key contoso=rt_…  # the directory

docker compose up --build       # Postgres, both planes, a customer, both pages, LDAP
```

Three UIs, and one to four processes. `roster serve` serves the console under
`/` on `control.http` when `control.console.dir` names the build.
`roster account serve` holds tenant keys and faces the internet; `roster ldap
serve` is roster as a directory for clients that speak nothing else.

Those two are also **blocks** — `account:` and `ldap:`, named is a listener and
empty is nowhere — so `roster serve` opens them and a deployment need not be
three containers to run one binary. Which to want is a question about blast
radius and it is the deployment's: `docs/operating.md`, "One process, or three",
and `cmd/consumers.go` for why it is not this repository's answer.

**Either way they are consumers that reach roster only over the wire**
(`account/`, `ldap/`, checked by `scripts/test.sh` — neither may import
`internal`, `cmd` or `server`). In one process the account app dials roster's
own listener, so nothing about that is relaxed.

`docs/ldap.md` is the directory's design -- a bind is an app password (a key
with `Me.Get` alone), and a password bind cannot pass a second factor because
`Vouch.Verify` says so.

## `auth.Plain` is not for production

It believes what the caller writes. It is right for tests and a sandbox, and it
is what `serve.go` wires **when there is no `control:` plane** — an app with
nowhere to keep keys has nothing else it could check.

Name a control plane and the same wiring reads API keys instead
(`auth.Seq(keys.Acting(…), auth.Bearer(keys.Store(…)))`), which needs no
certificate authority and no HTTP endpoint: `roster key add` mints one from a
shell, and `ApiKey.Issue` mints one over the wire. mTLS is
the other answer and is a deployment's to configure.

What is HTTP is the console's **session cookie**, because that is a credential
a browser holds and `auth` reads credentials rather than making them.

## Reference

- `docs/entity.md` — the twenty-three entities, how they relate, one paragraph each
- `docs/glossary.md` — the vocabulary this file uses as given: the wall, the gate,
  a grant, a layer, a plane, a walk, a rig. Four words name two things each and it
  says which
- `docs/baseline.md` — the promises a normal user relies on, each pinned to its
  tests. **Touching code under one of them means running its tests, and a
  baseline test is never weakened to let a change pass.**
- `docs/relying-party.md` — the two shapes an app takes in front of the issuer,
  the two runnable ones, and the API each uses
- `README.md` — the same ground at length, including upgrading payday
- <https://github.com/lesomnus/payday/tree/main/docs> — the guides and the
  references behind them
