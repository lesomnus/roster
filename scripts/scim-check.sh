#!/usr/bin/env bash
# A roster a directory's own validator can be pointed at, for as long as
# somebody is pointing it: stood up from nothing, reached through a tunnel, and
# everything it was asked kept.
#
# Microsoft's SCIM validator is a page Microsoft hosts
# (`docs/scim.md` § "Microsoft's validator"). An address and a token go into a
# form and a person reads the result, so nothing of it runs in CI -- what it
# asks is asked there by `TestWhatMicrosoftsValidatorAsks`, from its tutorial.
# This is for what the tutorial does not say: it gives that page somewhere to
# call, and writes down what it sent and what it was answered, so that the
# difference becomes a fixture rather than something somebody remembers.
#
# It builds the binary, makes a tenant nobody is in and the key its directory
# would hold (`roster scim provision`), starts `roster serve` with the SCIM
# listener, and puts two things in front of it:
#
#   - **a recorder**, which is the only thing the tunnel reaches. It asks for
#     the token this was handed, swaps it for the tenant's own key -- which so
#     never leaves this machine, and is printed nowhere -- and appends each
#     request and its answer to `requests.jsonl`, without the token;
#   - **a tunnel**, `cloudflared`, to the recorder.
#
# Then it waits: until nothing has been asked for a while, or the time is up.
#
#     SCIM_CHECK_TOKEN=…  SCIM_CHECK_CF_API_TOKEN=…  SCIM_CHECK_ZONE=example.com \
#       SCIM_CHECK_ZONE_ID=…  SCIM_CHECK_ACCOUNT_ID=…  ./scripts/scim-check.sh
#
#   SCIM_CHECK_TOKEN         what goes in the validator's token field. Made up
#                            when unset, and then printed with the address --
#                            which is how the workflow runs it, so that there
#                            is nothing to keep between runs and nothing to
#                            remember. A log anybody may read then holds it:
#                            see "How much this takes", in the recorder.
#
# The tunnel, one of three ways:
#
#   SCIM_CHECK_CF_API_TOKEN  **a name of its own each time.** A Cloudflare API
#                            token (Account: Cloudflare Tunnel: Edit; Zone:
#                            DNS: Edit, of the one zone). A tunnel and a record
#                            `scim-check-<made up>.<zone>` are made for this
#                            run and taken away when it ends, so nothing
#                            answers at a name between runs, and no run's
#                            name is the last one's.
#     SCIM_CHECK_ZONE          the zone, by its name: the name is one label
#                              under it, which the zone's own certificate
#                              covers.
#     SCIM_CHECK_ZONE_ID       and by its identifier, and the account's --
#     SCIM_CHECK_ACCOUNT_ID    both on the zone's overview page.
#
#   SCIM_CHECK_TUNNEL_TOKEN  a tunnel made beforehand, by its token, whose
#                            public hostname is routed to
#                            http://localhost:18193;
#     SCIM_CHECK_HOST          that hostname.
#
#   SCIM_CHECK_QUICK=1       a tunnel with no account at all, at a name of
#                            Cloudflare's choosing under `trycloudflare.com`.
#
# What the first costs is what its token can do: write any record of the zone.
# So the zone it is given is one kept for this -- not the one a deployment's
# names are in.
#
#   SCIM_CHECK_MINUTES       the most it waits; 30.
#   SCIM_CHECK_IDLE          minutes with nothing asked, after something was,
#                            that end it early; 10.
#   SCIM_CHECK_OUT           where `requests.jsonl` and `roster.log` are left;
#                            ./scim-check.
#
# With no tunnel named it serves the recorder on this machine alone, which is
# how to see what it does: `curl -H "Authorization: Bearer $SCIM_CHECK_TOKEN"
# http://127.0.0.1:18193/scim/v2/Users`.
#
#     ./scripts/scim-check.sh --down    # take away what a run that was stopped left
#     ./scripts/scim-check.sh --sweep   # and what any run left, hours ago
#
# And what a run kept, as the test that replays it reads it
# (`scim/validator_sent_test.go`, where what to add to a row is said):
#
#     ./scripts/scim-check.sh --fixture scim-check/requests.jsonl
#
# `.github/workflows/scim-check.yml` runs it on a runner, by hand.
set -o errexit
set -o pipefail

__root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${__root}"

: "${SCIM_CHECK_MINUTES:=30}"
: "${SCIM_CHECK_IDLE:=10}"
: "${SCIM_CHECK_OUT:=${__root}/scim-check}"

# Which tunnel and which record this run made, by their identifiers and
# nothing else: what `--down` reads when the run that made them was stopped
# before it could take them away. Not where the answers are kept, since that
# is published and this is nobody's business.
: "${SCIM_CHECK_STATE:=${TMPDIR:-/tmp}/scim-check.cloudflare.json}"
export SCIM_CHECK_STATE

# cf is Cloudflare's API, for the three things the first way to a tunnel asks
# of it: `up HOST SERVICE` makes a tunnel that routes HOST to SERVICE and the
# record that names it, and prints the tunnel's token; `down` takes away what
# `up` made; `sweep` takes away what any run made more than three hours ago and
# left, by the name each was made under.
#
# The token it is called with is in neither what it prints nor what it keeps.
cf() {
	python3 - "$@" <<'PY'
import datetime, json, os, secrets, sys, time, urllib.error, urllib.request

API = os.environ.get("SCIM_CHECK_CF_API", "https://api.cloudflare.com/client/v4")
token = os.environ["SCIM_CHECK_CF_API_TOKEN"]
account, zone = os.environ["SCIM_CHECK_ACCOUNT_ID"], os.environ["SCIM_CHECK_ZONE_ID"]
state = os.environ["SCIM_CHECK_STATE"]
MARK = "scim-check-"


def call(method, path, body=None, fine=()):
    req = urllib.request.Request(API + path, method=method, data=None if body is None else json.dumps(body).encode())
    req.add_header("Authorization", "Bearer " + token)
    req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=30) as res:
            return json.load(res)
    except urllib.error.HTTPError as e:
        if e.code in fine:
            return None
        try:
            said = "; ".join(str(x.get("message", x)) for x in json.load(e).get("errors", []))
        except Exception:
            said = ""
        raise SystemExit(f"cloudflare: {method} {path.split('?')[0]} was answered {e.code}: {said}")
    except urllib.error.URLError as e:
        raise SystemExit(f"cloudflare: {method} {path.split('?')[0]} was not answered: {e.reason}")


def keep(made):
    with open(state, "w") as f:
        json.dump(made, f)


def gone(tunnel):
    # A tunnel is deleted with nothing connected to it, and what was
    # connected a moment ago takes a moment to be nothing.
    call("DELETE", f"/accounts/{account}/cfd_tunnel/{tunnel}/connections", fine=range(400, 500))
    for attempt in range(6):
        try:
            call("DELETE", f"/accounts/{account}/cfd_tunnel/{tunnel}", fine=(404,))
            return
        except SystemExit:
            if attempt == 5:
                raise
            time.sleep(2)


def up(host, service):
    name = MARK + datetime.datetime.now(datetime.timezone.utc).strftime("%Y%m%d-%H%M%S-") + secrets.token_hex(3)
    t = call("POST", f"/accounts/{account}/cfd_tunnel", {"name": name, "config_src": "cloudflare"})["result"]
    keep({"tunnel": t["id"]})
    call("PUT", f"/accounts/{account}/cfd_tunnel/{t['id']}/configurations", {"config": {"ingress": [
        {"hostname": host, "service": service, "originRequest": {}},
        {"service": "http_status:404"},
    ]}})
    r = call("POST", f"/zones/{zone}/dns_records", {
        "type": "CNAME", "proxied": True, "ttl": 1,
        "name": host, "content": t["id"] + ".cfargotunnel.com", "comment": name,
    })["result"]
    keep({"tunnel": t["id"], "record": r["id"]})
    print(t["token"])


def down():
    try:
        with open(state) as f:
            made = json.load(f)
    except FileNotFoundError:
        return
    if made.get("record"):
        call("DELETE", f"/zones/{zone}/dns_records/{made['record']}", fine=(404,))
    if made.get("tunnel"):
        gone(made["tunnel"])
    os.remove(state)


def old(stamp):
    at = datetime.datetime.fromisoformat(stamp.replace("Z", "+00:00"))
    return datetime.datetime.now(datetime.timezone.utc) - at > datetime.timedelta(hours=3)


def sweep():
    n = 0
    for r in call("GET", f"/zones/{zone}/dns_records?type=CNAME&per_page=100")["result"]:
        if (r.get("comment") or "").startswith(MARK) and old(r["created_on"]):
            call("DELETE", f"/zones/{zone}/dns_records/{r['id']}", fine=(404,))
            n += 1
    for t in call("GET", f"/accounts/{account}/cfd_tunnel?is_deleted=false&per_page=100")["result"]:
        if t.get("name", "").startswith(MARK) and old(t["created_at"]):
            gone(t["id"])
            n += 1
    print(f"   {n} left by a run more than three hours ago, taken away" if n else "   nothing left by an earlier run")


{"up": up, "down": down, "sweep": sweep}[sys.argv[1]](*sys.argv[2:])
PY
}

# fixture is a run's record with what a replay does not read left out: a
# request a line -- what was asked, what was answered, and of the answer only
# what a later request goes by, which is the id of what it made and how many
# it found. A request that did not carry the token is nobody's the page sent,
# and is left out too.
#
# The record is what anybody sent, so it is read as text and nothing in it is
# run.
fixture() {
	python3 -I - "$1" <<'PY'
import json
import sys

for line in open(sys.argv[1], encoding="utf-8"):
    line = line.strip()
    if not line:
        continue
    r = json.loads(line)
    if r.get("refused"):
        continue

    out = {"method": r["method"], "path": r["path"]}
    asked = r.get("request") or ""
    if asked.strip():
        try:
            out["body"] = json.loads(asked)
        except ValueError:
            out["text"] = asked
    out["answered"] = r["status"]

    try:
        said = json.loads(r.get("response") or "")
    except ValueError:
        said = None
    if isinstance(said, dict):
        if r["method"] == "POST" and r["status"] == 201 and said.get("id"):
            out["made"] = said["id"]
        if isinstance(said.get("totalResults"), int):
            out["found"] = said["totalResults"]

    print(json.dumps(out, ensure_ascii=False, separators=(",", ":")))
PY
}

case "${1:-}" in
--fixture)
	[ -n "${2:-}" ] || { echo "--fixture: which record; a run leaves it at scim-check/requests.jsonl" >&2; exit 2; }
	fixture "$2"
	exit 0
	;;
--down)
	[ -n "${SCIM_CHECK_CF_API_TOKEN:-}" ] && cf down
	exit 0
	;;
--sweep)
	cf sweep
	exit 0
	;;
esac

work="$(mktemp -d)"
pids=()
made=0
napping=""
cleanup() {
	# The sleep a wait was on first, and then only what this started is waited
	# for: waiting for everything waited out that sleep, a minute a run that
	# was cancelled does not have.
	if [ -n "${napping}" ]; then
		kill "${napping}" 2>/dev/null || true
	fi
	for pid in "${pids[@]}"; do
		kill "${pid}" 2>/dev/null || true
	done
	if [ "${#pids[@]}" -gt 0 ]; then
		wait "${pids[@]}" 2>/dev/null || true
	fi
	# After the tunnel is stopped, since one with something connected to it
	# is not deleted.
	if [ "${made}" = 1 ]; then
		cf down || echo "the tunnel or its record was not taken away; \`./scripts/scim-check.sh --down\` does" >&2
	fi
	rm -rf "${work}"
}
trap cleanup EXIT
# Stopped is ended, and ending is what takes away what this made: a run that
# is cancelled is sent these, and has seconds.
trap 'exit 130' INT TERM

# nap sleeps where a signal can end it. A shell waiting on `sleep` hears a
# signal when the sleep is over, which is a minute a cancelled run does not
# have.
nap() {
	sleep "$1" &
	napping="$!"
	wait "${napping}" || true
	napping=""
}

out="${SCIM_CHECK_OUT}"
mkdir -p "${out}"
: > "${out}/requests.jsonl"

printed=0
if [ -z "${SCIM_CHECK_TOKEN:-}" ]; then
	SCIM_CHECK_TOKEN="check-$(head -c 24 /dev/urandom | base64 | tr -d '/+=')"
	printed=1
fi

echo "== build"
go build -o "${work}/roster" ./cmd/roster

# Ports nothing else on a desk is likely to be on. Only the recorder's is one
# anything outside is routed to, and that is why it is the one written down
# where a tunnel is set up.
data=18151 control=18161 scim=18192 recorder=18193
for port in "${data}" "${control}" "${scim}" "${recorder}"; do
	if (exec 3<>"/dev/tcp/127.0.0.1/${port}") 2>/dev/null; then
		echo "something is already listening on 127.0.0.1:${port}; stop it first" >&2
		exit 1
	fi
done

cat > "${work}/roster.yaml" <<YAML
db:
  driver: sqlite3
  dsn: "file:${work}/roster.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
  migrate: true
watch:
  broker: memory
spin:
  lease: local
server:
  addr: 127.0.0.1:${data}
host:
  resolver: none
control:
  db:
    driver: sqlite3
    dsn: "file:${work}/control.db?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)"
    migrate: true
  watch:
    broker: memory
  addr: 127.0.0.1:${control}
scim:
  addr: 127.0.0.1:${scim}
  insecure: true
vouch:
  keys: ["check:$(head -c 32 /dev/urandom | base64)"]
YAML
r() { "${work}/roster" --config "${work}/roster.yaml" "$@"; }

# A tenant nobody is in, and the connection its directory provisions through:
# an organisation's own door, naming people by `oid`, which is what a
# deployment's Entra connection is -- so the `externalId` the page is given has
# to be a GUID here as it has to be there.
echo "== a tenant, and the key its directory holds"
head -c 24 /dev/urandom | base64 | r init --operator admin --password-stdin >/dev/null
r --as admin tenant add @check '{"name":"Check"}' >/dev/null
r --as admin connection add '{
	"tenant": {"alias": "check"}, "name": "entra",
	"issuer": "https://login.microsoftonline.com/00000000-0000-4000-8000-000000000000/v2.0",
	"client_id": "scim-check", "subject_claim": "oid"
}' >/dev/null
key="$(r --as admin scim provision --tenant check --connection entra 2>/dev/null)"
case "${key}" in
rt_*) ;;
*)
	echo "no key was minted; \`roster scim provision\` said: ${key}" >&2
	exit 1
	;;
esac

echo "== roster, and the recorder in front of it"
# The binary itself and not `r`: a function sent to the background is a shell
# with roster under it, and the shell is all that stopping it stops -- roster
# stayed up, on these ports, after the first run of this was over.
env -u SCIM_CHECK_CF_API_TOKEN "${work}/roster" --config "${work}/roster.yaml" serve > "${out}/roster.log" 2>&1 &
pids+=("$!")

# The recorder. The tunnel reaches this and nothing else.
#
# It is the one place the two tokens meet: the one a person typed into the
# page, which this was handed, and the tenant's key, which was minted a moment
# ago and is forwarded in its place. Neither is written to the record.
#
# And it answers a browser's preflight, for the page's origin and no other,
# which roster's own listener does not: whether the page calls from the browser
# it is open in or from Microsoft's side is one of the things this is run to
# find out, and an `origin` on a recorded request is the answer.
env -u SCIM_CHECK_CF_API_TOKEN KEY="${key}" TOKEN="${SCIM_CHECK_TOKEN}" python3 - "${recorder}" "http://127.0.0.1:${scim}" "${out}/requests.jsonl" <<'PY' &
import datetime, hmac, http.server, json, os, sys, threading, urllib.error, urllib.request, uuid

port, upstream, path = int(sys.argv[1]), sys.argv[2], sys.argv[3]
key, token = os.environ["KEY"], os.environ["TOKEN"]
lock = threading.Lock()
PAGE = "https://scimvalidator.microsoft.com"

# How much this takes. The token for the page is in a log anybody may read
# when a runner made it up, so anybody may ask -- of a roster with nobody in
# it, which is the point of it -- and what they send is kept and published.
# So a request is no larger than roster itself would read, and the record no
# longer than a page run a hundred times over: after that it is asked no more.
MOST = 1 << 20
ROWS, BYTES = 5000, 16 << 20
kept = {"rows": 0, "bytes": 0}


class Recorder(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, *_):
        pass

    def cors(self):
        if self.headers.get("Origin") == PAGE:
            self.send_header("Access-Control-Allow-Origin", PAGE)
            self.send_header("Access-Control-Allow-Headers", "authorization, content-type")
            self.send_header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
            self.send_header("Vary", "Origin")

    def do_OPTIONS(self):
        self.send_response(204)
        self.cors()
        self.send_header("Content-Length", "0")
        self.end_headers()
        self.keep({"method": "OPTIONS", "status": 204})

    def keep(self, rec):
        rec = {
            "at": datetime.datetime.now(datetime.timezone.utc).isoformat(timespec="milliseconds"),
            "path": self.path,
            "origin": self.headers.get("Origin", ""),
            "user_agent": self.headers.get("User-Agent", ""),
            **rec,
        }
        line = json.dumps(rec, ensure_ascii=False) + "\n"
        with lock, open(path, "a") as f:
            f.write(line)
            kept["rows"] += 1
            kept["bytes"] += len(line)

    def refuse(self, status, detail):
        answered = json.dumps({
            "schemas": ["urn:ietf:params:scim:api:messages:2.0:Error"], "status": str(status), "detail": detail,
        }).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/scim+json")
        self.send_header("Content-Length", str(len(answered)))
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(answered)
        self.close_connection = True

    def answer(self):
        n = int(self.headers.get("Content-Length") or 0)
        if n > MOST:
            return self.refuse(413, "a request of at most a megabyte")
        with lock:
            full = kept["rows"] >= ROWS or kept["bytes"] >= BYTES
        if full:
            return self.refuse(429, "this check has been asked as much as it keeps")
        body = self.rfile.read(n) if n else b""

        scheme, _, given = self.headers.get("Authorization", "").partition(" ")
        if scheme.lower() != "bearer" or not hmac.compare_digest(given.strip().encode(), token.encode()):
            # Somebody who was not handed the token. Refused here, and nothing
            # of what they sent is kept.
            status, headers = 401, {"Content-Type": "application/scim+json", "WWW-Authenticate": 'Bearer realm="roster"'}
            answered = json.dumps({
                "schemas": ["urn:ietf:params:scim:api:messages:2.0:Error"],
                "status": "401", "detail": "the token this check was handed, as `Authorization: Bearer …`",
            }).encode()
            self.keep({"method": self.command, "status": status, "refused": True})
        else:
            # The one thing added to what was sent: an `externalId` on a person
            # being made without one. roster refuses that -- it is how a
            # sign-in would find them -- and a directory always sends one,
            # being what its mapping is for; the page sends only what its form
            # was given, and remembering to give it this is the step of running
            # it that is forgotten. What it sent is kept as it sent it.
            added = None
            if self.command == "POST" and self.path.split("?")[0].rstrip("/").lower().endswith("/users"):
                try:
                    said = json.loads(body)
                    if isinstance(said, dict) and not any(k.lower() == "externalid" and said[k] for k in said):
                        added = str(uuid.uuid4())
                        sent = json.dumps({**said, "externalId": added}).encode()
                    else:
                        sent = body
                except ValueError:
                    sent = body
            else:
                sent = body
            req = urllib.request.Request(upstream + self.path, data=sent if n else None, method=self.command)
            req.add_header("Authorization", "Bearer " + key)
            if self.headers.get("Content-Type"):
                req.add_header("Content-Type", self.headers["Content-Type"])
            try:
                with urllib.request.urlopen(req, timeout=60) as res:
                    status, headers, answered = res.status, res.headers, res.read()
            except urllib.error.HTTPError as e:
                status, headers, answered = e.code, e.headers, e.read()
            except Exception as e:  # roster is not answering, which is this check's to say
                status, headers = 502, {"Content-Type": "application/scim+json"}
                answered = json.dumps({"status": "502", "detail": "the check's roster did not answer: " + str(e)}).encode()
            self.keep({
                "method": self.command,
                "content_type": self.headers.get("Content-Type", ""),
                "request": body.decode("utf-8", "replace"),
                **({"added_external_id": added} if added else {}),
                "status": status,
                "response": answered.decode("utf-8", "replace"),
            })

        self.send_response(status)
        self.cors()
        for name in ("Content-Type", "Location", "WWW-Authenticate"):
            if headers.get(name):
                self.send_header(name, headers[name])
        self.send_header("Content-Length", str(len(answered)))
        self.end_headers()
        if answered:
            self.wfile.write(answered)

    do_GET = do_POST = do_PUT = do_PATCH = do_DELETE = answer


class Server(http.server.ThreadingHTTPServer):
    daemon_threads = True

    def handle_error(self, request, client_address):
        # A connection somebody dropped is theirs to drop, and says nothing
        # about this: a tunnel does it between requests, and so does a probe.
        if isinstance(sys.exc_info()[1], (ConnectionError, TimeoutError)):
            return
        super().handle_error(request, client_address)


Server(("127.0.0.1", port), Recorder).serve_forever()
PY
pids+=("$!")

# Up when the recorder is answered by roster refusing nothing: the schema, read
# with the token, through both.
up=0
for _ in $(seq 1 120); do
	if curl -fsS -m 5 -o /dev/null -H "Authorization: Bearer ${SCIM_CHECK_TOKEN}" \
		"http://127.0.0.1:${recorder}/scim/v2/ServiceProviderConfig" 2>/dev/null; then
		up=1
		break
	fi
	sleep 0.5
done
if [ "${up}" != 1 ]; then
	echo "roster did not come up behind the recorder; its log:" >&2
	tail -n 30 "${out}/roster.log" >&2
	exit 1
fi
# That read was this script's, and not something the page asked.
: > "${out}/requests.jsonl"

where="http://127.0.0.1:${recorder}"
if [ -n "${SCIM_CHECK_CF_API_TOKEN:-}" ]; then
	echo "== a tunnel to it, and a name for this run alone"
	cf sweep
	host="scim-check-$(head -c 6 /dev/urandom | od -An -tx1 | tr -d ' \n').${SCIM_CHECK_ZONE:?the zone a name is made under, by its name}"
	made=1
	tunnel="$(cf up "${host}" "http://localhost:${recorder}")"
	env -u SCIM_CHECK_CF_API_TOKEN TUNNEL_TOKEN="${tunnel}" cloudflared tunnel --no-autoupdate run > "${work}/tunnel.log" 2>&1 &
	pids+=("$!")
	where="https://${host}"
elif [ -n "${SCIM_CHECK_TUNNEL_TOKEN:-}" ]; then
	echo "== a tunnel to it"
	TUNNEL_TOKEN="${SCIM_CHECK_TUNNEL_TOKEN}" cloudflared tunnel --no-autoupdate run > "${work}/tunnel.log" 2>&1 &
	pids+=("$!")
	where="https://${SCIM_CHECK_HOST:?the hostname the tunnel routes to http://localhost:${recorder}}"
elif [ "${SCIM_CHECK_QUICK:-0}" = "1" ]; then
	echo "== a tunnel to it, by a name of Cloudflare's choosing"
	cloudflared tunnel --no-autoupdate --url "http://127.0.0.1:${recorder}" > "${work}/tunnel.log" 2>&1 &
	pids+=("$!")
	for _ in $(seq 1 60); do
		where="$(grep -o 'https://[a-z0-9-]*\.trycloudflare\.com' "${work}/tunnel.log" | head -n 1 || true)"
		[ -n "${where}" ] && break
		sleep 1
	done
	if [ -z "${where}" ]; then
		echo "the tunnel gave no address; it said:" >&2
		tail -n 20 "${work}/tunnel.log" >&2
		exit 1
	fi
fi

if [ "${where#https://}" != "${where}" ]; then
	# Through the tunnel, and not only behind it: the name resolving, the
	# route being the recorder's port, the token being the one it was handed.
	reached=0
	for _ in $(seq 1 60); do
		if curl -fsS -m 10 -o /dev/null -H "Authorization: Bearer ${SCIM_CHECK_TOKEN}" \
			"${where}/scim/v2/ServiceProviderConfig" 2>/dev/null; then
			reached=1
			break
		fi
		sleep 2
	done
	if [ "${reached}" != 1 ]; then
		echo "${where} did not reach the recorder; the tunnel said:" >&2
		tail -n 20 "${work}/tunnel.log" >&2
		echo "is its public hostname routed to http://localhost:${recorder}, and is the name one label under the zone?" >&2
		exit 1
	fi
	: > "${out}/requests.jsonl"
fi

cat <<SAY

== ready, for ${SCIM_CHECK_MINUTES} minutes at the most

   1. Open https://scimvalidator.microsoft.com/ and sign in.
   2. "Discover schema".
   3. SCIM endpoint URL:   ${where}/scim/v2
      Token:               $([ "${printed}" = 1 ] && echo "${SCIM_CHECK_TOKEN}" || echo "the one this was handed (SCIM_CHECK_TOKEN)")
   4. Tick "Enable Group Tests".
   5. "Test Schema".

   Nothing else to set: the joining property is \`userName\` for a user and
   \`displayName\` for a group, and an \`externalId\` is added here to a person
   made without one.

   It ends by itself ${SCIM_CHECK_IDLE} minutes after the last thing it is asked.

SAY

asked() { wc -l < "${out}/requests.jsonl" | tr -d ' '; }
was=0
idle=0
for minute in $(seq 1 "${SCIM_CHECK_MINUTES}"); do
	nap 60
	now="$(asked)"
	if [ "${now}" = "${was}" ]; then
		idle=$((idle + 1))
	else
		idle=0
	fi
	was="${now}"
	echo "   ${minute}m: ${now} asked"
	if [ "${now}" != 0 ] && [ "${idle}" -ge "${SCIM_CHECK_IDLE}" ]; then
		break
	fi
done

echo
echo "== what it was asked, and answered"
python3 - "${out}/requests.jsonl" <<'PY'
import collections, json, sys

rows = [json.loads(l) for l in open(sys.argv[1]) if l.strip()]
if not rows:
    print("   nothing. The page was not run, or did not reach this -- and where it said so, a name")
    print("   under `trycloudflare.com` may be one it will not call: SCIM_CHECK_CF_API_TOKEN makes")
    print("   one under a zone of your own (the head of scripts/scim-check.sh).")
    sys.exit(0)

by = collections.Counter((r["method"], r["status"]) for r in rows)
for (method, status), n in sorted(by.items()):
    print(f"   {n:4}  {method:7} {status}")

# Where it called from, which its tutorial does not say: a browser says where
# the page it is running is.
origins = sorted({r["origin"] for r in rows if r.get("origin")})
print("   called from a browser, on " + ", ".join(origins) if origins else "   called with no `Origin`: not from a browser")

unkind = [r for r in rows if r["status"] >= 400 and not r.get("refused")]
if unkind:
    print()
    print("   answered with a refusal:")
    for r in unkind:
        detail = ""
        try:
            detail = json.loads(r.get("response") or "{}").get("detail", "")
        except ValueError:
            pass
        print(f"     {r['status']} {r['method']:6} {r['path'][:70]}  {detail[:110]}")
added = sum(1 for r in rows if r.get("added_external_id"))
if added:
    print(f"\n   {added} made with no `externalId`, and given one here")
refused = sum(1 for r in rows if r.get("refused"))
if refused:
    print(f"\n   {refused} without the token, refused and not kept")
PY
echo
echo "   ${out}/requests.jsonl is every request and its answer; ${out}/roster.log is what roster said."
