# Upgrading a deployment

What a running roster needs when the binary changes: what moved in the
database, what changed in behaviour, and what is only new surface you can
ignore.

Read the section for the version you are coming **from**, and every one below
it.

## The short version

roster has no `migrations/` directory. `db.migrate: true` lets ent bring the
database to the shape the schema says; without it, `serve` runs `migrate.Check`
and **refuses to start** until somebody has. That is the direction to fail in —
a process that started against a database it does not match is one that answers
wrongly rather than not at all.

So an upgrade is: read what moved, decide whether it is safe to apply
automatically, apply it, start.

## From `800b843` — a UUID is the standard library's

Nothing moves in the database. Every identifier column holds the same text it
held before, and holds it as text: `database/sql` writes a `uuid.UUID` out in
the canonical form, which is what `github.com/google/uuid` did for itself. The
wire is unchanged too, since a UUID has always crossed it as sixteen bytes.

It is here only so that an operator reading a diff of the binary's dependencies
and finding `github.com/google/uuid` gone knows it was deliberate and that
nothing has to be applied.

## From `47cd900` — every table was renamed

roster now builds on a fork of ent that names a table after its entity without
pluralizing it. `user` rather than `users`, `site` rather than `sites`, and so
on for every table this deployment has. Index names do not move, having been
built from the singular already, and neither do the join tables of a
many-to-many edge, which are named after the edge. Foreign key constraint
symbols carry two table names and move with them.

This is the largest database change roster has had. It touches every table, and
nothing in the schema or the code says it happened — the names are derived, so
the diff is entirely in the database.

**Applying it.** `db.migrate: true` and ent plans the rename with everything
else. Read the plan first: a rename is cheap, but ent decides between renaming
a table and creating a new one beside the old, and the second would leave every
row behind. `migrate.Check` refuses to start against a database that has not
been moved, so a deployment that skips this stops rather than reads empty
tables.

**Anything that names a table itself** has to move by hand: a dashboard query,
a backup script that copies named tables, a report, an external reader. Nothing
inside roster does — it goes through the generated constants — but a deployment
usually has something outside it that does.

## From `dec419b` — the trail grew a column, and an erase learned to destroy

### The database moves, and one part of it is not free

`Audit` gains **`domain`** (`uint32`, default `0`) and an index on
`(domain, date_created)`.

Adding the column is cheap: it has a default, so PostgreSQL 11 and later add it
without rewriting the table.

**The index is not.** `CREATE INDEX` takes a write lock for as long as it takes
to build, and `Audit` is the one table that never stops growing — on a
deployment with a long trail this is minutes of blocked writes, and `db.migrate:
true` will do it the blocking way without asking. On anything large:

```sh
# before starting the new binary, with the old one still running
CREATE INDEX CONCURRENTLY audit_domain_date_created ON audit (domain, date_created);
```

Then start with `db.migrate: true` and ent will find it already there.

**Rows written before the column read as `pdid.Unknown`.** That is a real value
and not a gap — no entity may be registered as domain 0 — so a per-kind
retention policy never matches them and they fall to whatever the default says.
`cmd/upgrade_test.go` pins both halves.

### One behaviour changes, and it can lock somebody out

**A second factor no longer finishes a sign-in on its own.** `Verify` with
`kind: "totp"` used to answer `ok` for anybody whose *only* credential was a
seed — six digits and a thirty-second window were a whole sign-in — and it now
refuses with `FailedPrecondition`.

Who this reaches: anybody in your deployment holding a TOTP credential and **no
password and no external identity**. Before upgrading:

```sql
SELECT h.id, h.alias FROM holder h
  JOIN credential c ON c.holder_id = h.id AND c.kind = 'totp'
 WHERE NOT EXISTS (SELECT 1 FROM credential p WHERE p.holder_id = h.id AND p.kind = 'password')
   AND NOT EXISTS (SELECT 1 FROM identity i WHERE i.holder_id = h.id);
```

Rows here are people who could sign in before and cannot after. An empty result
means nothing to do.

Giving them one is `VouchService/Reset` on the **admin port** — the door that
exists for exactly this, *setting a password for somebody who has just phoned
support*. There is no shell command for it: `roster` has entity commands and
`roster key add`, and a data-plane person's password is neither. So if you have
people in that query and no admin port configured, configure one before
upgrading, or leave the seed-only accounts alone until you do.

The same rule stops `Identity.Erase` taking somebody's last provider when all
they have left is a seed, which is a refusal where there used to be none.

### Two refusals that were leaks

**An edge may not reach a row you cannot see.** The gate checked the edges an
`Add` hangs off, and checked only the path to the tenant — so `Email.vouched_by`
could be pointed at another tenant's identity and read back through a nested
select. A clerk with `Email.Add` and `Email.Get` read another customer's holder
and tenant. Fixed in payday's generator, so every edge behind the wall is now
checked at `Add`, and again at `Patch` for the ones that can move.

Nothing legitimate is affected: a write inside your own tenant passes exactly as
before, and the `Ungated` stack carries no gate at all, so seeds, `init` and the
CLI are untouched. What is refused is an edge that leaves the caller's scope,
which was never a thing to do on purpose.

**Nobody asserts that an address has been checked.** `Email.Add` took
`date_verified` from the request. Nothing reads the column yet, so this changes
no behaviour you can observe — but if you have a tool that seeds addresses
*through a credentialled call* and sets it, that call now returns
`InvalidArgument`. Seeding without a frame — `init`, a shell, `Ungated` — is
unaffected. Check with:

```sql
SELECT count(*) FROM email WHERE date_verified IS NOT NULL;
```

Rows here were written by somebody rather than by a verification. They are left
alone; nothing reads them.

### New surface you can ignore until you want it

- **`audit:`** — how long the trail is kept, per kind. Absent is **forever**,
  which is what every deployment had before, so leaving it out changes nothing.
  `docs/operating.md`, "How long the trail is kept".
- **`holder:`** — how long after an erase somebody is destroyed. Absent is
  **never**, again what you had.
- **`roster trail`**, **`roster forget`**, **`roster restore`** — commands, and
  nothing runs them for you.
- **`IssueService` on the data plane** — a customer minting their own `rt_`.
  Additive: no existing caller sees a difference, and a deployment with no
  `control:` reads no keys anyway.
- **`SyncService.Watch`** — one stream an app holds open to hear that somebody
  was suspended, signed out everywhere, or erased. Nothing changes until an app
  calls it, and calling it needs `/roster.SyncService/Watch` on that app's key
  and a **broker**: `watch.broker: none` answers `Unimplemented` rather than
  opening a stream that never carries anything, and `memory` is right for one
  replica and silently wrong for two. `docs/operating.md`, "How an app hears
  about it".
- **`MeService.IssueKey` and `/RevokeKey`**, and `MeGetResponse.keys` — a
  person minting and revoking their own `rt_`. Both need a role naming them,
  so nobody gains anything until you grant it, and `MeService/IssueKey` is
  refused for methods its caller does not hold like every other grant.

`audit:` and `holder:` are the two new **configuration** blocks, and each is
refused at startup if it names a window with nowhere to put what leaves it.
That refusal is the point; see operating.md. Everything else above is surface
that sits there until something calls it.

### And what a downstream app might notice

- The **outbox** no longer carries `secret:` columns in its `patch` documents.
  If you drain it into something that expected a verifier there — nothing
  should — it is gone.
- `Audit.domain` appears in the generated TypeScript (`ts/gen`). Additive, and
  so are `SyncService`, `SyncEvent` and the three new `MeService` shapes.
- **`MeGetResponse` grew `keys`.** An app decoding it into a hand-written type
  ignores the field; one that round-trips the message and asserts on its whole
  contents will see it. `examples/sso` is the pattern either way — it mirrors
  the shape it reads rather than sharing a type, so what roster answers with is
  roster's to change.
- If you copied `examples/sso`'s **delegation method list**, it grew by two
  (`MeService/IssueKey`, `/RevokeKey`). A delegation narrows to the
  intersection of what the app asks for and what the person holds, so asking
  for less than you draw is what makes a button refuse when pressed — and
  asking for more than you draw costs nothing.

## Checking before you commit to it

```sh
go tool pd doctor .              # what would go wrong, before it does
roster config                    # the configuration as it was actually read
roster trail prune --dry-run --older-than 8760h   # counts, destroys nothing
roster forget --dry-run          # who a grace period would reach
```

Every destructive command here has a `--dry-run`, and none of them is run by
anything but you or the sweep you configured.
