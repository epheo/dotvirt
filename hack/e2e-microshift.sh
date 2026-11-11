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
# KUBEVIRT=0 proves the other shape dotvirt installs on: no KubeVirt, served
# through a portail Gateway, with the modules reporting what the cluster has.
# APP_IMAGE_TAR and OPERATOR_IMAGE_TAR (docker archives of images built from this
# tree) replace the published images, for what no release carries yet.
#
# Requires root privileges (MicroShift needs OVS + cgroups), podman, kubectl.
#   sudo hack/e2e-microshift.sh          # full run
#   CLEAN=1 sudo hack/e2e-microshift.sh  # tear down container + loopback VG
set -euo pipefail

# Pinned by digest: the tag moves with each MicroShift build.
MS_IMAGE="${MS_IMAGE:-ghcr.io/epheo/microshift:5.0.0_202610291612.p0_g0f0de241_5.0.0_okd_scos.6_epheo.44@sha256:884cd9c8093d603bdd4377d532f6eb21e31c0c31aa75a183d4659a5fb2966289}"
NAME="${NAME:-dotvirt-e2e-microshift}"
KUBEVIRT="${KUBEVIRT:-1}"
APP_IMAGE_TAR="${APP_IMAGE_TAR:-}"
OPERATOR_IMAGE_TAR="${OPERATOR_IMAGE_TAR:-}"
if [ -n "${APP_IMAGE_TAR}${OPERATOR_IMAGE_TAR}" ] && { [ -z "${APP_IMAGE_TAR}" ] || [ -z "${OPERATOR_IMAGE_TAR}" ]; }; then
	echo "ERROR: set both APP_IMAGE_TAR and OPERATOR_IMAGE_TAR, or neither" >&2
	exit 1
fi
# The loopback VG behind r-drbd. The image turns a VG of this name with a thin
# pool of that name into a Pool and a default StorageClass by itself.
LVM_DISK="${LVM_DISK:-/var/lib/${NAME}/lvmdisk.image}"
VG_NAME="myvg1"
THIN_POOL="thin"
WORKDIR="${WORKDIR:-$(mktemp -d /tmp/dotvirt-e2e.XXXXXX)}"
mkdir -p "${WORKDIR}" # an env-supplied WORKDIR (CI) arrives uncreated
PROJECT="${PROJECT:-team-a}"
TIMEOUT_INSTALL="${TIMEOUT_INSTALL:-900}" # per-phase ceiling, seconds

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# ARGOCD_VERSION and KUBEVIRT_VERSION, each beside the digest of the manifest it
# installs, and PORTAIL_COMMIT.
source "${REPO_ROOT}/hack/versions.env"
export KUBECONFIG="${WORKDIR}/kubeconfig"

log() { echo "=== [$(date +%H:%M:%S)] $*"; }
# Every exec is time-bounded: a single hung attempt would otherwise defeat the
# retry ceiling (checked only BETWEEN attempts) and stall the job to its kill.
pexec() { timeout 30 podman exec "${NAME}" "$@"; }
kc() { kubectl --request-timeout=30s "$@"; }
# fetch <url> <file> <sha256>: download <url> to <file>, failing unless it hashes to <sha256>.
fetch() {
	curl -fsSL -o "$2" "$1"
	echo "$3  $2" | sha256sum --check --strict
}

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
	log "r-drbd state and logs"
	kc get pools,volumes.r-drbd.io -o wide 2>&1 | head -12 || true
	kc -n r-drbd-system get pods -o wide 2>&1 | head -8 || true
	kc -n r-drbd-system logs deploy/r-drbd-controller --all-containers --tail=15 2>&1 | tail -15 || true
	kc -n r-drbd-system logs ds/r-drbd-agent --all-containers --tail=15 2>&1 | tail -15 || true
	log "gateway state and logs"
	kc get gateways,httproutes -A -o wide 2>&1 | head -10 || true
	kc -n portail-system logs deploy/portail-controller --tail=20 2>&1 | tail -20 || true
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
	truncate --size=24G "${LVM_DISK}" # sparse; forgejo 5Gi + drafts 1Gi are thin
	dev="$(losetup --find --show --nooverlap "${LVM_DISK}")"
	vgcreate -f -y "${VG_NAME}" "${dev}"
	lvcreate -q -y --type thin-pool -l 90%FREE -n "${THIN_POOL}" "${VG_NAME}"
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
# portail is the edge (epheo.15+): the router is Removed by default, and the
# Gateway API experimental CRDs land from manifests.d once the core is up.
no_router() { [ -z "$(kc get namespace openshift-ingress --ignore-not-found -o name)" ]; }
retry "router removed" 600 no_router
retry "gateway api crds established" 600 kc wait --for=condition=Established --timeout=5s \
	crd/tcproutes.gateway.networking.k8s.io crd/udproutes.gateway.networking.k8s.io
# r-drbd serves replicas=1 volumes without the DRBD module this runner's kernel
# lacks: this lane proves dotvirt on r-drbd-local, not replication.
pool_available() {
	kc get pools -o jsonpath='{range .items[*]}{.spec.vg}={.status.conditions[?(@.type=="Available")].status}{"\n"}{end}' \
		| grep -qx "${VG_NAME}=True"
}
retry "r-drbd Pool available" 600 pool_available
default_class() {
	[ "$(kc get storageclass -o jsonpath='{range .items[?(@.metadata.annotations.storageclass\.kubernetes\.io/is-default-class=="true")]}{.metadata.name}{end}')" = "r-drbd-local" ]
}
retry "r-drbd-local is the default class" 300 default_class

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
# The ApplicationSet CRD outgrows the client-side apply annotation limit (Argo CD >= 3.3).
fetch "https://raw.githubusercontent.com/argoproj/argo-cd/${ARGOCD_VERSION}/manifests/install.yaml" \
	"${WORKDIR}/argocd-install.yaml" "${ARGOCD_INSTALL_SHA256}"
kc apply -n argocd --server-side --force-conflicts -f "${WORKDIR}/argocd-install.yaml"
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
if [ "${KUBEVIRT}" = "1" ]; then
	log "installing KubeVirt ${KUBEVIRT_VERSION}"
	fetch "https://github.com/kubevirt/kubevirt/releases/download/${KUBEVIRT_VERSION}/kubevirt-operator.yaml" \
		"${WORKDIR}/kubevirt-operator.yaml" "${KUBEVIRT_OPERATOR_SHA256}"
	kc apply -f "${WORKDIR}/kubevirt-operator.yaml"
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
fi

# ── 4. Edge: portail, with a Gateway only dotvirt's namespace may attach to ───
if [ "${KUBEVIRT}" = "0" ]; then
	log "installing portail ${PORTAIL_COMMIT}"
	kc apply --server-side --force-conflicts -k "https://github.com/epheo/portail//deploy/install?ref=${PORTAIL_COMMIT}"
	retry "portail controller available" "${TIMEOUT_INSTALL}" \
		kc -n portail-system wait deploy/portail-controller --for=condition=Available --timeout=10s
	kc create namespace gateway-e2e --dry-run=client -o yaml | kc apply -f -
	kc apply -f - <<EOF
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: edge
  namespace: gateway-e2e
spec:
  gatewayClassName: portail
  listeners:
    - name: http
      port: 8080
      protocol: HTTP
      allowedRoutes:
        namespaces:
          from: Selector
          selector:
            matchLabels:
              kubernetes.io/metadata.name: dotvirt
EOF
fi

# ── 5. dotvirt operator + CR ───────────────────────────────────────────────────
# load_image <archive> <ref>: into cri-o's store, past the registry nobody has.
# The store is spelled out so the image lands where cri-o reads, whatever
# skopeo's own storage.conf says.
load_image() {
	podman cp "$1" "${NAME}:/tmp/e2e-image.tar" \
		&& timeout 300 podman exec "${NAME}" skopeo copy -q docker-archive:/tmp/e2e-image.tar \
			"containers-storage:[overlay@/var/lib/containers/storage+/run/containers/storage]$2"
}
if [ -n "${APP_IMAGE_TAR}" ]; then
	retry "app image loaded" 900 load_image "${APP_IMAGE_TAR}" localhost/dotvirt:e2e
	retry "operator image loaded" 900 load_image "${OPERATOR_IMAGE_TAR}" localhost/dotvirt-operator:e2e
fi

log "deploying the dotvirt operator"
make -C "${REPO_ROOT}/operator" deploy >/dev/null
if [ -n "${APP_IMAGE_TAR}" ]; then
	kc -n dotvirt-operator set image deploy/dotvirt-operator-controller manager=localhost/dotvirt-operator:e2e
	kc -n dotvirt-operator set env deploy/dotvirt-operator-controller RELATED_IMAGE_DOTVIRT=localhost/dotvirt:e2e
fi
retry "operator rolled out" "${TIMEOUT_INSTALL}" \
	kc -n dotvirt-operator rollout status deploy/dotvirt-operator-controller --timeout=10s
retry "operator available" "${TIMEOUT_INSTALL}" \
	kc -n dotvirt-operator wait deploy --all --for=condition=Available --timeout=10s

GATEWAY_HOST="dotvirt.e2e.test"
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
$(if [ "${KUBEVIRT}" = "0" ]; then
	printf '    host: %s\n    gateway: {name: edge, namespace: gateway-e2e}\n' "${GATEWAY_HOST}"
else
	echo '    type: route'
fi)
EOF
retry "Dotvirt Available" "${TIMEOUT_INSTALL}" sh -c \
	"kubectl -n dotvirt get dotvirt dotvirt -o jsonpath='{.status.conditions[?(@.type==\"Available\")].status}' | grep -q True"
retry "dotvirt deployment ready" "${TIMEOUT_INSTALL}" \
	kc -n dotvirt wait deploy/dotvirt --for=condition=Available --timeout=10s

# ── 6. Reach the API from the host ────────────────────────────────────────────
# Before any port-forward: a failure here is past the trap that stops them.
if [ "${KUBEVIRT}" = "0" ]; then
	retry "gateway data plane available" "${TIMEOUT_INSTALL}" \
		kc -n gateway-e2e wait deploy/portail-edge --for=condition=Available --timeout=10s
fi
log "port-forwarding dotvirt + forge"
kc -n dotvirt port-forward svc/dotvirt 18080:8080 >/dev/null 2>&1 &
kc -n dotvirt port-forward svc/dotvirt-forgejo 13000:3000 >/dev/null 2>&1 &
if [ "${KUBEVIRT}" = "0" ]; then
	kc -n gateway-e2e port-forward svc/portail-edge 18081:8080 >/dev/null 2>&1 &
fi
PF_PIDS="$(jobs -p)"
trap 'rc=$?; kill ${PF_PIDS} 2>/dev/null || true; [ ${rc} -ne 0 ] && diagnostics ${rc}; exit ${rc}' EXIT
BASE="http://127.0.0.1:18080"
FORGE="http://127.0.0.1:13000"
retry "dotvirt healthz" 120 curl -fsS --max-time 10 "${BASE}/api/healthz"
# A HostNetwork edge (portail, a router) dials the Service from the node's
# network. OVN-Kubernetes SNATs that to the gateway router's join IP, which the
# operand NetworkPolicy admits only through openshift-host-network's policy
# group: without it the edge answers 502. A pod IP would not tell, as a node's
# own traffic to its local pods is admitted whatever the policy. nsenter, not
# podman exec: no exec session to hang.
NODE_NET="/proc/$(podman inspect -f '{{.State.Pid}}' "${NAME}")/ns/net"
SVC_IP="$(kc -n dotvirt get svc dotvirt -o jsonpath='{.spec.clusterIP}')"
HOST_GROUP=policy-group.network.openshift.io/host-network
node_reaches_dotvirt() {
	nsenter --net="${NODE_NET}" curl -fsS -o /dev/null --max-time 10 "http://${SVC_IP}:8080/api/healthz"
}
# A policy refusal is a drop, so curl's timeout (28): a broken nsenter or a
# restarting dotvirt fails otherwise and proves nothing.
node_refused() {
	local rc=0
	node_reaches_dotvirt || rc=$?
	[ "${rc}" -eq 28 ]
}
retry "dotvirt answers the node's host network" 120 node_reaches_dotvirt
# The check proves itself: without the group the same call must time out.
kc label namespace openshift-host-network "${HOST_GROUP}-" >/dev/null
retry "the node's host network refused without its policy group" 120 node_refused
kc label namespace openshift-host-network "${HOST_GROUP}=" >/dev/null
retry "dotvirt answers the node's host network again" 120 node_reaches_dotvirt

# Caller token: the loop runs as a real (admin) user, exactly like production.
kc -n dotvirt create serviceaccount e2e-admin --dry-run=client -o yaml | kc apply -f -
kc create clusterrolebinding e2e-admin --clusterrole=cluster-admin \
	--serviceaccount=dotvirt:e2e-admin --dry-run=client -o yaml | kc apply -f -
TOK="$(kc -n dotvirt create token e2e-admin --duration=6h)"
FTOK="$(kc get secret dotvirt-forge -n dotvirt -o jsonpath='{.data.token}' | base64 -d | tr -d '[:space:]')"

api() { curl -fsS --max-time 30 -H "Authorization: Bearer ${TOK}" "$@"; }
forge() { curl -fsS --max-time 30 -H "Authorization: token ${FTOK}" -H 'Content-Type: application/json' "$@"; }

# ── 7. Create the tenant project through dotvirt itself ───────────────────────
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

# The supported personas (docs/personas.md), each offered what TestPersonas
# pins for MicroShift, in both lanes. The operator binds the network admin role
# to Group dotvirt-platform-admins; a ServiceAccount joins no Group, so it is
# bound here.
check_personas() {
	log "checking the supported personas"
	persona_token() {
		local sa=$1 role
		shift
		kc -n "${PROJECT}" create serviceaccount "${sa}" --dry-run=client -o yaml | kc apply -f - >/dev/null
		for role in "$@"; do
			kc -n "${PROJECT}" create rolebinding "${sa}-${role}" --clusterrole="${role}" \
				--serviceaccount="${PROJECT}:${sa}" --dry-run=client -o yaml | kc apply -f - >/dev/null
		done
		kc -n "${PROJECT}" create token "${sa}" --duration=1h
	}
	VIEWER_TOK="$(persona_token e2e-viewer view dotvirt-tenant-egress-view)"
	AUTHOR_TOK="$(persona_token e2e-author edit dotvirt-tenant-egress-view)"
	PROJECT_ADMIN_TOK="$(persona_token e2e-project-admin admin dotvirt-tenant-egress)"
	NETWORK_ADMIN_TOK="$(persona_token e2e-network-admin)"
	kc create clusterrolebinding e2e-network-admin --clusterrole=dotvirt-platform-network-admin \
		--serviceaccount="${PROJECT}:e2e-network-admin" --dry-run=client -o yaml | kc apply -f - >/dev/null
	offered() {
		local got
		got="$(curl -fsS --max-time 30 -H "Authorization: Bearer $1" "${BASE}/api/capabilities?namespace=${PROJECT}" |
			jq -c '.namespace | {repoRead: .project.repoRead, vmCreate: .vm.create, console, networkCreate: .network.create, egressFirewallCreate: .egressFirewall.create}')" || return 1
		echo "offered ${got}, wanted $2"
		[ "${got}" = "$2" ]
	}
	# A grant offers a new object only where the cluster serves its kind: a kind
	# nothing syncs is never offered. MicroShift serves EgressFirewalls from
	# epheo.39 on, UserDefinedNetworks from epheo.44 on.
	# served <resource> <var> sets <var> to the cluster's answer. Only a NotFound
	# means not served; any other failure is asked again, as the server counts a
	# failed read as served.
	served() {
		local out err answer
		err="$(mktemp)"
		if out="$(kc get --raw /apis/k8s.ovn.org/v1 2>"${err}")"; then
			rm -f "${err}"
			# A body that does not parse is asked again.
			answer="$(jq -r --arg r "$1" '[.resources[].name] | index($r) != null' <<<"${out}")" || return 1
			printf -v "$2" '%s' "${answer}"
			return 0
		fi
		if grep -q 'NotFound' "${err}"; then
			rm -f "${err}"
			printf -v "$2" '%s' false
			return 0
		fi
		cat "${err}"
		rm -f "${err}"
		return 1
	}
	retry "the cluster's answer on EgressFirewalls" 120 served egressfirewalls egress
	retry "the cluster's answer on UserDefinedNetworks" 120 served userdefinednetworks udn
	log "EgressFirewalls served: ${egress}; UserDefinedNetworks served: ${udn}"
	# Without KubeVirt, edit and admin aggregate no VM rights.
	vms=true
	[ "${KUBEVIRT}" != "0" ] || vms=false
	retry "a project viewer reads the repo and authors nothing" 180 offered "${VIEWER_TOK}" \
		'{"repoRead":true,"vmCreate":false,"console":false,"networkCreate":false,"egressFirewallCreate":false}'
	retry "a project author authors VMs, networks where served, and opens consoles" 180 offered "${AUTHOR_TOK}" \
		'{"repoRead":true,"vmCreate":'"${vms}"',"console":'"${vms}"',"networkCreate":'"${udn}"',"egressFirewallCreate":false}'
	retry "a project admin authors VMs, and networks and egress firewalls where served" 180 offered "${PROJECT_ADMIN_TOK}" \
		'{"repoRead":true,"vmCreate":'"${vms}"',"console":'"${vms}"',"networkCreate":'"${udn}"',"egressFirewallCreate":'"${egress}"'}'
	# View reads a project's networks wherever they are served. Created here, not
	# through git: what is checked is the viewer's read.
	if [ "${udn}" = true ]; then
		kc apply -f - >/dev/null <<YAML
apiVersion: k8s.ovn.org/v1
kind: UserDefinedNetwork
metadata: {name: e2e-reads, namespace: ${PROJECT}}
spec:
  topology: Layer2
  layer2: {role: Secondary, subnets: [10.20.0.0/24]}
YAML
		reads_network() {
			curl -fsS --max-time 30 -H "Authorization: Bearer ${VIEWER_TOK}" "${BASE}/api/networks" |
				jq -e --arg ns "${PROJECT}" '[.networks[] | select(.namespace == $ns and .name == "e2e-reads" and .backing == "UserDefinedNetwork")] | length == 1'
		}
		retry "a project viewer reads the project's network" 180 reads_network
	fi
	platform_offered() {
		local got
		got="$(curl -fsS --max-time 30 -H "Authorization: Bearer ${NETWORK_ADMIN_TOK}" "${BASE}/api/capabilities" |
			jq -c '.cluster | {platform, platformRead, hosts}')" || return 1
		echo "offered ${got}"
		[ "${got}" = '{"platform":true,"platformRead":false,"hosts":false}' ]
	}
	retry "a platform network admin stages platform changes and sees no project" 180 platform_offered
	no_project_caps() {
		[ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 30 -H "Authorization: Bearer ${NETWORK_ADMIN_TOK}" \
			"${BASE}/api/capabilities?namespace=${PROJECT}")" = 404 ]
	}
	retry "a platform network admin is not in the project" 180 no_project_caps
}

# ── 8. Without KubeVirt the loop ends here: nothing to stage, what matters is
# that the install came up and says what this cluster has.
if [ "${KUBEVIRT}" = "0" ]; then
	through_gateway() {
		curl -fsS --max-time 10 -H "Host: ${GATEWAY_HOST}" http://127.0.0.1:18081/api/healthz
	}
	retry "dotvirt answers through the portail Gateway" "${TIMEOUT_INSTALL}" through_gateway
	modules_right() {
		api "${BASE}/api/modules" | jq -e '.vms == false and .routes == true and .replicatedStorage == true'
	}
	retry "modules report no VMs, routes and r-drbd" 180 modules_right
	# A namespace admin, not the cluster-admin everything above runs as: KubeVirt's
	# aggregated roles are absent, and admin alone must still see its namespace.
	kc -n "${PROJECT}" create serviceaccount e2e-tenant --dry-run=client -o yaml | kc apply -f -
	kc -n "${PROJECT}" create rolebinding e2e-tenant --clusterrole=admin \
		--serviceaccount="${PROJECT}:e2e-tenant" --dry-run=client -o yaml | kc apply -f -
	TENANT_TOK="$(kc -n "${PROJECT}" create token e2e-tenant --duration=1h)"
	tenant_sees_project() {
		curl -fsS --max-time 30 -H "Authorization: Bearer ${TENANT_TOK}" "${BASE}/api/inventory" \
			| jq -e --arg p "${PROJECT}" '[.projects[].name] == [$p] and ([.projects[0].namespaces[].canAuthor] | all)'
	}
	retry "a namespace admin sees and may author its project" 180 tenant_sees_project
	warnings="$(api "${BASE}/api/inventory" | jq -c '.warnings // []')"
	[ "${warnings}" = "[]" ] || { echo "ERROR: inventory warnings without KubeVirt: ${warnings}" >&2; exit 1; }
	# On by default; community Argo CD has no ArgoCD resource for the operator to set it on.
	sync_waits_on_argocd_cm() {
		[ "$(kc -n dotvirt get dotvirt dotvirt -o jsonpath='{.status.conditions[?(@.type=="SyncImpersonationReady")].reason}')" = "ManualStep" ]
	}
	retry "sync impersonation is asked for and waits on argocd-cm" 360 sync_waits_on_argocd_cm
	# Published images may predate these rules; this tree must hold them.
	if [ -n "${APP_IMAGE_TAR}" ]; then
		check_personas
	fi
	log "no-KubeVirt install complete"
	exit 0
fi

# ── 9. Anchor VM: the repo must never render EMPTY ───────────────────────────
# Argo's automated.allowEmpty stays false by design (an emptied repo cannot
# wipe a tier), so the round-trip's delete would never prune if its VM were the
# repo's only manifest. One permanent anchor VM keeps the source non-empty —
# and doubles as a second pass over the create loop.
log "staging the anchor VM"
api -X POST "${BASE}/api/vms" -H 'Content-Type: application/json' -d "{
  \"name\":\"anchor\",\"namespace\":\"${PROJECT}\",\"instancetype\":\"u1.medium\",\"preference\":\"fedora\",
  \"osImage\":{\"name\":\"fedora\",\"namespace\":\"openshift-virtualization-os-images\"},
  \"diskSize\":\"10Gi\",\"running\":false}" >/dev/null
# Selects no pod, so it blocks nothing: a second declared kind for the repo read check.
api -X POST "${BASE}/api/networkpolicies" -H 'Content-Type: application/json' \
	-d "{\"name\":\"e2e-reads\",\"namespace\":\"${PROJECT}\",\"appliedTo\":{\"e2e-selects\":\"nothing\"}}" >/dev/null
PR="$(api -X POST "${BASE}/api/draft/propose?project=${PROJECT}" -H 'Content-Type: application/json' \
	-d '{"title":"e2e: anchor","message":""}' | grep -o '"prNumber":[0-9]*' | cut -d: -f2)"
merge_pr "${PROJECT}" "${PR}"
anchor_in_cluster() { kc get vm anchor -n "${PROJECT}"; }
retry "anchor VM in cluster" 400 anchor_in_cluster

# Published images may predate these rules; this tree must hold them.
if [ -n "${APP_IMAGE_TAR}" ]; then
	# A repo holds every kind it declares, here VMs and a NetworkPolicy. Kubernetes
	# answers who reads it: a namespace admin may, a role listing VMs alone may not.
	log "checking who reads the project's repo"
	kc -n "${PROJECT}" create serviceaccount e2e-admin-reader --dry-run=client -o yaml | kc apply -f -
	kc -n "${PROJECT}" create rolebinding e2e-admin-reader --clusterrole=admin \
		--serviceaccount="${PROJECT}:e2e-admin-reader" --dry-run=client -o yaml | kc apply -f -
	kc -n "${PROJECT}" create serviceaccount e2e-narrow --dry-run=client -o yaml | kc apply -f -
	kc -n "${PROJECT}" apply -f - <<YAML
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: {name: e2e-narrow}
rules:
- {apiGroups: ["kubevirt.io"], resources: ["virtualmachines"], verbs: ["list"]}
- {apiGroups: [""], resources: ["namespaces"], verbs: ["get"]}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: {name: e2e-narrow}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: Role, name: e2e-narrow}
subjects: [{kind: ServiceAccount, name: e2e-narrow, namespace: "${PROJECT}"}]
YAML
	ADMIN_READER_TOK="$(kc -n "${PROJECT}" create token e2e-admin-reader --duration=1h)"
	NARROW_TOK="$(kc -n "${PROJECT}" create token e2e-narrow --duration=1h)"
	history_answers() {
		local got
		got="$(curl -s -o /dev/null -w '%{http_code}' --max-time 30 -H "Authorization: Bearer $1" \
			"${BASE}/api/projects/${PROJECT}/history")"
		echo "history answered ${got}, wanted $2"
		[ "${got}" = "$2" ]
	}
	# Until the mirror holds the merge the repo declares nothing, so nothing is
	# refused. The poll is the backstop when no webhook lands.
	mirror_holds_anchor() {
		curl -fsS --max-time 30 -H "Authorization: Bearer ${ADMIN_READER_TOK}" \
			"${BASE}/api/projects/${PROJECT}/history" | grep -q 'e2e: anchor'
	}
	retry "the mirror holds the anchor merge" 400 mirror_holds_anchor
	retry "a namespace admin reads its project's repo" 180 history_answers "${ADMIN_READER_TOK}" 200
	retry "a role listing VMs alone is refused the repo" 180 history_answers "${NARROW_TOK}" 403
	# Kubernetes will not get them the namespace, so neither will dotvirt.
	kc -n "${PROJECT}" create serviceaccount e2e-no-get --dry-run=client -o yaml | kc apply -f -
	kc -n "${PROJECT}" create role e2e-no-get --verb=list --resource=virtualmachines.kubevirt.io \
		--dry-run=client -o yaml | kc apply -f -
	kc -n "${PROJECT}" create rolebinding e2e-no-get --role=e2e-no-get \
		--serviceaccount="${PROJECT}:e2e-no-get" --dry-run=client -o yaml | kc apply -f -
	NO_GET_TOK="$(kc -n "${PROJECT}" create token e2e-no-get --duration=1h)"
	sees_no_project() {
		curl -fsS --max-time 30 -H "Authorization: Bearer ${NO_GET_TOK}" "${BASE}/api/inventory" | jq -e '.projects | length == 0'
	}
	retry "a token that may not get the namespace sees no project" 180 sees_no_project
	# KubeVirt (instancetype.kubevirt.io:view) and CDI (cdi.kubevirt.io:config-reader)
	# grant these to every authenticated user, so a token with no grant reads them
	# as the admin does.
	catalog_as() {
		curl -fsS --max-time 30 -H "Authorization: Bearer $1" "${BASE}/api/options" | jq -c '[.instancetypes, .preferences]' &&
			curl -fsS --max-time 30 -H "Authorization: Bearer $1" "${BASE}/api/storage/classes" | jq -c '[.[] | .profile]'
	}
	same_catalog() {
		local admin plain
		admin="$(catalog_as "${TOK}")" && plain="$(catalog_as "${NO_GET_TOK}")" || return 1
		[ "${admin}" = "${plain}" ] || { echo "admin ${admin}"; echo "plain ${plain}"; return 1; }
	}
	retry "any authenticated user reads KubeVirt's and CDI's catalogs" 180 same_catalog
	# MicroShift grants signed-in users no storage classes; the operator's
	# dotvirt-storage-catalog binding does, as OpenShift's basic-user role.
	reads_storage_classes() {
		curl -fsS --max-time 30 -H "Authorization: Bearer ${NO_GET_TOK}" "${BASE}/api/storage/classes" | jq -e 'length > 0'
	}
	retry "any authenticated user reads the storage classes" 180 reads_storage_classes
	check_personas
fi

# ── 10. The round-trip itself ───────────────────────────────────────────────────
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
