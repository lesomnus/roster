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

### New surface you can ignore until you want it

- **`audit:`** — how long the trail is kept, per kind. Absent is **forever**,
  which is what every deployment had before, so leaving it out changes nothing.
  `docs/OPERATING.md`, "How long the trail is kept".
- **`holder:`** — how long after an erase somebody is destroyed. Absent is
  **never**, again what you had.
- **`roster trail`**, **`roster forget`**, **`roster restore`** — commands, and
  nothing runs them for you.
- **`IssueService` on the data plane** — a customer minting their own `rt_`.
  Additive: no existing caller sees a difference, and a deployment with no
  `control:` reads no keys anyway.

Both new blocks are refused at startup if they name a window with nowhere to
put what leaves it. That refusal is the point; see OPERATING.md.

### And what a downstream app might notice

- The **outbox** no longer carries `secret:` columns in its `patch` documents.
  If you drain it into something that expected a verifier there — nothing
  should — it is gone.
- `Audit.domain` appears in the generated TypeScript (`ts/gen`). Additive.

## Checking before you commit to it

```sh
go tool pd doctor .              # what would go wrong, before it does
roster config                    # the configuration as it was actually read
roster trail prune --dry-run --older-than 8760h   # counts, destroys nothing
roster forget --dry-run          # who a grace period would reach
```

Every destructive command here has a `--dry-run`, and none of them is run by
anything but you or the sweep you configured.
