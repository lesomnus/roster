# roster

**The store that answers who somebody is.** People, the external identities they
sign in with, the addresses they use, and the organisations, sites and teams they
belong to -- held in one schema, so that changing identity provider is changing a
login screen rather than changing the record of who works here.

It is one layer of an identity system and not the whole of one: the protocol is
[Ory Hydra](https://www.ory.sh/hydra/)'s and the sign-in flow is an app's. roster
owns the records they both ask about, and owns `sub`.

> **roster stores facts and verifies claims about them. It never issues anything
> a third party verifies.**

So it checks a password and a second factor, and it does not mint a session for
somebody else's browser or sign a token another system verifies alone.
[docs/position.md](docs/position.md) is that line applied, including what it
means you still have to run.

## Quick start

Everything, on Postgres, with a customer already in it:

```sh
docker compose up --build
```

| | | |
| --- | --- | --- |
| the console | <http://localhost:8082/> | `admin` / `admin` |
| a customer's own people | <http://localhost:8090/> | `erin` / `correct horse battery staple` |
| a product app, holding its own token | <http://localhost:5555/> | sign in through Hydra |
| the same page behind `oauth2-proxy` | <http://localhost:4180/> | the other relying-party shape |
| the data plane, for your app | `localhost:50051`, or `:8080` over HTTP | a key, below |
| the directory | `ldap://localhost:1389` | an app password |

That is four processes, Hydra, and two demo products
([docs/relying-party.md](docs/relying-party.md)). `docker compose down -v` takes
it all away.

### Or from a checkout, in five commands

```sh
go run ./cmd/roster init                      # the operator who runs this deployment
go run ./cmd/roster serve &                   # the listeners roster.yaml names

roster tenant add @newco                      # your first customer
roster holder add @newco/admin                # somebody in it
roster role   add @newco/everything '{"methods":["/roster.*/*"]}'
echo '{"role":  {"slug":{"alias":"everything","tenant":{"alias":"newco"}}},
       "holder":{"slug":{"alias":"admin",     "tenant":{"alias":"newco"}}}}' \
  | roster binding add -

KEY=$(roster key add --tenant newco --holder admin --allow '/roster.*/*')
```

`init` writes the operator and **no customer** -- a tenant is a customer, and one
written by a command is a customer nobody asked for. The key is printed once and
resolves to that person, so the loop closes with a call:

```sh
curl -sS -X POST http://127.0.0.1:8080/roster.MeService/Get \
  -H 'content-type: application/json' -H 'connect-protocol-version: 1' \
  -H "authorization: Bearer ${KEY}" -d '{}'
```

A password instead of a key, for somebody at a browser:

```sh
roster vouch reset @newco/admin               # generated here, printed once
```

[docs/usage/tutorial.md](docs/usage/tutorial.md) is this walked slowly, from an
empty directory to a person signing in, and `cmd/tutorial_test.go` runs it on
every commit so the page cannot drift from the binary.

## Where to go next

| you want to | |
| --- | --- |
| decide whether roster is the right thing at all | [docs/position.md](docs/position.md) |
| follow one deployment end to end | [docs/usage/tutorial.md](docs/usage/tutorial.md) |
| know what to type | [docs/usage/](docs/usage/) -- the CLI, customers, ways in, permissions |
| run it for real | [docs/operating.md](docs/operating.md) -- databases, listeners, processes, the trail, replicas, TLS |
| sign people in | [docs/login.md](docs/login.md), and [docs/relying-party.md](docs/relying-party.md) for the app in front |
| serve a directory | [docs/ldap.md](docs/ldap.md) |
| know the tables | [docs/entity.md](docs/entity.md) |
| know what a word here means | [docs/glossary.md](docs/glossary.md) |

[docs/README.md](docs/README.md) is the same map with the reading order on it.

## What it does that is worth knowing before you look

- **Verifies a password** without handing the hash out -- argon2id, timing-safe,
  with attempt counting and a lockout, all in the one place that holds the row.
  A second factor (TOTP or WebAuthn) is the same story.
- **Answers to API keys.** A second roster runs in the same process on its own
  database, holding the deployment's own services and what each may call -- so a
  key never lives in the tables it protects.
- **Roles bound at a scope**, in the shape Kubernetes settled on: a `Site` is a
  namespace, a role with no site is a `ClusterRole`, and nobody may grant what
  they do not hold.
- **`/me`** -- who the caller is and every RPC they may call, in one round trip,
  from the same union the server enforces.
- **Answers about a token it issued**, so a product app handed a key learns which
  person it stands for. Opaque on purpose: revoking is a delete, and it works
  now.
- **A terminal is enough.** Every RPC that can have a command has one, and the
  same binary is a customer's own client (`client.addr` and their key) as well as
  a shell on the box.

## Working on roster

Most of this app is generated from `proto/`, so the usual shape of a change is:
edit the schema, regenerate, then write the part no schema can state.

```sh
go tool pd gen . && go tool pd gen --ts .    # after touching proto/
./scripts/test.sh                            # everything CI decides on
```

- [CLAUDE.md](CLAUDE.md) -- the rules: what is generated, what is yours, and the
  one about fixing [payday](https://github.com/lesomnus/payday) rather than
  working around it here.
- [docs/development.md](docs/development.md) -- the same ground at length:
  generation, upgrading payday, the three pages, the sandbox, and what the
  generator refuses.
- [docs/baseline.md](docs/baseline.md) -- the promises a normal user relies on,
  each pinned to its tests.
- [docs/roadmap.md](docs/roadmap.md) -- how it was built and how far new work has
  got.

roster is also the second app payday is tried against, and the more demanding
one.
