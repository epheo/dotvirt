#!/usr/bin/env bash
# e2e-microshift.sh — the real-loop tier: boot a disposable MicroShift
# (ghcr.io/epheo/microshift as a privileged container), install the published
# dotvirt operator on it, and prove the actual product loop end to end:
#
#   operator install -> managed Forgejo -> ArgoCD wiring -> project create via
#   the dotvirt API -> platform PR merge -> tenant repo + app appear ->
#   hack/e2e-roundtrip.sh (stage VM -> PR -> merge -> Synced -> delete -> gone)
#
# Everything rides in-cluster URLs (forge service, argo service), so no DNS or
# TLS scaffolding: the operator already targets webhook delivery at dotvirt's
# own Service, and spec.forge.url/argocd.serverURL pin the rest.
#
# Requires root privileges (MicroShift needs OVS + cgroups), podman, kubectl.
#   sudo hack/e2e-microshift.sh          # full run
#   CLEAN=1 sudo hack/e2e-microshift.sh  # tear down container + loopback VG
set -euo pipefail

MS_IMAGE="${MS_IMAGE:-ghcr.io/epheo/microshift:4.22}"
NAME="${NAME:-dotvirt-e2e-microshift}"
# The loopback VG backing TopoLVM; the VG name must match the lvmd device-class
# packaged in the MicroShift image.
LVM_DISK="${LVM_DISK:-/var/lib/${NAME}/lvmdisk.image}"
VG_NAME="myvg1"
ARGOCD_VERSION="${ARGOCD_VERSION:-v3.1.0}"
KUBEVIRT_VERSION="${KUBEVIRT_VERSION:-}" # empty = the published stable
WORKDIR="${WORKDIR:-$(mktemp -d /tmp/dotvirt-e2e.XXXXXX)}"
mkdir -p "${WORKDIR}" # an env-supplied WORKDIR (CI) arrives uncreated
PROJECT="${PROJECT:-team-a}"
TIMEOUT_INSTALL="${TIMEOUT_INSTALL:-900}" # per-phase ceiling, seconds

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
export KUBECONFIG="${WORKDIR}/kubeconfig"

log() { echo "=== [$(date +%H:%M:%S)] $*"; }
# Every exec is time-bounded: a single hung attempt would otherwise defeat the
# retry ceiling (checked only BETWEEN attempts) and stall the job to its kill.
pexec() { timeout 30 podman exec "${NAME}" "$@"; }
kc() { kubectl --request-timeout=30s "$@"; }

clean() {
	podman rm -f "${NAME}" 2>/dev/null || true
	if [ -f "${LVM_DISK}" ]; then
		vgremove -f -y "${VG_NAME}" 2>/dev/null || true
		dev="$(losetup -j "${LVM_DISK}" | cut -d: -f1)"
		[ -n "${dev}" ] && losetup -d "${dev}" 2>/dev/null || true
		rm -rf "$(dirname "${LVM_DISK}")"
	fi
}

diagnostics() {
	log "DIAGNOSTICS (rc=$1)"
	kc get nodes -o wide 2>&1 | head -5 || true
	kc get pods -A 2>&1 | grep -v Running | head -40 || true
	kc get dotvirt -n dotvirt -o yaml 2>&1 | sed -n '/status:/,$p' | head -60 || true
	kc get applications.argoproj.io -A 2>&1 | head -10 || true
	log "waiting containers (reason + message)"
	kc get pods -A -o json 2>/dev/null | jq -r '.items[]
		| . as $p | .status.containerStatuses[]? | select(.state.waiting)
		| $p.metadata.namespace + "/" + $p.metadata.name + " [" + .name + "]: "
		+ .state.waiting.reason + " - " + (.state.waiting.message // "")' 2>/dev/null | head -20 || true
	log "recent warning events"
	kc get events -A --field-selector type=Warning \
		--sort-by=.lastTimestamp 2>/dev/null | tail -15 || true
	log "pvcs"
	kc get pvc -A 2>&1 | head -8 || true
	log "topolvm logs"
	kc -n topolvm-system logs deploy/topolvm-controller --tail=15 --all-containers 2>&1 | tail -15 || true
	kc -n topolvm-system logs ds/topolvm-lvmd-0 --tail=10 2>&1 | tail -10 || true
	log "dotvirt namespace state"
	kc get pods,deploy -n dotvirt -o wide 2>&1 | head -12 || true
	log "forgejo logs"
	kc logs -n dotvirt deploy/dotvirt-forgejo --tail=25 2>&1 | tail -25 || true
	log "dotvirt app logs"
	kc logs -n dotvirt deploy/dotvirt --tail=40 2>&1 | tail -40 || true
	log "operator logs"
	op="$(kc -n dotvirt-operator get deploy -o name 2>/dev/null | head -1)"
	[ -n "${op}" ] && kc logs -n dotvirt-operator "${op}" --tail=60 2>&1 | tail -60 || true
	log "microshift journal (best-effort; exec can hang on this podman)"
	pexec journalctl -u microshift --no-pager -n 40 2>&1 || true
	log "container console tail"
	podman logs --tail 20 "${NAME}" 2>&1 || true
}

if [ "${CLEAN:-0}" = "1" ]; then
	clean
	log "cleaned up"
	exit 0
fi

[ "$(id -u)" = "0" ] || { echo "run with sudo: MicroShift needs real privileges" >&2; exit 1; }
trap 'rc=$?; [ ${rc} -ne 0 ] && diagnostics ${rc}; exit ${rc}' EXIT

# retry <label> <seconds> <cmd...>: poll until cmd succeeds or the ceiling hits.
# A heartbeat keeps the CI log live. The command may be a shell FUNCTION, so no
# timeout wrapper here — each helper bounds its own attempt (pexec wraps podman
# in timeout, kc carries --request-timeout, curl carries --max-time).
retry() {
	local label="$1" ceiling="$2" start beat
	shift 2
	start=$(date +%s)
	beat=$(date +%s)
	until "$@" >/dev/null 2>&1; do
		local now
		now=$(date +%s)
		if [ $((now - start)) -gt "${ceiling}" ]; then
			echo "ERROR: timed out waiting for ${label}; the last attempt said:" >&2
			"$@" >&2 2>&1 || true
			return 1
		fi
		if [ $((now - beat)) -ge 60 ]; then
			echo "    ... still waiting for ${label} ($((now - start))s)"
			beat=${now}
		fi
		sleep 5
	done
	log "${label} ($(($(date +%s) - start))s)"
}

# ── 1. Boot MicroShift as a privileged container ───────────────────────────────
log "host prerequisites"
modprobe openvswitch || true
if [ ! -f "${LVM_DISK}" ]; then
	mkdir -p "$(dirname "${LVM_DISK}")"
	truncate --size=24G "${LVM_DISK}" # sparse; the distro lvmd reserves spare-gb:10, forgejo 5Gi + drafts 1Gi ride what remains
	dev="$(losetup --find --show --nooverlap "${LVM_DISK}")"
	vgcreate -f -y "${VG_NAME}" "${dev}"
fi

podman rm -f "${NAME}" 2>/dev/null || true
log "pulling ${MS_IMAGE}"
podman pull -q "${MS_IMAGE}" >/dev/null
log "starting ${NAME}"
vol_opts=(--tty --volume /dev:/dev)
for device in input snd dri; do
	[ -d "/dev/${device}" ] && vol_opts+=(--tmpfs "/dev/${device}")
done
podman run --privileged -d \
	--ulimit nofile=524288:524288 \
	--dns-search=. \
	"${vol_opts[@]}" \
	--tmpfs /var/lib/containers \
	--name "${NAME}" \
	--hostname "${NAME}" \
	"${MS_IMAGE}" >/dev/null

# The critical path avoids podman exec entirely: exec sessions into the booted
# systemd container hang intermittently (and forever) on the runner's podman.
# The apiserver is probed over TCP and the kubeconfig leaves via podman cp,
# which rides the container mount, not an exec session.
IP="$(podman inspect -f '{{.NetworkSettings.IPAddress}}' "${NAME}")"
apiserver_up() {
	[ "$(curl -ks -o /dev/null -w '%{http_code}' --max-time 5 "https://${IP}:6443/livez")" != "000" ]
}
retry "apiserver answering on ${IP}:6443" 600 apiserver_up

copy_kubeconfig() {
	podman cp "${NAME}:/var/lib/microshift/resources/kubeadmin/kubeconfig" "${WORKDIR}/kubeconfig.raw" 2>/dev/null \
		&& [ -s "${WORKDIR}/kubeconfig.raw" ]
}
retry "kubeadmin kubeconfig" 300 copy_kubeconfig
# A fresh minimal kubeconfig around the extracted client credential — never
# sed-edit YAML (a pattern like 'cluster:' also matches 'clusters:' and quietly
# corrupts the file). The server cert isn't SAN'd for the container IP, so
# verification is skipped: a throwaway test cluster.
CLIENT_CERT="$(awk '/client-certificate-data:/{print $2}' "${WORKDIR}/kubeconfig.raw")"
CLIENT_KEY="$(awk '/client-key-data:/{print $2}' "${WORKDIR}/kubeconfig.raw")"
[ -n "${CLIENT_CERT}" ] && [ -n "${CLIENT_KEY}" ] || { echo "ERROR: no client credential in the kubeadmin kubeconfig" >&2; exit 1; }
cat >"${KUBECONFIG}" <<KCFG
apiVersion: v1
kind: Config
clusters:
- name: e2e
  cluster:
    server: https://${IP}:6443
    insecure-skip-tls-verify: true
contexts:
- name: e2e
  context:
    cluster: e2e
    user: admin
current-context: e2e
users:
- name: admin
  user:
    client-certificate-data: ${CLIENT_CERT}
    client-key-data: ${CLIENT_KEY}
KCFG
chmod 600 "${KUBECONFIG}"
kc_up() { kc get --raw /livez; }
retry "kubectl reaches the cluster" 60 kc_up

node_ready() { kc get nodes 2>/dev/null | grep -q ' Ready '; }
retry "node Ready" 300 node_ready
retry "router deployed" 600 kc -n openshift-ingress get deploy router-default
retry "storage class present" 600 sh -c 'kubectl get storageclass -o name | grep -q .'
# TopoLVM must be fully up before anything claims a PVC: lvmd (DaemonSet)
# mints /run/topolvm for the node plugin, and the controller provisions.
topolvm_ready() {
	[ "$(kc -n topolvm-system get pods --no-headers 2>/dev/null | awk '$3!="Running"' | wc -l)" = "0" ] \
		&& [ "$(kc -n topolvm-system get pods --no-headers 2>/dev/null | wc -l)" -ge 3 ]
}
retry "topolvm ready" 600 topolvm_ready

# ── 2. ArgoCD (hard dependency; dotvirt never installs it) ─────────────────────
log "installing Argo CD ${ARGOCD_VERSION}"
kc create namespace argocd --dry-run=client -o yaml | kc apply -f -
# MicroShift enforces SCCs: the community manifests pin fixed non-root UIDs
# (redis 999, dex), which restricted-v2 refuses — redis then never mints the
# argocd-redis secret and every other pod sits in CreateContainerConfigError.
# nonroot-v2 (any non-zero UID) is the narrowest grant this distro ships that
# admits them; production installs use OpenShift GitOps instead.
kc apply -f - <<SCC
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: e2e-scc-nonroot
rules:
- apiGroups: [security.openshift.io]
  resources: [securitycontextconstraints]
  resourceNames: [nonroot-v2]
  verbs: [use]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: e2e-argocd-nonroot
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: e2e-scc-nonroot
subjects:
- apiGroup: rbac.authorization.k8s.io
  kind: Group
  name: system:serviceaccounts:argocd
SCC
curl -fsSL "https://raw.githubusercontent.com/argoproj/argo-cd/${ARGOCD_VERSION}/manifests/install.yaml" | kc apply -n argocd -f -
# Plain-HTTP server so the forge can deliver webhooks to the in-cluster Service
# without a trust store.
kc -n argocd patch configmap argocd-cmd-params-cm --type merge -p '{"data":{"server.insecure":"true"}}'
kc -n argocd rollout restart deploy argocd-server
for d in argocd-repo-server argocd-server argocd-applicationset-controller; do
	retry "argocd ${d} available" "${TIMEOUT_INSTALL}" kc -n argocd wait deploy/"${d}" --for=condition=Available --timeout=10s
done
appctrl_ready() {
	[ "$(kc -n argocd get statefulset argocd-application-controller -o jsonpath='{.status.readyReplicas}')" = "1" ]
}
retry "argocd application-controller ready" "${TIMEOUT_INSTALL}" appctrl_ready

# ── 3. KubeVirt (emulation: the loop applies VM objects, none needs to boot) ──
if [ -z "${KUBEVIRT_VERSION}" ]; then
	KUBEVIRT_VERSION="$(curl -fsSL https://storage.googleapis.com/kubevirt-prow/release/kubevirt/kubevirt/stable.txt)"
fi
log "installing KubeVirt ${KUBEVIRT_VERSION}"
kc apply -f "https://github.com/kubevirt/kubevirt/releases/download/${KUBEVIRT_VERSION}/kubevirt-operator.yaml"
kc apply -f - <<EOF
apiVersion: kubevirt.io/v1
kind: KubeVirt
metadata:
  name: kubevirt
  namespace: kubevirt
spec:
  configuration:
    developerConfiguration:
      useEmulation: true
EOF
retry "kubevirt Available" "${TIMEOUT_INSTALL}" \
	kc -n kubevirt wait kubevirt/kubevirt --for=condition=Available --timeout=10s
# The roundtrip references u1.medium/fedora; recent KubeVirt deploys the common
# instancetypes itself — install the bundle only when this one doesn't.
if ! kc get virtualmachineclusterinstancetype u1.medium >/dev/null 2>&1; then
	log "installing common-instancetypes bundle"
	kc apply -f "https://github.com/kubevirt/common-instancetypes/releases/latest/download/common-clusterinstancetypes-bundle.yaml"
	kc apply -f "https://github.com/kubevirt/common-instancetypes/releases/latest/download/common-clusterpreferences-bundle.yaml"
fi

# ── 4. dotvirt operator (published digest-pinned images) + CR ─────────────────
log "deploying the dotvirt operator"
make -C "${REPO_ROOT}/operator" deploy >/dev/null
retry "operator available" "${TIMEOUT_INSTALL}" \
	kc -n dotvirt-operator wait deploy --all --for=condition=Available --timeout=10s

log "applying the Dotvirt CR"
kc create namespace dotvirt --dry-run=client -o yaml | kc apply -f -
kc apply -f - <<EOF
apiVersion: dotvirt.io/v1alpha1
kind: Dotvirt
metadata:
  name: dotvirt
  namespace: dotvirt
spec:
  forge:
    managed: true
    # In-cluster URL: clones, Argo repo-creds and webhooks all resolve without
    # external DNS or a trust store — the point of this hermetic loop.
    url: http://dotvirt-forgejo.dotvirt.svc.cluster.local:3000
  argocd:
    namespace: argocd
    serverURL: http://argocd-server.argocd.svc.cluster.local
  ingress:
    type: route
EOF
retry "Dotvirt Available" "${TIMEOUT_INSTALL}" sh -c \
	"kubectl -n dotvirt get dotvirt dotvirt -o jsonpath='{.status.conditions[?(@.type==\"Available\")].status}' | grep -q True"
retry "dotvirt deployment ready" "${TIMEOUT_INSTALL}" \
	kc -n dotvirt wait deploy/dotvirt --for=condition=Available --timeout=10s

# ── 5. Reach the API from the host ────────────────────────────────────────────
log "port-forwarding dotvirt + forge"
kc -n dotvirt port-forward svc/dotvirt 18080:8080 >/dev/null 2>&1 &
kc -n dotvirt port-forward svc/dotvirt-forgejo 13000:3000 >/dev/null 2>&1 &
PF_PIDS="$(jobs -p)"
trap 'rc=$?; kill ${PF_PIDS} 2>/dev/null || true; [ ${rc} -ne 0 ] && diagnostics ${rc}; exit ${rc}' EXIT
BASE="http://127.0.0.1:18080"
FORGE="http://127.0.0.1:13000"
retry "dotvirt healthz" 120 curl -fsS --max-time 10 "${BASE}/api/healthz"

# Caller token: the loop runs as a real (admin) user, exactly like production.
kc -n dotvirt create serviceaccount e2e-admin --dry-run=client -o yaml | kc apply -f -
kc create clusterrolebinding e2e-admin --clusterrole=cluster-admin \
	--serviceaccount=dotvirt:e2e-admin --dry-run=client -o yaml | kc apply -f -
TOK="$(kc -n dotvirt create token e2e-admin --duration=6h)"
FTOK="$(kc get secret dotvirt-forge -n dotvirt -o jsonpath='{.data.token}' | base64 -d | tr -d '[:space:]')"

api() { curl -fsS --max-time 30 -H "Authorization: Bearer ${TOK}" "$@"; }
forge() { curl -fsS --max-time 30 -H "Authorization: token ${FTOK}" -H 'Content-Type: application/json' "$@"; }

# ── 6. Create the tenant project through dotvirt itself ───────────────────────
log "creating project ${PROJECT} via the dotvirt API"
api -X POST "${BASE}/api/projects" -H 'Content-Type: application/json' \
	-d "{\"name\":\"${PROJECT}\"}" >/dev/null
PR="$(api -X POST "${BASE}/api/draft/propose?project=platform" -H 'Content-Type: application/json' \
	-d '{"title":"e2e: create project","message":""}' | grep -o '"prNumber":[0-9]*' | cut -d: -f2)"
[ -n "${PR}" ] || { echo "ERROR: platform propose returned no PR" >&2; exit 1; }
merge_pr() {
	local repo="$1" pr="$2" i
	for i in $(seq 1 30); do
		forge -o /dev/null -X POST "${FORGE}/api/v1/repos/dotvirt/${repo}/pulls/${pr}/merge" \
			-d '{"Do":"merge"}' 2>/dev/null && return 0
		sleep 2
	done
	echo "ERROR: merge of ${repo}#${pr} failed" >&2
	return 1
}
merge_pr platform "${PR}"
retry "namespace ${PROJECT} labeled" "${TIMEOUT_INSTALL}" sh -c \
	"kubectl get namespace ${PROJECT} -o jsonpath='{.metadata.labels.dotvirt\.io/project}' 2>/dev/null | grep -q ."
retry "tenant repo exists" 300 forge -o /dev/null "${FORGE}/api/v1/repos/dotvirt/${PROJECT}"
retry "project in inventory" 300 sh -c \
	"curl -fsS -H 'Authorization: Bearer ${TOK}' '${BASE}/api/inventory' | grep -q '\"${PROJECT}\"'"

# ── 6b. Anchor VM: the repo must never render EMPTY ───────────────────────────
# Argo's automated.allowEmpty stays false by design (an emptied repo cannot
# wipe a tier), so the round-trip's delete would never prune if its VM were the
# repo's only manifest. One permanent anchor VM keeps the source non-empty —
# and doubles as a second pass over the create loop.
log "staging the anchor VM"
api -X POST "${BASE}/api/vms" -H 'Content-Type: application/json' -d "{
  \"name\":\"anchor\",\"namespace\":\"${PROJECT}\",\"instancetype\":\"u1.medium\",\"preference\":\"fedora\",
  \"osImage\":{\"name\":\"fedora\",\"namespace\":\"openshift-virtualization-os-images\"},
  \"diskSize\":\"10Gi\",\"running\":false}" >/dev/null
PR="$(api -X POST "${BASE}/api/draft/propose?project=${PROJECT}" -H 'Content-Type: application/json' \
	-d '{"title":"e2e: anchor","message":""}' | grep -o '"prNumber":[0-9]*' | cut -d: -f2)"
merge_pr "${PROJECT}" "${PR}"
anchor_in_cluster() { kc get vm anchor -n "${PROJECT}"; }
retry "anchor VM in cluster" 400 anchor_in_cluster

# ── 7. The round-trip itself ───────────────────────────────────────────────────
log "running the GitOps round-trip"
OUT="${WORKDIR}/roundtrip.txt"
BASE="${BASE}" FORGE="${FORGE}" PROJECT="${PROJECT}" NS="${PROJECT}" \
	TOK="${TOK}" FTOK="${FTOK}" TIMEOUT=300 \
	"${REPO_ROOT}/hack/e2e-roundtrip.sh" | tee "${OUT}"

# The measurement script prints TIMEOUT per stalled hop instead of failing;
# in CI a stalled hop IS the failure.
if grep -q "TIMEOUT\|!!" "${OUT}"; then
	echo "ERROR: the round-trip stalled (see above)" >&2
	exit 1
fi
log "round-trip complete — the real loop holds"
