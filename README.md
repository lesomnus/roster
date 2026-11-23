# roster

**The store that answers who somebody is.** People, the external identities they
sign in with, the addresses they use, and the organisations, sites and teams they
belong to -- held in one schema, so that changing identity provider is changing a
login screen rather than changing the record of who works here.

It is one layer of an identity system and not the whole of one: the protocol is
[Ory Hydra](https://www.ory.sh/hydra/)'s and the sign-in flow is an app's. roster
owns the records they both ask about, and owns `sub`.

> **Read [docs/glossary.md](docs/glossary.md) first.** A handful of words here --
> *holder*, *tenant*, *roster operator*, *roster user*, *admin console*, *user
> console* -- mean one thing each and are used that way everywhere, including in
> commits and issues. `cli/docs_test.go` holds the documentation to it.

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
| the admin console | <http://localhost:8081/> | `admin` / `admin` |
| the user console, one customer's own organisation | <http://localhost:8080/> | `erin` / `correct horse battery staple` |
| a customer's own people, through the app in front | <http://localhost:8090/> | `erin` / `correct horse battery staple` |
| a product app, holding its own token | <http://localhost:5555/> | sign in through Hydra |
| the same page behind `oauth2-proxy` | <http://localhost:4180/> | the other relying-party shape |
| the data plane, for your app | `localhost:50051`, or `:8080` over HTTP | a key, below |
| the directory | `ldap://localhost:1389` | an app password |
| where a directory provisions people | <http://localhost:8092/scim/v2> | a tenant's key, from `roster scim provision` |
| what the app in front mailed | <http://localhost:8025/> | Mailpit, and nothing of roster's |

That is five processes of roster's, Hydra, a mailbox, and two demo products
([docs/relying-party.md](docs/relying-party.md)). `docker compose down -v` takes
it all away.

Every port is published on `127.0.0.1` alone, because the passwords above, and
the database's, Hydra's and the clients' secrets in `compose.yaml`, are the same
in every checkout. To be reached from another machine say so, beside the name it
is reached by -- `BIND=0.0.0.0 PUBLIC_HOST=<that name> docker compose up
--build` -- and whoever can reach this machine can then sign in as the operator.

### Or from a checkout, in five commands

```sh
go run ./cmd/roster init                      # the operator who runs this deployment
go run ./cmd/roster serve &                   # the listeners roster.yaml names

roster --as admin tenant add @newco           # your first customer, and who administers it

KEY=$(roster --as admin key add --tenant newco --holder admin --allow '/roster.*/*')
```

`init` writes the operator and **no customer** -- a tenant is a customer, and one
written by a command is a customer nobody asked for. `--as admin` is that
operator: a command that reads the database directly says who is at the shell,
and what it shows or writes of anybody is on the access record under that name.
A tenant arrives with somebody in it -- `@newco/admin`, the role `everything`,
and the binding between them -- so there is nobody to add before the key. It is
printed once and resolves to that person, and the loop closes with the fifth
command, a call:

```sh
curl -sS -X POST http://127.0.0.1:8080/roster.MeService/Get \
  -H 'content-type: application/json' -H 'connect-protocol-version: 1' \
  -H "authorization: Bearer ${KEY}" -d '{}'
```

A password instead of a key, for somebody at a browser:

```sh
roster --as admin vouch reset @newco/admin               # generated here, printed once
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
| let a directory provision people here | [docs/scim.md](docs/scim.md) |
| know the tables | [docs/entity.md](docs/entity.md) |
| know what a word here means | [docs/glossary.md](docs/glossary.md) |

[docs/README.md](docs/README.md) is the same map with the reading order on it.

## What it does that is worth knowing before you look

- **Verifies a password** without handing the hash out -- argon2id, timing-safe,
  with attempt counting and a lockout, all in the one place that holds the row.
  A second factor (TOTP or WebAuthn) is the same story.
- **Answers to API keys.** A second roster runs in the same process on its own
  database, holding the roster operators and the keys of the apps they run
  across tenants -- so a key never lives in the tables it protects.
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

## What the image is made of

**roster's images are builds of `main`, named by run.** A push to `main` whose
gates all pass is pushed once, as `:edge`, `:r<run>`, `:YYMMDD` and
`:YYMMDD-r<run>`, and nothing else is pushed: a commit whose run went red has no
image until that run is run again and passes. The image of a commit is found from
its `ci` run on `main`, as
[deploy/README.md](deploy/README.md#using-it-as-a-base) says, and names its
commit as the label `org.opencontainers.image.revision`, and inside it as
`roster version --short`; one pushed before that was compiled in prints an empty
line there.

**A release is a name for one of those builds.** A `v*` tag pushed names the
image `main` pushed for the tagged commit, under the tag's own name and no other
-- `ghcr.io/lesomnus/roster:v0.2.0`, and no `latest` -- by the same digest, so
its SBOM and its attestation below are the release's as well
(`.github/workflows/release.yml`). Nothing is built for a tag. A tag of a commit
with no image -- not of `main`, or whose run never passed -- fails, and names
nothing. A release's name does not move: a tag whose name the registry already
has at another digest fails, and writes nothing. Inside a release,
`roster version` says the build's own name, `YYMMDD-r<run>`, since nothing was
compiled for the tag, and `roster version --short` the commit. `v0.1.0` and
`v0.1.1` were tagged before this, and name no image.

Every image `main` pushes to
[ghcr.io/lesomnus/roster](https://github.com/lesomnus/roster/pkgs/container/roster)
carries its SBOM beside it: a CycloneDX document attached to the digest as an OCI
referrer, and the same document signed as a keyless Sigstore attestation by
`ci.yml` on `main` ([lesomnus/sbom-attach](https://github.com/lesomnus/sbom-attach)).
It lists the Go modules both binaries carry, the base image's Debian packages, and
the npm packages the pages are bundled from -- which the image cannot show, so they
come from `ts/package-lock.json`, without the dev dependencies. An image pushed
before this was added has none.

Read it without pulling the image -- by digest, since all but a release's tags move:

```sh
oras discover --artifact-type application/vnd.cyclonedx+json ghcr.io/lesomnus/roster@sha256:…
oras pull ghcr.io/lesomnus/roster@sha256:…        # the referrer's digest: sbom.cdx.json
trivy image --sbom-sources oci ghcr.io/lesomnus/roster@sha256:…
```

and check that this repository's workflow on `main` made it and nothing has changed
it since:

```sh
cosign verify-attestation --type cyclonedx \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity https://github.com/lesomnus/roster/.github/workflows/ci.yml@refs/heads/main \
  ghcr.io/lesomnus/roster@sha256:…
```

That proves where the document came from, not that it is complete: a dependency
that leaves no trace in the image or in the lockfile is in neither scan. And the
signing is public -- Sigstore's transparency log records the repository, the
workflow, the commit and the digest of each one.

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
  generation, upgrading payday, the four pages, the sandbox, and what the
  generator refuses.
- [docs/baseline.md](docs/baseline.md) -- the promises a normal user relies on,
  each pinned to its tests.
- [docs/roadmap.md](docs/roadmap.md) -- how it was built and how far new work has
  got.

roster is also the second app payday is tried against, and the more demanding
one.
