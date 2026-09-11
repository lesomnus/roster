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

echo "== the image this checkout builds"
docker build -q --target app -t "roster-cluster:${CLUSTER}" . >/dev/null
k3d image import "roster-cluster:${CLUSTER}" -c "${CLUSTER}" >/dev/null

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

carry

echo "== up"
kube "kubectl create ns ${NS} >/dev/null && kubectl -n ${NS} apply -k /w/deploy >/dev/null"
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

echo
echo "cluster: ok"
