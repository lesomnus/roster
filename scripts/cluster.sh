#!/usr/bin/env bash
# The reference deployment, in a real cluster.
#
# `scripts/hydra.sh` stands `compose.yaml` up, which is an **imitation** of a
# deployment: Hydra runs with `--dev`, nothing terminates TLS, and the clients
# are registered by a line in the compose file rather than by the thing a
# deployment runs. Every defect a person found in a browser the week this was
# written lived in that gap.
#
# So this runs `deploy/` -- the manifests, in k3d, with the image built from
# this checkout. What it proves is that they are coherent and that
# `roster login doctor` passes against what they produce, which is the contract
# a deployment inherits.
#
#     ./scripts/cluster.sh            # up, check, down
#     ./scripts/cluster.sh --hold     # leave it up to look at
#
# Not in `scripts/test.sh`: it needs an engine and a few minutes, like
# `hydra.sh` and `e2e.sh`.
#
# # Why everything runs in a container
#
# The engine may be somewhere else (`CLAUDE.md` says it is, here), so the
# cluster's published ports are not reachable from this checkout. Everything
# that talks to the API server is therefore a container on the cluster's own
# network, and the kubeconfig is rewritten to the in-network address. It is the
# same constraint `docker/flow.sh` already works under.
#
# # And then the flow, and the one class a fresh cluster cannot see
#
# `docker/itself.sh` runs as a Job **inside** the cluster -- the same script
# `scripts/hydra.sh` runs against compose, told three addresses -- so a product
# goes round the loop over the real manifests against a Hydra with no `--dev`.
#
# After which the registration is **changed under the running pods** and the
# whole thing is asked again. That is the shape of the outage every other gate
# here is blind to (#12), and the last phase is what says it cannot happen
# quietly.
#
# # And then again, behind something that ends TLS
#
# Which is the shape a deployment has: an ingress with the certificate, the
# issuer told so with `serve.public.tls.allow_termination_from`, and every app
# behind it seeing plain http while its public origin is https. That is written
# as a kustomize **overlay** on `deploy/` -- both because it is what a
# deployment writes and because nothing had ever written one, so *the base is
# overlay-able* was a claim and not a fact.
#
# # And all three walks, not one
#
# `docker/itself.sh` is our own app, `docker/flow.sh` is the protocol with curl,
# and `docker/behind.sh` is `oauth2-proxy` in front of a page -- a **standard
# third party**, which does its own discovery and fetches the key set itself over
# TLS it has to be taught to trust. All three are the scripts `scripts/hydra.sh`
# runs against compose, told where things are.
set -o errexit
set -o nounset
set -o pipefail

__dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
__root="$(cd "${__dir}/.." && pwd)"
cd "${__root}"

: "${CLUSTER:=roster-e2e}"
: "${NS:=roster}"
: "${KUBECTL_IMAGE:=alpine/k8s:1.31.0}"

hold=""
for v in "$@"; do
	case "${v}" in --hold) hold=1 ;; esac
done

for bin in k3d docker; do
	command -v "${bin}" >/dev/null || { echo "cluster: ${bin} is not here" >&2; exit 1; }
done

work="$(mktemp -d)"
# What the containers read, and **not a bind mount**: the engine may be
# somewhere else, so a path from this checkout means nothing to it -- only
# `/workspaces` does, and hardcoding that would break everywhere else. A volume
# is the same on both sides wherever the engine is.
vol="roster-cluster-$$"
docker volume create "${vol}" >/dev/null
carry() { tar -C "${work}" -cf - . | docker run --rm -i -v "${vol}:/w" alpine sh -c 'tar -C /w -xf -'; }

cleanup() {
	rm -rf "${work}"
	docker volume rm -f "${vol}" >/dev/null 2>&1 || true
	if [ -z "${hold}" ]; then
		k3d cluster delete "${CLUSTER}" >/dev/null 2>&1 || true
	else
		echo
		echo "cluster: left up. kubeconfig:"
		echo "  k3d kubeconfig get ${CLUSTER}"
	fi
}
trap cleanup EXIT

# settled is a Job's outcome: 0 complete, 1 failed, 2 still going after `secs`.
#
# `kubectl wait --for=condition=complete` is the obvious call and is wrong for
# the last phase here, where a **failure is the expected answer**: it would
# spend its whole timeout on it. So both conditions are watched, which costs a
# poll and buys an answer as soon as there is one.
settled() {
	local job="$1" secs="${2:-300}" i=0 t
	while [ "${i}" -lt "${secs}" ]; do
		t="$(kube "kubectl -n ${NS} get job/${job} -o jsonpath='{.status.conditions[*].type}' 2>/dev/null" || true)"
		case " ${t} " in
		*" Complete "*) return 0 ;;
		*" Failed "*) return 1 ;;
		esac

		i=$((i + 5))
		sleep 5
	done

	return 2
}

# walk is `docker/itself.sh` in the cluster, once, with its output on stdout
# whichever way it went. Deleted first because a Job's pod template is
# immutable, so an `apply` over a finished one is refused.
walk() {
	kube "kubectl -n ${NS} delete job roster-walk --ignore-not-found >/dev/null 2>&1; kubectl -n ${NS} apply -f /w/walk.yaml >/dev/null"

	local out=0
	settled roster-walk || out=$?
	kube "kubectl -n ${NS} logs job/roster-walk" || true

	return "${out}"
}

# resync is the two Jobs in `clients.yaml`, run the way a sync runs them: the
# one that writes the clients, and then the one that checks what Hydra now
# holds. 0 both fine, 1 the check went red, 2 it never finished, 3 the write
# itself failed.
#
# **The order is the whole of this function**, and leaving it to `apply` is a
# bug this rig had: the manifests say `sync-wave: "0"` then `"1"` and Argo
# honours that, while plain `kubectl apply` starts both at once. So the check
# read a registration the sync had not written yet -- which passed, and which
# made the phase that changes a registration report that nothing was wrong with
# it. Applying twice, with the check deleted in between, is the re-sync that
# puts it second.
resync() {
	local dir="${1:-/w/deploy}"

	kube "kubectl -n ${NS} delete job --all >/dev/null 2>&1; kubectl -n ${NS} apply -k ${dir} >/dev/null"
	settled roster-hydra-clients || return 3

	kube "kubectl -n ${NS} delete job roster-hydra-clients-check >/dev/null 2>&1; kubectl -n ${NS} apply -k ${dir} >/dev/null"

	local out=0
	settled roster-hydra-clients-check || out=$?

	return "${out}"
}

# product is the pod serving the relying party, by uid. Which pod it is matters
# for exactly one assertion and it is the assertion the last phase is about.
product() {
	kube "kubectl -n ${NS} get pod -l app.kubernetes.io/component=product -o jsonpath='{.items[0].metadata.uid}'"
}

echo "== from nothing"
k3d cluster delete "${CLUSTER}" >/dev/null 2>&1 || true
# No load balancer and no Traefik: nothing here is reached from outside the
# cluster's network, and the ingress is the next commit's problem.
k3d cluster create "${CLUSTER}" --servers 1 --agents 0 --no-lb \
	--k3s-arg "--disable=traefik@server:0" \
	--k3s-arg "--disable=metrics-server@server:0" \
	--wait >/dev/null

# The in-network address, because the published one is on the engine's host.
k3d kubeconfig get "${CLUSTER}" \
	| sed "s|server: https://.*|server: https://k3d-${CLUSTER}-server-0:6443|" \
	> "${work}/kubeconfig"

kube() {
	docker run --rm --network "k3d-${CLUSTER}" \
		-v "${vol}:/w" -e KUBECONFIG=/w/kubeconfig \
		"${KUBECTL_IMAGE}" sh -c "$*"
}

echo "== the images this checkout builds"
# Two, and the second is not a detail. What the deployment runs is the `app`
# stage, which is **distroless**: no shell, no `curl`, and none of `docker/`'s
# walks -- all of which is right for what it ships and all of which a walk
# needs. So the walk runs the `dev` stage, the same one `scripts/hydra.sh` runs
# against compose, and the thing under test is still the `app` one.
docker build -q --target app -t "roster-cluster:${CLUSTER}" . >/dev/null
docker build -q --target dev -t "roster-walk:${CLUSTER}" . >/dev/null
k3d image import "roster-cluster:${CLUSTER}" "roster-walk:${CLUSTER}" -c "${CLUSTER}" >/dev/null

cp -r deploy "${work}/deploy"
# The rig runs what was just built rather than what is published.
cat >> "${work}/deploy/kustomization.yaml" <<EOF

# Appended by scripts/cluster.sh.
patches: []
EOF
python3 - "${work}/deploy/kustomization.yaml" "roster-cluster:${CLUSTER}" <<'PY'
import sys
p, ref = sys.argv[1], sys.argv[2]
name, tag = ref.split(":")
s = open(p).read().replace(
	"  - name: ghcr.io/lesomnus/roster\n    newTag: edge",
	f"  - name: ghcr.io/lesomnus/roster\n    newName: {name}\n    newTag: {tag}",
)
open(p, "w").write(s)
PY

# The issuer's certificate, made here rather than checked in: one in git works
# until a date nobody wrote down. Hydra terminates TLS itself in this rig --
# `deploy/hydra.yaml` says what that gives up -- and everything that talks to it
# is handed the CA, which is what `ca.crt` in the same Secret is for.
echo "== a certificate for the issuer"
docker run --rm --entrypoint sh -v "${vol}:/w" alpine/openssl:3.3.2 -c '
	set -e
	cd /tmp
	openssl req -x509 -newkey rsa:2048 -nodes -days 1 -keyout ca.key -out ca.crt \
		-subj "/CN=roster cluster rig" >/dev/null 2>&1
	# One per host that serves TLS, from the one CA. The **product** needs one
	# too, and that is the rule `--dev` hides: an issuer not in development
	# mode refuses a redirect URI over plain http -- *http is only allowed for
	# hosts with suffix localhost* -- so a relying party reached at any other
	# name cannot complete a flow at all.
	for h in roster-hydra roster-product; do
		openssl req -newkey rsa:2048 -nodes -keyout "${h}.key" -out "${h}.csr" \
			-subj "/CN=${h}" >/dev/null 2>&1
		printf "subjectAltName=DNS:%s.roster.svc.cluster.local,DNS:%s.roster.svc,DNS:%s\n" \
			"${h}" "${h}" "${h}" > ext
		openssl x509 -req -in "${h}.csr" -CA ca.crt -CAkey ca.key -CAcreateserial \
			-days 1 -extfile ext -out "${h}.crt" >/dev/null 2>&1
	done

	# And one for the thing that **ends** TLS in front of both of them, which is
	# what a deployment has and what the certificates above stand in for. Two
	# names on one certificate, because an ingress is one terminator serving
	# several hosts and the names are what it routes on.
	openssl req -newkey rsa:2048 -nodes -keyout roster-edge.key -out roster-edge.csr \
		-subj "/CN=roster-edge" >/dev/null 2>&1
	# Every name the terminator answers to, and **all of them up front**. The
	# third one was added to nginx a phase later than to this list, and what
	# that looked like was a walk whose curl could not verify a certificate and
	# a wait loop reporting that nothing answered.
	#
	# No apostrophes in here: this whole block is one single-quoted argument to
	# sh -c, so one ends the string and openssl is handed the rest as flags.
	printf "subjectAltName=%s\n" \
		"DNS:roster-issuer.roster.svc.cluster.local,DNS:roster-issuer.roster.svc,DNS:roster-issuer,DNS:roster-app.roster.svc.cluster.local,DNS:roster-app.roster.svc,DNS:roster-app,DNS:roster-proxy.roster.svc.cluster.local,DNS:roster-proxy.roster.svc,DNS:roster-proxy" > ext
	openssl x509 -req -in roster-edge.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
		-days 1 -extfile ext -out roster-edge.crt >/dev/null 2>&1

	mkdir -p /w/tls && cp roster-hydra.crt roster-hydra.key roster-product.crt roster-product.key \
		roster-edge.crt roster-edge.key ca.crt /w/tls/
' >/dev/null

carry

echo "== up"
kube "kubectl create ns ${NS} >/dev/null"

# **The secrets, which `deploy/` deliberately ships none of.**
#
# It had three, with values anybody can read, and an overlay bringing its own
# under the same names could not build at all: kustomize accumulates generators
# before patches, so a `$patch: delete` of the base's does not prevent the
# collision -- `id … Name:"roster-hydra" … exists; can not use behavior:
# 'unspecified'`. A real deployment found that one commit after a local render
# said it was fine, because the local render had the SOPS generator stubbed out.
#
# So they are the rig's, like the certificates below, and nothing a deployment
# inherits has a credential in it.
kube "kubectl -n ${NS} create secret generic roster-hydra \
	--from-literal=system=this-is-a-rig-and-not-a-secret >/dev/null"
kube "kubectl -n ${NS} create secret generic roster-vouch \
	--from-literal=keys='[\"rig:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=\"]' >/dev/null"
kube "kubectl -n ${NS} create secret generic roster-product \
	--from-literal=client-secret=this-is-a-rig-and-not-a-secret-either >/dev/null"
# Before the manifests, because Hydra will not start without it.
kube "kubectl -n ${NS} create secret generic roster-hydra-tls \
	--from-file=tls.crt=/w/tls/roster-hydra.crt \
	--from-file=tls.key=/w/tls/roster-hydra.key \
	--from-file=ca.crt=/w/tls/ca.crt >/dev/null"
kube "kubectl -n ${NS} create secret generic roster-product-tls \
	--from-file=tls.crt=/w/tls/roster-product.crt \
	--from-file=tls.key=/w/tls/roster-product.key >/dev/null"
kube "kubectl -n ${NS} create secret generic roster-edge-tls \
	--from-file=tls.crt=/w/tls/roster-edge.crt \
	--from-file=tls.key=/w/tls/roster-edge.key >/dev/null"
kube "kubectl -n ${NS} apply -k /w/deploy >/dev/null"
kube "kubectl -n ${NS} rollout status deploy/roster-hydra --timeout=300s"

# **Twice, and the second one is the check.**
#
# `login provision` skips an operator whose tenant does not exist, and the
# tenant is made by `resources:` when the server starts -- so a first start
# fronts nobody and says so, and the *second* has the key. Written with
# `emptyDir` volumes this could never happen: the database went with the pod
# and every start was a first one. Restarting here is what proves the volumes
# outlive it.
kube "kubectl -n ${NS} rollout status deploy/roster --timeout=300s"
echo "== again, for the key the first start could not mint"
kube "kubectl -n ${NS} delete pod -l app.kubernetes.io/component=server >/dev/null 2>&1; kubectl -n ${NS} rollout status deploy/roster --timeout=300s"

echo
echo "== is the login app fronting anybody"
kube "kubectl -n ${NS} logs deploy/roster --tail=200 | grep -E 'login - addr'" \
	|| { echo "cluster: the login app is not serving; the log above says why" >&2; exit 1; }

echo
echo "== the clients, as hydra has them"
# The Jobs are Argo hooks, so nothing here runs them: `resync` does, in the
# order the waves ask for.
ran=0
resync || ran=$?
kube "kubectl -n ${NS} logs job/roster-hydra-clients"
case "${ran}" in
0) ;;
3) echo "cluster: the clients were not applied at all" >&2; exit 1 ;;
*) echo; echo "cluster: the check refused this deployment:"; kube "kubectl -n ${NS} logs job/roster-hydra-clients-check"; exit 1 ;;
esac
kube "kubectl -n ${NS} logs job/roster-hydra-clients-check"

# **Somebody to sign in as**, which `deploy/` deliberately does not declare:
# `Holder`, `Credential`, `Identity` and `Email` are the ways into an account,
# and a file that made those would grant access to whoever can write it. So the
# rig makes its own person, the way `docs/operating.md` says to.
#
# Inside the roster pod rather than as a Job, because these are local writes to
# a database that pod has open -- and piped in rather than quoted onto one
# command line, which through a shell, a container and `kubectl exec` is three
# layers of quoting to be wrong about.

# walkyaml writes the walk's Job for one pair of addresses: the issuer and the
# app. Two phases run it -- the issuer serving TLS itself, and the issuer behind
# something that ends TLS -- and the script it runs is the same either way.
walkyaml() {
	cat > "${work}/walk.yaml" <<EOF
# The walk, as a Job **inside** the cluster -- which is what makes the Services
# resolve and the issuer's certificate trustable. It is \`docker/itself.sh\`
# unchanged: the same script \`scripts/hydra.sh\` runs against compose, told
# three addresses.
apiVersion: batch/v1
kind: Job
metadata:
  name: roster-walk
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: walk
          image: roster-walk:${CLUSTER}
          command: ["/usr/local/bin/itself.sh"]
          env:
            - { name: ISSUER, value: "$1" }
            - { name: LOGIN, value: "http://roster-login.roster.svc.cluster.local:8091" }
            - { name: BASE, value: "$2" }
            # curl's own name for a trust store, because this issuer's
            # certificate is the rig's own.
            - { name: CURL_CA_BUNDLE, value: /tls/ca.crt }
          volumeMounts:
            - { name: tls, mountPath: /tls, readOnly: true }
      volumes:
        - name: tls
          secret:
            secretName: roster-hydra-tls
            items: [{ key: ca.crt, path: ca.crt }]
EOF
	carry
}

walkyaml \
	"https://roster-hydra.roster.svc.cluster.local:4444" \
	"https://roster-product.roster.svc.cluster.local:5555"

echo
echo "== somebody to sign in as"
# One command per `exec`, because the image is **distroless** and has no shell
# to give a script to -- which is right for what it ships and is a thing to
# find out here rather than in a deployment's runbook.
seed() { kube "kubectl -n ${NS} exec -i deploy/roster -c roster -- roster --config /config/config.yaml $*"; }
seed "holder add @acme/erin" >/dev/null 2>&1 || true
seed "role add @acme/everything '{\"methods\":[\"/roster.*/*\"]}'" >/dev/null 2>&1 || true
kube "printf '%s' '{\"role\":{\"slug\":{\"alias\":\"everything\",\"tenant\":{\"alias\":\"acme\"}}},\"holder\":{\"slug\":{\"alias\":\"erin\",\"tenant\":{\"alias\":\"acme\"}}}}' \
	| kubectl -n ${NS} exec -i deploy/roster -c roster -- roster --config /config/config.yaml binding add -" >/dev/null 2>&1 || true
kube "printf '%s' 'correct horse battery staple' \
	| kubectl -n ${NS} exec -i deploy/roster -c roster -- roster --config /config/config.yaml vouch set --password-stdin @acme/erin" >/dev/null
echo "erin, with a password"

echo
echo "== the flow, in the cluster"
walk || { echo "cluster: the walk did not finish" >&2; exit 1; }

# **The class no fresh cluster catches**, and the reason this rig is a rig
# rather than one more walk (#12).
#
# Everything above starts a fresh process, and a fresh process gets the client's
# authentication method right **whatever it is registered as**:
# `golang.org/x/oauth2` probes -- the header, then the body -- and caches what
# worked for the life of that process. So the hour `itself.login-demo` signed
# nobody in was invisible to every gate: a commit changed the declared client to
# `client_secret_post`, the sync applied it, and the pods that were **already
# running** went on sending the header with no second try. Restart them and it
# works. Run any test and it passes.
#
# Two things are asserted here, and neither is about that library being wrong:
#
#   1. the deployment's **own** check goes red on the sync that does it, so
#      somebody is told at push time rather than by a person in a browser
#   2. the failure is **deterministic** -- it survives a restart, and it clears
#      without one -- which is what pinning the method in code bought. Before
#      that, a restart cured it and hid it, which is the property that turned a
#      configuration mistake into an hour.
echo
echo "== and then the registration changes under the running pods"

was="$(product)"

# The change a person made, made the way they made it: the declaration, then a
# sync. Not a `curl` at the admin API -- that would be this rig inventing a way
# to break a deployment, where what is worth reproducing is the way a deployment
# actually breaks.
python3 - "${work}/deploy/clients/product.json" <<'PY'
import sys

p = sys.argv[1]
s = open(p).read()
assert '"client_secret_basic"' in s, "the client no longer says what this phase changes"
open(p, "w").write(s.replace('"client_secret_basic"', '"client_secret_post"'))
PY
carry
broke=0
resync || broke=$?
[ "${broke}" != "3" ] \
	|| { kube "kubectl -n ${NS} logs job/roster-hydra-clients"; echo "cluster: the sync that was meant to break it did not run" >&2; exit 1; }
echo "   the declared client now says client_secret_post"

# And **nothing was restarted**, which is the whole of what makes this the class
# it is. A rig that recreated the pods here would be testing a fresh process
# again, which is the thing that was always green.
now="$(product)"
[ -n "${now}" ] && [ "${now}" = "${was}" ] \
	|| { echo "cluster: the product pod was replaced, so this phase is testing a fresh process" >&2; exit 1; }
echo "   and the app is the pod that was already running"

# 1. The deployment's own gate, on the same sync.
[ "${broke}" = "1" ] || {
	echo "cluster: the check passed a registration this stack cannot use (${broke})" >&2
	kube "kubectl -n ${NS} logs job/roster-hydra-clients-check"
	exit 1
}
kube "kubectl -n ${NS} logs job/roster-hydra-clients-check --tail=20" \
	| grep -q 'client_secret_post' \
	|| { echo "cluster: the check went red for some other reason:"; kube "kubectl -n ${NS} logs job/roster-hydra-clients-check"; exit 1; }
echo "   the sync's own check refuses it, naming the method"

# 2. And a sign-in stops, at the exchange, on the first attempt.
if walk >/dev/null 2>&1; then
	echo "cluster: a sign-in worked against a registration the app cannot use, which means this phase proves nothing" >&2
	exit 1
fi
kube "kubectl -n ${NS} logs job/roster-walk" | grep -q 'the callback answered 400' \
	|| { echo "cluster: the walk failed somewhere other than the exchange:"; kube "kubectl -n ${NS} logs job/roster-walk"; exit 1; }
# The sentence that says which of the app's five checks refused it, which is the
# other thing that hour cost -- it used to answer 400 and log nothing.
kube "kubectl -n ${NS} logs deploy/roster-product --tail=50" | grep -q 'would not exchange the code' \
	|| { echo "cluster: the app did not say why it refused the callback:"; kube "kubectl -n ${NS} logs deploy/roster-product --tail=50"; exit 1; }
echo "   and a sign-in fails at the exchange, in one line in the app's log"

# 3. **A restart does not cure it**, which is the assertion this whole phase is
# built to make. A probing client would come up, try the header, be refused, try
# the body, and work -- so the registration and the code would disagree with
# nothing to show for it until the next thing to read the registration. The
# method is in the code now, so a fresh process fails exactly as the old one
# did.
kube "kubectl -n ${NS} rollout restart deploy/roster-product >/dev/null"
kube "kubectl -n ${NS} rollout status deploy/roster-product --timeout=300s >/dev/null"
if walk >/dev/null 2>&1; then
	echo "cluster: a restart cured it, so the method is being discovered rather than said -- the defect in #12 is back" >&2
	exit 1
fi
echo "   restarting the app does not cure it, which is the point"

echo
echo "== and the registration going back is the whole of the fix"

python3 - "${work}/deploy/clients/product.json" <<'PY'
import sys

p = sys.argv[1]
s = open(p).read()
open(p, "w").write(s.replace('"client_secret_post"', '"client_secret_basic"'))
PY
carry
after="$(product)"
resync || { kube "kubectl -n ${NS} logs job/roster-hydra-clients-check"; echo "cluster: the check still refuses a deployment that is put back" >&2; exit 1; }
echo "   the check passes again"

# **With no restart**, which is the other half of the method being said rather
# than discovered: nothing is cached, so there is nothing to clear. A deployment
# recovers by fixing the declaration and waiting for a sync.
walk || { echo "cluster: putting the registration back did not sign anybody in" >&2; exit 1; }
[ "$(product)" = "${after}" ] \
	|| { echo "cluster: the app restarted during the recovery, so 'no restart' is not what was checked" >&2; exit 1; }
echo "   and so does the flow, on the pod that never restarted"


# **Behind something that ends TLS**, which is the shape a deployment has and the
# last thing #12 listed as covered nowhere.
#
# Up to here Hydra serves TLS itself. That is right for a cluster with no ingress
# and it is not what a deployment does: a deployment ends TLS at its ingress and
# tells Hydra so with `serve.public.tls.allow_termination_from`, so the issuer
# sees plain http with `X-Forwarded-Proto: https` on it and every URL it builds
# has to be the **public** scheme anyway. Nothing here had ever seen that, and
# what lives there is a whole class: an app that works out its own scheme from
# `r.TLS` gets it wrong, and one that builds a URL from what it was told gets it
# right. One such app was wrong (`98a2b99`).
#
# It is an **overlay** rather than a second rig, which is the other thing worth
# proving: `deploy/` says an overlay is how a deployment changes the host in
# `URLS_*` and where TLS ends, and until now nothing had written one.
echo
echo "== and again, with TLS ending in front of it"

mkdir -p "${work}/behind"

# The terminator. nginx, unprivileged, one certificate with both names on it,
# and the two headers that make this the shape it is -- so the issuer knows the
# browser arrived over https and which name it asked for.
cat > "${work}/behind/edge.yaml" <<'EOF'
apiVersion: v1
kind: ConfigMap
metadata:
  name: roster-edge
data:
  edge.conf: |
    server {
      listen 8443 ssl;
      server_name roster-issuer roster-issuer.roster.svc roster-issuer.roster.svc.cluster.local;
      ssl_certificate     /tls/tls.crt;
      ssl_certificate_key /tls/tls.key;
      location / {
        proxy_pass http://roster-hydra:4444;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto https;
        proxy_set_header X-Forwarded-For $remote_addr;
      }
    }
    server {
      listen 8443 ssl;
      server_name roster-app roster-app.roster.svc roster-app.roster.svc.cluster.local;
      ssl_certificate     /tls/tls.crt;
      ssl_certificate_key /tls/tls.key;
      location / {
        proxy_pass http://roster-product:5555;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto https;
        proxy_set_header X-Forwarded-For $remote_addr;
      }
    }
    # And the third relying party, which is somebody else's code: `oauth2-proxy`
    # in front of a page. It is here from the **start** rather than added when
    # its walk runs, and that is not tidiness -- this file is mounted with
    # `subPath`, and a subPath mount **never sees an updated ConfigMap**. Nginx
    # would not reload for one either. Editing it later left the terminator
    # serving two names and answering the third from whichever block is first,
    # which reads as *404 page not found* from a Go server nobody expected to be
    # in the path.
    #
    # Which also fixes the order it has to be in: nginx resolves a plain
    # `proxy_pass` name **at startup**, so the Service below has to exist before
    # this pod does. It does, with no endpoints, which is enough for DNS.
    server {
      listen 8443 ssl;
      server_name roster-proxy roster-proxy.roster.svc roster-proxy.roster.svc.cluster.local;
      ssl_certificate     /tls/tls.crt;
      ssl_certificate_key /tls/tls.key;
      location / {
        proxy_pass http://roster-behind:4180;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-Proto https;
        proxy_set_header X-Forwarded-For $remote_addr;
      }
    }
---
apiVersion: v1
kind: Service
metadata:
  name: roster-proxy
spec:
  selector:
    app.kubernetes.io/name: roster
    app.kubernetes.io/component: edge
  ports:
    - { name: https, port: 443, targetPort: https }
---
apiVersion: v1
kind: Service
metadata:
  name: roster-issuer
spec:
  selector:
    app.kubernetes.io/name: roster
    app.kubernetes.io/component: edge
  ports:
    - { name: https, port: 443, targetPort: https }
---
apiVersion: v1
kind: Service
metadata:
  name: roster-app
spec:
  selector:
    app.kubernetes.io/name: roster
    app.kubernetes.io/component: edge
  ports:
    - { name: https, port: 443, targetPort: https }
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: roster-edge
  labels:
    app.kubernetes.io/name: roster
    app.kubernetes.io/component: edge
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: roster
      app.kubernetes.io/component: edge
  template:
    metadata:
      labels:
        app.kubernetes.io/name: roster
        app.kubernetes.io/component: edge
    spec:
      containers:
        - name: nginx
          image: nginxinc/nginx-unprivileged:1.27-alpine
          ports:
            - { name: https, containerPort: 8443 }
          readinessProbe:
            tcpSocket: { port: https }
            initialDelaySeconds: 2
            periodSeconds: 5
          volumeMounts:
            - { name: conf, mountPath: /etc/nginx/conf.d/edge.conf, subPath: edge.conf, readOnly: true }
            - { name: tls, mountPath: /tls, readOnly: true }
      volumes:
        - name: conf
          configMap: { name: roster-edge }
        - name: tls
          secret: { secretName: roster-edge-tls }
EOF

cat > "${work}/behind/proxy.yaml" <<'EOF'
apiVersion: v1
kind: Service
metadata:
  name: roster-behind
spec:
  selector:
    app.kubernetes.io/name: roster
    app.kubernetes.io/component: behind
  ports:
    - { name: http, port: 4180, targetPort: http }
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: roster-behind
  labels:
    app.kubernetes.io/name: roster
    app.kubernetes.io/component: behind
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: roster
      app.kubernetes.io/component: behind
  template:
    metadata:
      labels:
        app.kubernetes.io/name: roster
        app.kubernetes.io/component: behind
    spec:
      containers:
        - name: proxy
          image: quay.io/oauth2-proxy/oauth2-proxy:v7.14.2
          env:
            - { name: OAUTH2_PROXY_PROVIDER, value: oidc }
            - { name: OAUTH2_PROXY_CLIENT_ID, value: behind }
            - name: OAUTH2_PROXY_CLIENT_SECRET
              valueFrom: { secretKeyRef: { name: roster-behind, key: client-secret } }
            - { name: OAUTH2_PROXY_COOKIE_SECRET, value: sixteen-bytes-ok }
            - { name: OAUTH2_PROXY_HTTP_ADDRESS, value: "0.0.0.0:4180" }
            # **It is behind a proxy, and it has to be told.** Without this
            # oauth2-proxy ignores `X-Forwarded-*` and works its own URLs out
            # from the request it was handed, which here is plain http on a
            # Service name no browser can reach.
            - { name: OAUTH2_PROXY_REVERSE_PROXY, value: "true" }
            # Its **public** URL, which is the terminator's name and not its own
            # Service: what it registers has to be what a browser reaches.
            - { name: OAUTH2_PROXY_REDIRECT_URL, value: "https://roster-proxy.roster.svc.cluster.local/oauth2/callback" }
            - { name: OAUTH2_PROXY_OIDC_ISSUER_URL, value: "https://roster-issuer.roster.svc.cluster.local" }
            # The issuer's certificate is this rig's own, and this is somebody
            # else's client: it has its own name for a trust store.
            - { name: OAUTH2_PROXY_PROVIDER_CA_FILES, value: /tls/ca.crt }
            # **The identity is `sub` and not an address.** oauth2-proxy keys a
            # session on an email and refuses a token with none, and roster puts
            # `email` in a token only when the address is verified -- so this is
            # the line every relying party of roster wants.
            - { name: OAUTH2_PROXY_OIDC_EMAIL_CLAIM, value: sub }
            - { name: OAUTH2_PROXY_EMAIL_DOMAINS, value: "*" }
            - { name: OAUTH2_PROXY_SCOPE, value: "openid profile email" }
            - { name: OAUTH2_PROXY_SKIP_PROVIDER_BUTTON, value: "true" }
            - { name: OAUTH2_PROXY_UPSTREAMS, value: "static://200" }
            - { name: OAUTH2_PROXY_COOKIE_SECURE, value: "true" }
            # With the port where there is one, and the name a browser uses:
            # what oauth2-proxy does with an `rd` it will not follow is send the
            # browser to `/`, which turns a sign-out into the proxy forgetting
            # its own session and nothing else.
            - { name: OAUTH2_PROXY_WHITELIST_DOMAINS, value: "roster-issuer.roster.svc.cluster.local,roster-login.roster.svc.cluster.local:8091" }
          ports:
            - { name: http, containerPort: 4180 }
          readinessProbe:
            httpGet: { path: /ping, port: http }
            initialDelaySeconds: 2
            periodSeconds: 5
          volumeMounts:
            - { name: tls, mountPath: /tls, readOnly: true }
      volumes:
        - name: tls
          secret:
            secretName: roster-hydra-tls
            items: [{ key: ca.crt, path: ca.crt }]
EOF

# The overlay: the same base, with TLS ending in front of it. Every patch here is
# a line a deployment writes for its own hostnames.
cat > "${work}/behind/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

resources:
  - ../deploy
  - ./edge.yaml
  # Somebody else's relying party, up with the terminator rather than when its
  # walk runs: the terminator has to be able to resolve it at startup.
  - ./proxy.yaml

patches:
  # Hydra: stop serving TLS, trust the terminator's word about the scheme, and
  # publish the **public** name -- which is what every URL in the discovery
  # document is built from, so this and the terminator's certificate are one
  # decision.
  #
  # A strategic merge and not a replace: `env` merges by `name`, so this says the
  # three that change and inherits the rest. A patch that listed them all would
  # be a second copy of the base's contract, which is the shape this repository
  # keeps finding defects in.
  - patch: |
      apiVersion: apps/v1
      kind: Deployment
      metadata:
        name: roster-hydra
      spec:
        template:
          spec:
            containers:
              - name: hydra
                env:
                  - { name: SERVE_PUBLIC_TLS_ENABLED, value: "false" }
                  - { name: SERVE_PUBLIC_TLS_ALLOW_TERMINATION_FROM, value: "10.42.0.0/16" }
                  - { name: URLS_SELF_ISSUER, value: "https://roster-issuer.roster.svc.cluster.local" }
  # The relying party: plain http, no certificate of its own, and its **public**
  # origin is https. Which is every app behind an ingress -- and the pair of
  # lines that makes it one: the app is told what it is reached as rather than
  # working it out from the connection it got.
  - patch: |
      apiVersion: apps/v1
      kind: Deployment
      metadata:
        name: roster-product
      spec:
        template:
          spec:
            containers:
              - name: product
                args:
                  - --listen=0.0.0.0:5555
                  - --issuer=https://roster-issuer.roster.svc.cluster.local
                  - --client-id=product
                  - --base=https://roster-app.roster.svc.cluster.local
                readinessProbe:
                  httpGet: { scheme: HTTP }
  # And the check that reads what Hydra was told: the public port moved, so the
  # one line naming it moves with it. `deploy/clients.yaml` says that line is
  # said rather than derived for this reason.
  - patch: |
      apiVersion: batch/v1
      kind: Job
      metadata:
        name: roster-hydra-clients-check
      spec:
        template:
          spec:
            containers:
              - name: doctor
                args:
                  - --config=/config/config.yaml
                  - login
                  - doctor
                  - --public=https://roster-issuer.roster.svc.cluster.local
EOF

# The client's URLs move with the app, because a redirect URI is the **public**
# one and the issuer refuses anything else. Same file, same sync as the phase
# above.
python3 - "${work}/deploy/clients/product.json" <<'PY'
import sys

p = sys.argv[1]
s = open(p).read()
old = "https://roster-product.roster.svc.cluster.local:5555"
assert old in s, "the client no longer names the app this phase moves"
open(p, "w").write(s.replace(old, "https://roster-app.roster.svc.cluster.local"))
PY
carry

# The Jobs go first, because **a Job's pod template is immutable**: this overlay
# changes the check's arguments, and an `apply` over a finished Job is refused
# with `field is immutable` and a page of diff. `resync` below does the same
# thing for the same reason, and a deployment gets it from Argo's
# `hook-delete-policy: BeforeHookCreation`.
# Before the apply, because a pod that names a Secret which is not there yet is a
# pod that waits -- and this is the rig's, like the TLS ones above.
kube "kubectl -n ${NS} create secret generic roster-behind \
	--from-literal=client-secret=this-is-a-rig-and-not-a-secret-either >/dev/null 2>&1 || true"

kube "kubectl -n ${NS} delete job --all >/dev/null 2>&1; kubectl -n ${NS} apply -k /w/behind >/dev/null"
kube "kubectl -n ${NS} rollout status deploy/roster-edge --timeout=300s >/dev/null"
kube "kubectl -n ${NS} rollout status deploy/roster-behind --timeout=300s >/dev/null"
kube "kubectl -n ${NS} rollout status deploy/roster-hydra --timeout=300s >/dev/null"

# **The product coming up at all is the first assertion.** It does discovery
# against `--issuer` at startup and refuses a document whose `iss` is not that
# string, so an issuer that published `http://` here -- which is what it would
# do if `allow_termination_from` were missing or the terminator sent no
# `X-Forwarded-Proto` -- is a pod that never becomes ready.
kube "kubectl -n ${NS} rollout status deploy/roster-product --timeout=300s >/dev/null" \
	|| { kube "kubectl -n ${NS} logs deploy/roster-product --tail=20"; echo "cluster: the relying party would not come up behind the terminator" >&2; exit 1; }
echo "   the issuer is behind nginx, and the app came up against it"

moved=0
resync /w/behind || moved=$?
[ "${moved}" = "0" ] \
	|| { kube "kubectl -n ${NS} logs job/roster-hydra-clients-check"; echo "cluster: the check refuses the deployment behind a terminator (${moved})" >&2; exit 1; }
echo "   and the check passes against the public name"

# And the whole flow through it. The same script again, told the two public
# names -- so what it proves this time is that nothing in the stack decided its
# own scheme from the connection it was handed.
walkyaml \
	"https://roster-issuer.roster.svc.cluster.local" \
	"https://roster-app.roster.svc.cluster.local"
walk || { echo "cluster: the flow does not survive TLS ending in front of it" >&2; exit 1; }
echo "   and the flow goes round, with nothing serving its own TLS"


# **The other two walks**, which are the other two halves of what a deployment
# runs -- and both against the terminated shape above, because that is the one a
# deployment has.
#
# `docker/flow.sh` is the **protocol**, walked with curl rather than by an app, so
# it needs what a product would have been handed: the client's secret to exchange
# a code with, the `Holder.id` the token has to name, and a key that may sign her
# out and enrol a second factor.
#
# `docker/behind.sh` is `oauth2-proxy` in front of a page: a **standard third
# party**, which is the half of a deployment's apps our own code cannot speak for
# -- it does its own discovery and fetches the key set itself, over TLS it has to
# be taught to trust, which is a thing only somebody else's client can check for
# us.
echo
echo "== the other two walks, over the manifests"

# Who erin is, which is the fact the token has to carry.
sub="$(kube "kubectl -n ${NS} exec deploy/roster -c roster -- roster --config /config/config.yaml holder get -o json @acme/erin" \
	| sed -n 's/.*"id": "\([^"]*\)".*/\1/p' | head -1 | tr -d '\r')"
[ -n "${sub}" ] || { echo "cluster: the seeded person has no id" >&2; exit 1; }

# A key that may sign her out and give her an authenticator, for the steps of the
# walk that are about **her** rather than about a flow.
#
# Piped into a Secret **inside the cluster** and never through this shell: a key
# is a credential, and one that goes through a terminal is one in a scrollback.
kube "kubectl -n ${NS} delete secret roster-walk-key --ignore-not-found >/dev/null 2>&1; \
	kubectl -n ${NS} exec deploy/roster -c roster -- roster --config /config/config.yaml \
		key add --tenant acme --holder erin --name walk \
		--allow /roster.HolderService/Invalidate,/roster.CredentialService/Enrol,/roster.VouchService/Verify \
	| tr -d '\r\n' > /tmp/k && kubectl -n ${NS} create secret generic roster-walk-key --from-file=key=/tmp/k >/dev/null"
echo "   erin is ${sub}, and holds a key that may sign her out"

cat > "${work}/flow.yaml" <<EOF
# \`docker/flow.sh\` in the cluster: the protocol, with curl standing in for a
# product. What it checks that the two app walks cannot is the claims in the
# token, a second flow the issuer skips the form for, an \`Invalidate\` reaching
# the issuer, and the sign-out asked for with no hint.
apiVersion: batch/v1
kind: Job
metadata:
  name: roster-flow
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: flow
          image: roster-walk:${CLUSTER}
          command: ["/usr/local/bin/flow.sh"]
          env:
            - { name: SEED_CUSTOMER, value: "acme" }
            - { name: SEED_USER, value: "erin" }
            - { name: SEED_PASSWORD, value: "correct horse battery staple" }
            - { name: LOGIN, value: "http://roster-login.roster.svc.cluster.local:8091" }
            - { name: ISSUER, value: "https://roster-issuer.roster.svc.cluster.local" }
            - { name: ADMIN, value: "http://roster-hydra:4445" }
            # roster's own HTTP port, which is how anything that is not gRPC
            # reaches it -- and the **data plane's** Service, which is called
            # roster-data here because the one called roster in a deployment is
            # the console's. Getting that wrong is a walk that gets all the way
            # to the claims and then cannot resolve a host.
            #
            # No backticks in this heredoc: it is unquoted, because it
            # interpolates the image tag and the subject, so a backtick is a
            # command substitution and the error it gives is *roster-data:
            # command not found* from the rig rather than anything about the walk.
            - { name: ROSTER, value: "http://roster-data:8081" }
            - { name: OAUTH_CLIENT, value: "product" }
            # A registered redirect of that client, because the issuer refuses
            # any other -- nothing fetches it, the walk reads the code out of
            # the redirect.
            - { name: CALLBACK, value: "https://roster-app.roster.svc.cluster.local/callback" }
            # And where a sign-out asks to come back to, which is the app's
            # **origin** and not its callback -- because that is what
            # examples/product asks for and therefore what the client registers.
            # Hydra refuses any other, in as many words, and that refusal is how
            # this walk found out that the two are different URLs.
            - { name: AFTER_LOGOUT, value: "https://roster-app.roster.svc.cluster.local" }
            - { name: EXPECT_SUB, value: "${sub}" }
            - { name: CONSENT, value: "skip" }
            - { name: CURL_CA_BUNDLE, value: /tls/ca.crt }
            - name: CLIENT_SECRET
              valueFrom: { secretKeyRef: { name: roster-product, key: client-secret } }
            - name: INVALIDATE_KEY
              valueFrom: { secretKeyRef: { name: roster-walk-key, key: key } }
          volumeMounts:
            - { name: tls, mountPath: /tls, readOnly: true }
      volumes:
        - name: tls
          secret:
            secretName: roster-hydra-tls
            items: [{ key: ca.crt, path: ca.crt }]
EOF
carry

kube "kubectl -n ${NS} delete job roster-flow --ignore-not-found >/dev/null 2>&1; kubectl -n ${NS} apply -f /w/flow.yaml >/dev/null"
if ! settled roster-flow; then
	kube "kubectl -n ${NS} logs job/roster-flow"
	echo "cluster: the protocol walk did not finish" >&2
	exit 1
fi
kube "kubectl -n ${NS} logs job/roster-flow"


# A second relying party is a second **declared** client, which is three files and
# not one: the document, the secret it names, and the line in `login.clients` that
# says this app answers challenges for it. Leaving that last one out is one of the
# four defects `roster login doctor` exists for, so the rig does it the way a
# deployment does and the check gets to prove it.
cat > "${work}/deploy/clients/behind.json" <<'EOF'
{
  "client_id": "behind",
  "client_name": "the page a standard proxy sits in front of",
  "client_secret": "@SECRET@",
  "grant_types": ["authorization_code", "refresh_token"],
  "response_types": ["code"],
  "scope": "openid offline profile email",

  "token_endpoint_auth_method": "client_secret_basic",

  "redirect_uris": ["https://roster-proxy.roster.svc.cluster.local/oauth2/callback"],

  "post_logout_redirect_uris": []
}
EOF

python3 - "${work}/deploy/kustomization.yaml" "${work}/deploy/config.yaml" "${work}/behind/kustomization.yaml" <<'PY'
import sys

ku, cfg, overlay = sys.argv[1], sys.argv[2], sys.argv[3]

# The generator has to know about the second file.
s = open(ku).read()
old = "      - clients/product.json"
assert old in s
open(ku, "w").write(s.replace(old, old + "\n      - clients/behind.json", 1))

# And the Login App has to answer challenges for it.
s = open(cfg).read()
old = "    acme: [product]"
assert old in s, "login.clients no longer reads the way this rig expects"
open(cfg, "w").write(s.replace(old, "    acme: [product, behind]", 1))

# The proxy joins the overlay, and the Job that registers clients needs the
# second secret mounted where it looks for it: `/secrets/<id>`. Volumes and mounts
# merge by name, so this adds one of each and restates nothing.
s = open(overlay).read()
s += """  - patch: |
      apiVersion: batch/v1
      kind: Job
      metadata:
        name: roster-hydra-clients
      spec:
        template:
          spec:
            containers:
              - name: apply
                volumeMounts:
                  - { name: behind, mountPath: /secrets/behind, readOnly: true }
            volumes:
              - name: behind
                secret: { secretName: roster-behind, items: [{ key: client-secret, path: client-secret }] }
"""
open(overlay, "w").write(s)
PY
carry

# The client is declared, so the clients Job needs the second secret mounted and
# the Login App needs to claim it. Both are files above; this is the sync.

# The second client, registered and then asked about -- and the `doctor` run is
# not a formality here: a client Hydra has that `login.clients` does not name is
# the defect that reaches a person as *this login is not working*, and it is
# exactly the mistake a second relying party invites.
two=0
resync /w/behind || two=$?
[ "${two}" = "0" ] \
	|| { kube "kubectl -n ${NS} logs job/roster-hydra-clients"; kube "kubectl -n ${NS} logs job/roster-hydra-clients-check"; echo "cluster: the second client is not registered the way this stack needs (${two})" >&2; exit 1; }
echo "   a second relying party is declared, registered and claimed"

cat > "${work}/behind.yaml" <<EOF
# \`docker/behind.sh\` in the cluster: a page with \`oauth2-proxy\` in front,
# which is the shape half of a deployment's apps are in and the one our own code
# cannot speak for.
apiVersion: batch/v1
kind: Job
metadata:
  name: roster-behind-walk
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: walk
          image: roster-walk:${CLUSTER}
          command: ["/usr/local/bin/behind.sh"]
          env:
            - { name: PROXY, value: "https://roster-proxy.roster.svc.cluster.local" }
            - { name: LOGIN, value: "http://roster-login.roster.svc.cluster.local:8091" }
            - { name: ISSUER, value: "https://roster-issuer.roster.svc.cluster.local" }
            - { name: CLIENT, value: "behind" }
            - { name: SEED_USER, value: "erin" }
            - { name: SEED_PASSWORD, value: "correct horse battery staple" }
            - { name: CURL_CA_BUNDLE, value: /tls/ca.crt }
          volumeMounts:
            - { name: tls, mountPath: /tls, readOnly: true }
      volumes:
        - name: tls
          secret:
            secretName: roster-hydra-tls
            items: [{ key: ca.crt, path: ca.crt }]
EOF
carry

kube "kubectl -n ${NS} delete job roster-behind-walk --ignore-not-found >/dev/null 2>&1; kubectl -n ${NS} apply -f /w/behind.yaml >/dev/null"
if ! settled roster-behind-walk; then
	kube "kubectl -n ${NS} logs job/roster-behind-walk"
	# And what the two things in the path have to say, because a walk that fails
	# at a proxy is a walk whose own output is one line about a header.
	echo "--- the proxy"
	kube "kubectl -n ${NS} logs deploy/roster-behind --tail=40" || true
	echo "--- the terminator"
	kube "kubectl -n ${NS} logs deploy/roster-edge --tail=20" || true
	echo "cluster: somebody else's relying party did not get round the loop" >&2
	exit 1
fi
kube "kubectl -n ${NS} logs job/roster-behind-walk"

echo
echo "cluster: ok"
