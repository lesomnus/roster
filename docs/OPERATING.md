# Running roster

Task-shaped: what to type, in the order it has to happen. What each part *is*
lives in [POSITION.md](POSITION.md) and the package comments.

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

**Leave `control` out and the deployment believes its callers** — `auth.Plain`,
which says so once in the log. That is right for a checkout and is not something
to serve anywhere else.

A key must not live in the tables it protects, which is why the second database
is a database and not a reserved tenant. There is no query from one to the
other.

## Your first tenant and person

```sh
roster init --tenant acme --holder admin
```

A tenant is not put up from inside one, so the first row cannot arrive over the
API. `init` writes it through the server instance the process holds.

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
| `/roster.HolderService/Watch` | hearing that somebody left |
| `/roster.HolderService/Get` | a name for a screen, if it draws one |

Not `VouchService/Set` — changing a password belongs to whatever account portal
owns the person — and no `Holder` writes, since a product does not own the
people it serves.

## Who may do what

**Nobody, until you say so.** A caller with no binding may call nothing, because
the alternative is that adding the first role silently takes away what everybody
had.

A role is a list of RPCs:

```
Role     alias=operator  methods=[/roster.HolderService/Get, …]  site?
Binding  role  holder|group  site?
```

Scope is where the reference sits:

| where the role is referenced | what it covers |
| --- | --- |
| a `Binding` with no site | the whole tenant |
| a `Binding` with a site | that site |
| a `TeamMembership` | that team |

A `Group` is a set of people and grants nothing by itself — a binding to a group
reaches everybody in it, so the rule is written once and the membership changes.

### You cannot hand out what you do not hold

Writing a role or binding one is refused when it names a method you do not hold
**through a binding**. A role you hold in one team is not yours to bind across
the tenant.

So the first binding is `init`'s, and everything else descends from it.

### What a page shows

```
POST /roster.MeService/Get   {}
```

Takes nothing, answers about the caller: their identifiers, addresses, teams,
and every method they may call. That list is the union the server enforces, so
what a page shows and what it is allowed to do cannot drift.

It needs **no role** — requiring one to learn that you hold none is a deployment
where a new account cannot be told what it is for.

## Signing somebody in

See [LOGIN.md](LOGIN.md) for the whole path. In short: a product app calls
`Vouch.Verify` with its key, gets yes and two identifiers, and sets its own
session cookie. roster never talks to a browser.

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

- **No admin console.** Keys and roles are the CLI's, which means a shell on the
  box. A console would itself need a key, and the first key has to come from
  somewhere that is not one.
- **The sync channel is unfinished.** `Holder.Watch` streams, and an erase does
  not yet arrive on an open stream; see custody's `cmd/sync.go`.
- **No escalation-proof `Patch`.** `Role.Patch` would be how a role grows
  methods after it was written, and it is closed at the transport. A deployment
  that opens general writes opens that with them.
