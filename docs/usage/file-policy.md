# Permissions from a file

Set `policy.file` to add a read-only source of tenant-wide method permissions:

```yaml
policy:
  file: /etc/roster/policy.yaml
  refresh: 5s
```

The file uses payday's shared roles, groups and bindings. It does not create
database roles or memberships. Unlike `resources`, removing a binding from a
valid file removes that source's grant on refresh.

Use immutable tenant and holder UUIDs in the names below. Roles and groups have
readable names within the tenant UUID namespace. Holder subjects are
`@<tenant UUID>/<holder UUID>`; aliases are rejected. A user who can rename a
holder or take its old alias must not acquire deployment-owned permissions.

```yaml
roles:
  '@0199e000-0000-8000-8001-000000000001/reader':
    methods:
      - /roster.HolderService/Get
      - /roster.HolderService/List
groups:
  '@0199e000-0000-8000-8001-000000000001/readers':
    members:
      - '@0199e000-0000-8000-8001-000000000001/0199e000-0000-8000-8002-000000000002'
bindings:
  - role: '@0199e000-0000-8000-8001-000000000001/reader'
    group: '@0199e000-0000-8000-8001-000000000001/readers'
```

Replace the illustrative IDs with the IDs returned by your tenant and holder
reads. Each holder is checked against the database when the policy is accepted and
must already exist in that tenant. Bindings can name `holder`
instead of `group`, but never both. Groups do not nest, and membership alone
grants nothing. Every reference remains in its tenant. Database membership in
a similarly named group does not change this file's membership.

The file and database grants form a union. Removing a file binding does not
revoke an independent database binding. File permissions reach the whole
tenant, while existing site-scoped database bindings retain their scope. Token
restrictions still narrow the resulting permission. Methods follow roster's
usual patterns, including `/roster.HolderService/*` and `/*.*/*`.

The gate, scope calculation, delegation checks, credential-target checks and
`Holder.Reaches` all consult the same sources. Directory users still need their
rows and credentials; this file supplies authorization, not authentication.
`TokenService.Introspect` continues to describe credential restrictions;
consumers that need role-derived permissions read `Holder.Reaches.everywhere`.

The first document must be valid before the server starts. Refresh uses two
consecutive identical reads, replacing the complete policy only after
validation. Malformed or missing files keep the last valid policy and log an
error. A valid empty policy removes all file grants. Every replica refreshes
independently, so updates propagate after its own successful read. Requests
pin one policy revision; a changed policy closes existing data-plane streams
with `Unauthenticated` so reconnecting clients are checked again.

This source applies to the data plane. It does not change control-plane
operator permissions. Use an absolute path, or one relative to the server's
working directory. The policy itself provisions no rows. This payday upgrade
also regenerates its existing outbox support with a nullable event column; run
the normal schema migration before serving the upgraded binary.
