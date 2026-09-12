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
# # What it does not do yet
#
# `docker/flow.sh` and `docker/behind.sh` are still compose-only; `behind.sh` is
# the oauth2-proxy shape, which is half of what a deployment runs.
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
	printf "subjectAltName=%s\n" \
		"DNS:roster-issuer.roster.svc.cluster.local,DNS:roster-issuer.roster.svc,DNS:roster-issuer,DNS:roster-app.roster.svc.cluster.local,DNS:roster-app.roster.svc,DNS:roster-app" > ext
	openssl x509 -req -in roster-edge.csr -CA ca.crt -CAkey ca.key -CAcreateserial \
		-days 1 -extfile ext -out roster-edge.crt >/dev/null 2>&1

	mkdir -p /w/tls && cp roster-hydra.crt roster-hydra.key roster-product.crt roster-product.key \
		roster-edge.crt roster-edge.key ca.crt /w/tls/
' >/dev/null

carry

echo "== up"
kube "kubectl create ns ${NS} >/dev/null"
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

# The overlay: the same base, with TLS ending in front of it. Every patch here is
# a line a deployment writes for its own hostnames.
cat > "${work}/behind/kustomization.yaml" <<'EOF'
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization

resources:
  - ../deploy
  - ./edge.yaml

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
kube "kubectl -n ${NS} delete job --all >/dev/null 2>&1; kubectl -n ${NS} apply -k /w/behind >/dev/null"
kube "kubectl -n ${NS} rollout status deploy/roster-edge --timeout=300s >/dev/null"
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

echo
echo "cluster: ok"
