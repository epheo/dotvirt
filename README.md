# dotvirt

A vCenter-like WebUI that closes the gap between point-and-click VM operation and
GitOps. dotvirt **edits git repos** of KubeVirt manifests and **works alongside
ArgoCD** — Argo stays the only thing that applies state to the cluster; dotvirt is
the friendly inventory + editor on top of git and Argo's status.

It is **multi-user and multi-tenant as a thin lens that owns nothing**: it rides
the cluster's own authentication and RBAC.

## What it does

- **Inventory** — a live, per-user **project → namespace → VM** tree, a global
  search (name / IP / label), and a WebSocket stream that repaints on any git,
  cluster, or Argo change.
- **VM lifecycle** — create, edit, and delete through git PRs; power on/off,
  restart, pause, and live-migrate as direct cluster actions.
- **Console** — an in-browser **VNC** console plus console-screenshot thumbnails.
- **Data** — snapshots, clones, and adopting a cluster-only VM back into git.
- **Networking** — vCenter-style distributed port groups over OVN-Kubernetes
  `UserDefinedNetwork` / `ClusterUserDefinedNetwork`, plus NMState uplinks.
- **Observability** — Prometheus/Thanos performance charts, firing alarms,
  namespace/project quota + capacity bands, and per-VM ArgoCD drift.
- **Cluster ops** — node cordon/uncordon, evacuate (= live-migrate away), and
  browser-direct CDI image upload.

## How isolation works

**Tenant boundary = repo boundary.** Git has no sub-repo read ACLs, so one shared
repo can't be multi-tenant (UI filtering would be security theater). Each project
is its own git repo. Isolation holds at two layers that reinforce each other:

1. **Cluster RBAC** — every read, edit, and console call is made with the *user's*
   own bearer token (pass-through). dotvirt enforces nothing; the API server is the
   sole authority on which namespaces a user can see.
2. **Git** — a user only ever learns a repo URL from a namespace they can already
   see (the URL lives in a namespace annotation), and per-project repos hold no
   cross-tenant data.

**Projects are live cluster facts, not a dotvirt registry.** A project is a set of
namespaces sharing a label, pointing at one git repo via an annotation:

```sh
oc label    ns <ns> dotvirt.io/project=<project>
oc annotate ns <ns> dotvirt.io/repo=https://forge/dotvirt/<project>.git
```

dotvirt reads those with the user's token and assembles a 3-level tree
(**project → namespace → VM**). The only non-cluster input is one Forgejo
credential used to clone/push every repo.

### The platform tier

Tenant repos carry only **namespaced** workloads (VMs, DataVolumes, secondary
networks). Cluster-scoped and cross-tenant objects — `ClusterUserDefinedNetwork`,
NMState `NodeNetworkConfigurationPolicy`, and `Namespace` creation — live in a
separate **platform repo**, and the UI offers them only to a user who passes a
`SelfSubjectAccessReview` for that kind. The boundary is enforced by ArgoCD
AppProjects, not by dotvirt: the `dotvirt-tenants` project has an empty
`clusterResourceWhitelist`, so a tenant PR **cannot** land cluster infrastructure
even if its manifest contains it — only the static `dotvirt-platform` app may.

## Auth

Users sign in by pasting a Kubernetes token (`oc whoami -t`, or
`kubectl create token <sa> -n <ns>`). dotvirt validates it with a **TokenReview**
(as its own ServiceAccount) and stores the raw token in a **signed, httpOnly
cookie** — stateless, no server session store. Every subsequent cluster call
re-presents that token. (OIDC and kubeconfig-upload are later ways to obtain the
token; the authz model is unchanged.)

## Architecture

Two independent runtime services:

- **Backend** (`cmd/dotvirt`, Go): serves a JSON API under `/api`, a per-user
  WebSocket (`/api/inventory/stream`) that pushes each caller's own live inventory,
  and a VNC console WebSocket (`/api/vms/{ns}/{name}/vnc`) bridged to KubeVirt's VNC
  subresource (dialed with the user's token). Read sources — git (per-project
  repos), cluster (live VM/VMI state), ArgoCD (drift). Write target — per-project
  git branches. **Never applies to the cluster.**
- **Frontend** (`web/`, SvelteKit + Tailwind): a standalone static SPA.

**Identities in play:** user requests use the user's token; background work
(per-project running-branch export, the cluster-wide change-signal watch, Argo
re-sync) uses dotvirt's own ServiceAccount.

The **operator** (`operator/`, a separate Go module) is the install-time
provisioner — it turns a single `Dotvirt` resource into the whole install (see
*Install*). It deliberately holds the privileged install RBAC and a forge-**admin**
credential the runtime app never touches.

## Install

dotvirt installs from a single **`Dotvirt`** resource: the operator probes the
cluster, generates secrets, renders every resource, and bootstraps the git side.

**Prerequisites.** A cluster with **ArgoCD** and **KubeVirt** (the operator probes
for both and waits if either is absent); **OVN-Kubernetes**, **NMState**, and
**CDI** are optional and unlock networking and image upload. A git forge
(Forgejo/Gitea) — bring your own, or apply `deploy/forgejo.yaml` for a self-hosted
evaluation one.

```sh
# 1. Install the CRD
make -C operator install

# 2. Install namespace + a forge-admin credential (keys: url, username, token)
kubectl create namespace dotvirt
kubectl -n dotvirt create secret generic dotvirt-forge \
  --from-literal=url=https://forgejo.example.com \
  --from-literal=username=dotvirt \
  --from-literal=token=<forge-admin-token>

# 3. Apply a Dotvirt resource (operator/config/samples/… is a ready template)
kubectl apply -f operator/config/samples/dotvirt_v1alpha1_dotvirt.yaml

# 4. Run the operator (prepend ARGS=-dry-run once to validate, persisting nothing)
make -C operator run
```

A minimal `Dotvirt`:

```yaml
apiVersion: dotvirt.io/v1alpha1
kind: Dotvirt
metadata:
  name: dotvirt
  namespace: dotvirt
spec:
  image: registry.desku.be/dotvirt:<tag>
  forge:
    url: https://forgejo.example.com
    platformRepo: https://forgejo.example.com/dotvirt/platform.git
    credentialsSecret: dotvirt-forge
  argocd:
    namespace: openshift-gitops                                    # 'argocd' on community ArgoCD
    controllerServiceAccount: openshift-gitops-argocd-application-controller
  ingress:
    type: auto                                                     # Route on OpenShift, Ingress on k8s
    host: dotvirt.example.com
  metrics:
    url: https://thanos-querier.openshift-monitoring.svc.cluster.local:9091
```

From that one resource the operator provisions: the dotvirt ServiceAccount + its
minimal read-only ClusterRole; the Deployment, Service, and drafts PVC; a **Route**
(OpenShift) or **Ingress** (Kubernetes); the ArgoCD apply-RBAC, the
`dotvirt-tenants` and `dotvirt-platform` AppProjects, the per-project
ApplicationSet, the static platform Application, and Argo repo-credentials;
generated session / plugin / webhook secrets; the platform git repo itself; and an
org-level forge→ArgoCD webhook for instant sync. The raw manifests it renders live
in `deploy/` for reference.

### Onboarding a project (once per tenant)

1. Create the project's git repo (VMs on `main`, an empty `running` branch).
2. Label + annotate the project's namespace(s) (see *How isolation works*):

   ```sh
   oc label    ns <ns> dotvirt.io/project=<project>
   oc annotate ns <ns> dotvirt.io/repo=https://forge/dotvirt/<project>.git
   ```

The ApplicationSet's plugin generator re-polls dotvirt (`requeueAfterSeconds`),
reads the labeled namespaces, and provisions the per-project Argo Application within
a minute — no manual ApplicationSet edit, and dotvirt still never creates the
Application itself.

> The operator runs against the current kubecontext with `make -C operator run`
> (dry-run validated). Packaging it for in-cluster deployment — the `config/default`
> kustomize overlay behind `make deploy`, a published operator image, and an
> OLM/OperatorHub bundle — is not yet in the tree.

## Develop

```sh
# backend on :8080 (cluster + argo reads; needs a kubeconfig + Forgejo creds)
go run ./cmd/dotvirt -argo \
  -kubeconfig "$HOME/.kube/config" \
  -ui-origin http://localhost:5173
#   DOTVIRT_FORGE_URL / DOTVIRT_FORGE_TOKEN  → Forgejo endpoint + token
#   DOTVIRT_GIT_USERNAME / DOTVIRT_GIT_TOKEN → git clone/push credential

# frontend on :5173 (proxies /api -> :8080, WebSockets included)
cd web && npm install && npm run dev
```

Open http://localhost:5173 and sign in with `oc whoami -t`.

## Build

```sh
go build -o dotvirt ./cmd/dotvirt   # backend binary
cd web && npm run build             # static SPA -> web/build
```

The container image is multi-stage (the SvelteKit SPA is built static and the Go
binary serves it + `/api` at the same origin):

```sh
podman build -f Containerfile -t <registry>/dotvirt:tag .
make -C operator docker-build       # operator image (built from the repo-root context)
```

`go.mod` pins Go 1.26.4; on an older toolchain, build with `GOTOOLCHAIN=auto` so Go
fetches the pinned version.

## Configuration reference (backend flags / env)

The operator sets these from the `Dotvirt` spec; the table below is the underlying
backend reference (and what the `deploy/` manifests wire by hand).

| Flag | Env | Default | Purpose |
|------|-----|---------|---------|
| `-addr` | `DOTVIRT_ADDR` | `:8080` | HTTP listen address |
| `-ui-origin` | `DOTVIRT_UI_ORIGIN` | `http://localhost:5173` | CORS origin for the frontend (empty = same-origin / disabled) |
| `-kubeconfig` | `KUBECONFIG` | in-cluster | cluster access; empty uses the in-cluster SA |
| `-session-secret` | `DOTVIRT_SESSION_SECRET` | random | HMAC key signing the session cookie (set it so sessions survive restarts / span replicas) |
| `-project-label` | `DOTVIRT_PROJECT_LABEL` | `dotvirt.io/project` | namespace label whose value names the project |
| `-repo-annotation` | `DOTVIRT_REPO_ANNOTATION` | `dotvirt.io/repo` | namespace annotation holding the project's git repo URL |
| `-platform-repo` | `DOTVIRT_PLATFORM_REPO` | — | platform-tier repo for cluster-scoped + tenancy manifests (CUDN/NNCP/Namespace); routed by kind + SSAR-gated. Empty disables those creates |
| `-base-branch` | `DOTVIRT_BASE_BRANCH` | `main` | branch the inventory reads + PRs target |
| `-proposed-branch` | `DOTVIRT_PROPOSED_BRANCH` | `dotvirt/proposed` | working branch holding a draft changeset |
| `-running-branch` | `DOTVIRT_RUNNING_BRANCH` | `running` | per-project branch reflecting live cluster state (dotvirt-owned) |
| `-draft-dir` | `DOTVIRT_DRAFT_DIR` | `./.dotvirt-drafts` | root for persisted drafts (`<dir>/<user>/<project>.json`) |
| `-git-username` / `-git-token` | `DOTVIRT_GIT_USERNAME` / `DOTVIRT_GIT_TOKEN` | — | the one credential used to clone/push every project repo |
| `-forge-url` / `-forge-token` | `DOTVIRT_FORGE_URL` / `DOTVIRT_FORGE_TOKEN` | — | Forgejo endpoint + token for PR creation (owner/repo are derived per project from the repo URL) |
| `-push` | `DOTVIRT_PUSH` | `true` | push commits to the remote (disable for local/offline testing) |
| `-argo` | `DOTVIRT_ARGO` | `false` | enable ArgoCD drift reads + re-sync |
| `-export-interval` | — | `30s` | how often the SA exports live state to each project's running branch |
| `-git-poll-interval` | — | `10s` | how often to poll git for branch changes (git has no watch) |
| `-insecure-tls` | `DOTVIRT_INSECURE_TLS` | `false` | skip TLS verification for git + forge (dev; e.g. a self-signed Route) |
| `-metrics-url` | `DOTVIRT_METRICS_URL` | — | Prometheus/Thanos query API for the Performance tab (empty disables it) |
| `-metrics-ca` | `DOTVIRT_METRICS_CA` | — | PEM CA bundle to trust for `-metrics-url` (e.g. the mounted service-CA) instead of `-insecure-tls` |
| `-webhook-secret` | `DOTVIRT_WEBHOOK_SECRET` | — | HMAC secret for the Forgejo webhook endpoint (empty disables it) |
| `-public-url` | `DOTVIRT_PUBLIC_URL` | — | dotvirt's externally reachable base URL, for webhook auto-registration (empty disables) |
| `-appset-plugin-token` | `DOTVIRT_APPSET_PLUGIN_TOKEN` | — | shared bearer for the ArgoCD ApplicationSet plugin endpoint (empty disables it) |
| `-static-dir` | `DOTVIRT_STATIC_DIR` | — | built SPA dir to serve at the same origin (empty = dev: SPA on Vite) |

## More

- [`ROADMAP.md`](ROADMAP.md) — shipped features, what's next, and explicit non-goals.
- [`operator/README.md`](operator/README.md) — the install operator's design.
