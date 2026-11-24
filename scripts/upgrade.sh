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
# So BEFORE makes what a deployment has: an operator (`init`), the declared
# rows, the Login App's provision, a person with a password and a key, a start,
# and the trail moved into an archive. Then this puts a row into every table
# still empty -- the shape of the data does not matter to a migration, only that
# there is some -- and NOW starts on the result twice, the way the init
# container and `serve` do on every start. And then it reads back what an
# upgrade must not lose: NOW serving, the key answers `MeService/Get` and the
# person's password verifies, where a wrong one does not; the operator is
# still somebody `--as` names; the person's name, a column of a table that
# changed since OLDEST, reads back as BEFORE wrote it; and `roster trail verify
# --full` finds the archive BEFORE wrote as the account says.
#
# What it does not prove: anything of PostgreSQL, which a deployment of two
# replicas runs on -- the type of a column changed under rows, which SQLite
# takes and PostgreSQL refuses, is not seen here; a unique index over rows
# that repeat, since a table here holds one made-up row or a few real ones and
# nothing repeats; a default changed, or a column renamed that nobody
# declared, unless it is one of the few values read back; and what NOW does
# with the old rows once it serves, beyond the calls above.
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
# with its key from `login provision`, a tenant with a name and a directory,
# and an archive of the trail with its account signed and heard by the log.
# Every listener on port 0, so nothing here collides with anything on the
# machine. Hydra is nowhere, which nothing in a start asks about.
mkdir -p "${work}/data" "${work}/control" "${work}/run" "${work}/archive"
openssl genpkey -algorithm ed25519 -out "${work}/run/checkpoints.pem" 2>/dev/null \
	|| die "openssl could not make the key the archive's account is signed with"
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
  key: \${file:${work}/run/login-app.key}
  base: https://login.example.test
  remember: 8h
audit:
  archive: ${work}/archive
  checkpoints:
    key: ${work}/run/checkpoints.pem
    witnesses: ["log:"]
EOF

# What a deployment hands the process rather than writes in the file.
export ROSTER_VOUCH_KEYS="one:$(head -c 32 /dev/urandom | base64)"

# serving runs `serve` from one binary until it is answering, and leaves it
# running: its process is `served`, and the address of its data plane
# `answering`. A process that exits first is a start that failed, and its log
# says why.
serving() {
	local bin="$1" log="$2"
	"${bin}" --config "${work}/config.yaml" serve >"${log}" 2>&1 &
	served=$!
	pids+=("${served}")

	local i=0
	until grep -q 'grpc - addr' "${log}"; do
		kill -0 "${served}" 2>/dev/null || { cat "${log}" >&2; die "${bin} did not start"; }
		i=$((i + 1))
		[ "${i}" -lt 60 ] || { cat "${log}" >&2; die "${bin} did not answer within a minute"; }
		sleep 1
	done
	answering="$(grep -o 'grpc - addr=[^ ]*' "${log}" | head -n 1)"
	answering="${answering#grpc - addr=}"
}

stop() {
	kill "${served}" 2>/dev/null || true
	wait "${served}" 2>/dev/null || true
}

# start runs `serve` from one binary until it is answering, and stops it.
start() {
	local bin="$1" log="$2"
	serving "${bin}" "${log}"

	# And it is still up a moment later, past whatever runs once it is.
	sleep 2
	kill -0 "${served}" 2>/dev/null || { cat "${log}" >&2; die "${bin} stopped after it started"; }
	stop
}

# at runs one command of a build at the box, as the operator `init` made.
at() {
	local bin="$1"
	shift
	"${bin}" --config "${work}/config.yaml" --as admin "$@"
}

# over runs one command of a build over the wire, to the process `serving`
# started, with the key BEFORE minted.
over() {
	local bin="$1"
	shift
	cat >"${work}/client.yaml" <<EOF
client:
  addr: "${answering}"
  insecure: true
  auth:
    scheme: bearer
    credential: \${file:${work}/run/kept.key}
EOF
	"${bin}" --config "${work}/client.yaml" "$@"
}

# What BEFORE writes and NOW reads back. The password is chosen here rather
# than generated, so that NOW can be asked it.
password='correct horse battery upgraded'
name='Kim, kept through an upgrade'

provision() {
	local bin="$1" log="$2"
	"${bin}" --config "${work}/config.yaml" login provision --out "${work}/run" >"${log}" 2>&1 \
		|| { cat "${log}" >&2; die "${bin} login provision failed"; }
}

echo "== ${before} makes a deployment"
# The operator first, as `docs/operating.md` puts a deployment up: OLDEST's
# `init` after `login provision` is refused, `Role already exists`. Its
# password is this one's, read from stdin, since what `init` generates is
# printed in words that changed between builds.
printf '%s' "${password}" | "${before}/roster" --config "${work}/config.yaml" init --password-stdin >"${work}/old-init.log" 2>&1 \
	|| { cat "${work}/old-init.log" >&2; die "${before} init failed"; }

# A first start applies the declared rows, which is what gives `provision` a
# tenant to provision in; then a start the way every one after it goes.
start "${before}/roster" "${work}/old-0.log"
provision "${before}/roster" "${work}/old-1.log"

# A person with a password and a name, and a key: each by BEFORE's own
# commands, all of which OLDEST has. The key is the tenant administrator's,
# who holds what it allows -- a key hands out nothing its holder does not --
# and asks after a password as a front door does.
at "${before}/roster" holder add @acme/kim "{\"name\":\"${name}\"}" >"${work}/old-kim.log" 2>&1 \
	|| { cat "${work}/old-kim.log" >&2; die "${before} could not add a person"; }
printf '%s' "${password}" | at "${before}/roster" vouch set --password-stdin @acme/kim >>"${work}/old-kim.log" 2>&1 \
	|| { cat "${work}/old-kim.log" >&2; die "${before} could not set a password"; }
at "${before}/roster" key add --tenant acme --holder admin --name kept \
	--allow /roster.MeService/Get,/roster.VouchService/Verify 2>"${work}/old-key.log" | tr -d '\n' >"${work}/run/kept.key" \
	|| { cat "${work}/old-key.log" >&2; die "${before} could not mint a key"; }
[ -s "${work}/run/kept.key" ] || { cat "${work}/old-key.log" >&2; die "${before} printed no key"; }

start "${before}/roster" "${work}/old-2.log"

# And the trail, written so far, into the archive: what NOW is to find as the
# account BEFORE kept of it says. Up to a second ago, since `--older-than`
# refuses a window of nothing.
sleep 1
at "${before}/roster" trail prune --before "$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"${work}/old-prune.log" 2>&1 \
	|| { cat "${work}/old-prune.log" >&2; die "${before} could not move the trail into its archive"; }

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

echo "== and reads back what ${before} wrote"
serving "${now}/roster" "${work}/new-3-serve.log"
over "${now}/roster" me get >"${work}/new-me.log" 2>&1 \
	|| { cat "${work}/new-me.log" >&2; die "the key ${before} minted is not answered by MeService/Get"; }
grep -q '^alias[[:space:]]*admin$' "${work}/new-me.log" \
	|| { cat "${work}/new-me.log" >&2; die "the key ${before} minted is answered as somebody else"; }
echo "   the key: answered"
printf '%s' "${password}" | over "${now}/roster" vouch verify --secret-stdin @acme/kim >"${work}/new-verify.log" 2>&1 \
	|| { cat "${work}/new-verify.log" >&2; die "the password ${before} set does not verify"; }
if printf '%s' "not ${password}" | over "${now}/roster" vouch verify --secret-stdin @acme/kim >>"${work}/new-verify.log" 2>&1; then
	die "a password ${before} never set verifies"
fi
echo "   the password: verifies, and another does not"
stop

at "${now}/roster" key list >"${work}/new-operator.log" 2>&1 \
	|| { cat "${work}/new-operator.log" >&2; die "the operator ${before} made is not somebody --as names"; }
echo "   the operator: there"

# A column the person's table had when BEFORE wrote it, read back by NOW. The
# table took three columns between OLDEST and this, which on SQLite may be a
# table made again and its rows copied.
kept="$(at "${now}/roster" holder get -o json @acme/kim 2>"${work}/new-kim.log" | jq -r .name)" \
	|| { cat "${work}/new-kim.log" >&2; die "${now} cannot read the person ${before} made"; }
[ "${kept}" = "${name}" ] || die "the person's name was \"${name}\" and reads back \"${kept}\""
echo "   the person's name: as it was written"

at "${now}/roster" trail verify --full >"${work}/new-trail.log" 2>&1 \
	|| { cat "${work}/new-trail.log" >&2; die "the archive ${before} wrote is not what its account says"; }
echo "   the trail's archive: as its account says"

echo "upgrade: ok, from ${before} to ${now}"
