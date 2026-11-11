#!/usr/bin/env bash
# An upgrade, rehearsed: the databases one build made, brought up by another.
#
# Every other gate migrates a database that is empty, and the failure an upgrade
# has is one an empty database cannot have: a column that comes to a table that
# already has rows. Three NOT NULL columns with no default came between two
# releases of the one deployment, and its init container would not have
# finished -- SQLite refuses `ADD COLUMN … NOT NULL` with nothing to put in the
# rows already there, as Postgres does for any table that is not empty. Every
# gate was green the whole time, because none of them had seen a row.
#
# So BEFORE makes what a deployment has: the declared rows, the Login App's
# provision, a start. Then this puts a row into every table still empty -- the
# shape of the data does not matter to a migration, only that there is some --
# and NOW starts on the result twice, the way the init container and `serve` do
# on every start.
#
#     ./scripts/upgrade.sh BEFORE NOW
#
# Each is a directory with a build's `roster` in it. CI's `upgrade` job runs
# this twice (`.github/workflows/ci.yml`): from the build a change comes after
# -- a pull request's base, or what `main` was before a push -- and from the
# oldest build a deployment runs, OLDEST, which the job names. At a desk a
# build is a worktree and a `go build`:
#
#     git worktree add --detach /tmp/before <commit>
#     (cd /tmp/before && go build -o /tmp/bin-before/ ./cmd/roster)
#     go build -o /tmp/bin-now/ ./cmd/roster
#     ./scripts/upgrade.sh /tmp/bin-before /tmp/bin-now
#
# BEFORE and NOW are of one compatibility epoch (`.github/compat-epoch`). Until
# the first app launches, a change that breaks what an earlier build stored,
# exported, issued or was configured with moves the epoch rather than carrying a
# way forward (CLAUDE.md), and the job builds no BEFORE of another epoch: it
# says why and passes, since what this would find there is what was meant.
#
# SQLite, because that is what the deployment it was written for runs, and what
# fills the empty tables is SQLite's own catalogue.
set -o errexit
set -o pipefail

before="${1:?usage: scripts/upgrade.sh BEFORE NOW}"
now="${2:?usage: scripts/upgrade.sh BEFORE NOW}"
for b in "${before}" "${now}"; do
	if [ ! -x "${b}/roster" ]; then
		echo "upgrade: ${b} has no roster: BEFORE and NOW are each a directory with a build's roster in it" >&2
		exit 2
	fi
done
before="$(cd "${before}" && pwd)"
now="$(cd "${now}" && pwd)"

work="$(mktemp -d)"
pids=()
cleanup() {
	kill "${pids[@]}" 2>/dev/null || true
	wait 2>/dev/null || true
	rm -rf "${work}"
}
trap cleanup EXIT

die() {
	echo "upgrade: $1" >&2
	exit 1
}

# A deployment, as little of one as starts: both planes on SQLite, the Login App
# with its key from `login provision`, and a tenant with a name and a
# directory. Every listener on port 0, so nothing here collides with anything
# on the machine. Hydra is nowhere, which nothing in a start asks about.
mkdir -p "${work}/data" "${work}/control" "${work}/run"
pragmas='_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_txlock=immediate&_timefmt=auto'
cat >"${work}/resources.yaml" <<EOF
resources:
  - kind: Tenant
    alias: acme
    name: Acme
  - kind: Connection
    tenant: acme
    name: idp
    issuer: https://idp.example.test
    client_id: roster
    scopes: [email, profile]
  - kind: Host
    tenant: acme
    name: acme.example.test
EOF
cat >"${work}/config.yaml" <<EOF
resources: [${work}/resources.yaml]
server:
  addr: 127.0.0.1:0
db:
  driver: sqlite3
  dsn: "file:${work}/data/roster.db?${pragmas}"
  migrate: true
watch:
  broker: memory
spin:
  lease: local
control:
  db:
    driver: sqlite3
    dsn: "file:${work}/control/roster-control.db?${pragmas}"
    migrate: true
  watch:
    broker: memory
  addr: 127.0.0.1:0
login:
  addr: 127.0.0.1:0
  insecure: true
  hydra:
    admin: http://127.0.0.1:9
  key: file:${work}/run/login-app.key
  base: https://login.example.test
  remember: 8h
EOF

# What a deployment hands the process rather than writes in the file.
export ROSTER_VOUCH_KEYS="one:$(head -c 32 /dev/urandom | base64)"

# start runs `serve` from one binary until it is answering, and stops it. A
# process that exits first is a start that failed, and its log says why.
start() {
	local bin="$1" log="$2"
	"${bin}" --config "${work}/config.yaml" serve >"${log}" 2>&1 &
	local pid=$!
	pids+=("${pid}")

	local i=0
	until grep -q 'grpc - addr' "${log}"; do
		kill -0 "${pid}" 2>/dev/null || { cat "${log}" >&2; die "${bin} did not start"; }
		i=$((i + 1))
		[ "${i}" -lt 60 ] || { cat "${log}" >&2; die "${bin} did not answer within a minute"; }
		sleep 1
	done

	# And it is still up a moment later, past whatever runs once it is.
	sleep 2
	kill -0 "${pid}" 2>/dev/null || { cat "${log}" >&2; die "${bin} stopped after it started"; }
	kill "${pid}" 2>/dev/null || true
	wait "${pid}" 2>/dev/null || true
}

provision() {
	local bin="$1" log="$2"
	"${bin}" --config "${work}/config.yaml" login provision --out "${work}/run" >"${log}" 2>&1 \
		|| { cat "${log}" >&2; die "${bin} login provision failed"; }
}

echo "== ${before} makes a deployment"
# A first start applies the declared rows, which is what gives `provision` a
# tenant to provision in; then a start the way every one after it goes.
start "${before}/roster" "${work}/old-0.log"
provision "${before}/roster" "${work}/old-1.log"
start "${before}/roster" "${work}/old-2.log"

echo "== and a row in every table it left empty"
python3 - "${work}/data/roster.db" "${work}/control/roster-control.db" <<'PY'
import sqlite3, sys, uuid

# One row per empty table, made from the catalogue: whatever a NOT NULL column
# without a default needs, by its declared type, and a parent's key for a
# foreign one. Nothing reads these rows but the migration, and a migration
# cares that they are there, not what they say.
def value(decl):
    t = (decl or "").lower()
    if "uuid" in t:
        return str(uuid.uuid4())
    if "int" in t or "bool" in t:
        return 0
    if "real" in t or "double" in t or "float" in t or "decimal" in t or "numeric" in t:
        return 0.0
    if "date" in t or "time" in t:
        return "2026-01-01T00:00:00Z"
    if "json" in t:
        return b"null"
    if "blob" in t or "byte" in t:
        return b"\x00"
    return "x"

for path in sys.argv[1:]:
    db = sqlite3.connect(path)
    db.execute("PRAGMA foreign_keys = ON")
    tables = [r[0] for r in db.execute(
        "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name")]
    parents = {t: {r[2] for r in db.execute(f'PRAGMA foreign_key_list("{t}")') if r[2] != t} for t in tables}

    filled, order = set(), []
    while len(order) < len(tables):
        ready = [t for t in tables if t not in filled and parents[t] <= filled | {p for p in parents[t] if p not in tables}]
        if not ready:
            sys.exit(f"upgrade: {path}: the foreign keys among {sorted(set(tables) - filled)} go round")
        for t in ready:
            filled.add(t)
            order.append(t)

    seeded = 0
    for t in order:
        if db.execute(f'SELECT count(*) FROM "{t}"').fetchone()[0] > 0:
            continue
        fks = {r[3]: (r[2], r[4]) for r in db.execute(f'PRAGMA foreign_key_list("{t}")')}
        names, values, own = [], [], {}
        for _, name, decl, notnull, default, pk in db.execute(f'PRAGMA table_info("{t}")'):
            if name in fks:
                parent, key = fks[name]
                if parent == t:
                    continue
                row = db.execute(f'SELECT "{key}" FROM "{parent}" LIMIT 1').fetchone()
                if row is None:
                    if notnull:
                        sys.exit(f"upgrade: {path}: {t}.{name} needs a row of {parent}, which has none")
                    continue
                v = row[0]
            elif pk or (notnull and default is None):
                v = value(decl)
            else:
                continue
            own[name] = v
            names.append(f'"{name}"')
            values.append(v)
        # A key that names its own row, which is the one a self-reference can.
        for _, _, parent, name, key, *_ in db.execute(f'PRAGMA foreign_key_list("{t}")'):
            if parent == t and key in own:
                names.append(f'"{name}"')
                values.append(own[key])
        db.execute(f'INSERT INTO "{t}" ({", ".join(names)}) VALUES ({", ".join("?" * len(values))})', values)
        seeded += 1
    db.commit()
    print(f"   {path.rsplit('/', 1)[-1]}: {seeded} of {len(tables)} tables were empty")
PY

echo "== ${now} starts on them, twice"
for n in 1 2; do
	provision "${now}/roster" "${work}/new-${n}-provision.log"
	start "${now}/roster" "${work}/new-${n}-serve.log"
	echo "   start ${n}: ok"
done

echo "upgrade: ok, from ${before} to ${now}"
