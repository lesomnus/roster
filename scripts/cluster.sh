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
# # What it does not do yet
#
# Sign anybody in. That needs TLS in front (Hydra outside `--dev` refuses an
# `http://` issuer) and a relying party, and then it would be `docker/`'s walks
# pointed at this instead of at compose. Until then what this checks is that
# the manifests stand up and the contract holds, which is the half every defect
# so far was in.
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
	mkdir -p /w/tls && cp roster-hydra.crt roster-hydra.key roster-product.crt roster-product.key ca.crt /w/tls/
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
# The Jobs are Argo hooks, so nothing here runs them in order: delete and
# re-apply, then read what they said.
kube "kubectl -n ${NS} delete job --all >/dev/null 2>&1; kubectl -n ${NS} apply -k /w/deploy >/dev/null"
kube "kubectl -n ${NS} wait --for=condition=complete job/roster-hydra-clients --timeout=180s >/dev/null"
kube "kubectl -n ${NS} logs job/roster-hydra-clients"
kube "kubectl -n ${NS} wait --for=condition=complete job/roster-hydra-clients-check --timeout=180s >/dev/null" \
	|| { echo; echo "cluster: the check refused this deployment:"; kube "kubectl -n ${NS} logs job/roster-hydra-clients-check"; exit 1; }
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
            - { name: ISSUER, value: "https://roster-hydra.roster.svc.cluster.local:4444" }
            - { name: LOGIN, value: "http://roster-login.roster.svc.cluster.local:8091" }
            - { name: BASE, value: "https://roster-product.roster.svc.cluster.local:5555" }
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
kube "kubectl -n ${NS} delete job roster-walk --ignore-not-found >/dev/null 2>&1; kubectl -n ${NS} apply -f /w/walk.yaml >/dev/null"
if ! kube "kubectl -n ${NS} wait --for=condition=complete job/roster-walk --timeout=300s >/dev/null"; then
	kube "kubectl -n ${NS} logs job/roster-walk"
	echo "cluster: the walk did not finish" >&2
	exit 1
fi
kube "kubectl -n ${NS} logs job/roster-walk"

echo
echo "cluster: ok"
