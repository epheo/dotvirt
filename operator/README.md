# dotvirt installer operator

Provisions a full dotvirt install from a single `Dotvirt` resource — the production
installer that replaces today's manual repo creation + `oc apply`. It is the
**install-time provisioner**; dotvirt's runtime still owns nothing (rides user RBAC,
writes only git). The two identities are deliberately distinct: the operator holds
the privileged install RBAC + a forge-**admin** credential; the app keeps its narrow
clone/push token.

## Why a separate Go module (same repo)

The operator lives in the dotvirt monorepo (shared `pkg/forge`, lockstep versioning,
atomic binary↔render PRs, one CI) but is its **own** Go module so controller-runtime's
large `k8s.io/*` dependency tree doesn't constrain the app binary's KubeVirt/Argo
client versions. The root `go.work` ties them for tooling; the `replace` in `go.mod`
lets the operator also build standalone (its container image).

## Platform-agnostic, OpenShift as a profile

The controller detects the distribution (`internal/platform`) and renders
accordingly — **Route** on OpenShift, **Ingress** on vanilla Kubernetes — and picks
the ArgoCD namespace/SA defaults (`openshift-gitops` vs `argocd`). It installs
either as **plain manifests / Helm** (any cluster, no OLM) or as an **OLM bundle**
(OperatorHub, OpenShift) — same binary, two packagings.

## Layout

```
api/v1alpha1/      Dotvirt CRD types (spec/status + conditions)
internal/platform/ OpenShift-vs-Kubernetes detection
internal/controller/ phased reconcile (deps → render → forge bootstrap)
cmd/               manager entrypoint (leader election, health, metrics)
config/            generated CRD + RBAC (make manifests) + samples
```

## Develop

```sh
make generate     # DeepCopy methods (required to compile)
make manifests    # CRD + RBAC into config/
make build        # build the manager
make run          # run against the current kubecontext
make docker-build # image (built from the repo root context)
```

## What it provisions

From one `Dotvirt` resource the controller drives the install in order, recording a
status condition per step (so `kubectl get dotvirt` / `describe` explains a stuck
install):

- **Dependencies** — probes for ArgoCD and KubeVirt (hard prerequisites it never
  installs; it waits and reports if either is absent) and for OVN-K, NMState, and
  CDI (soft — noted, install proceeds).
- **Secrets** — generates the session key, the ApplicationSet-plugin token, and the
  webhook secrets once, so they survive restarts and replicas.
- **Workload** — the ServiceAccount, drafts PVC, Service, and Deployment, plus a
  **Route** (OpenShift) or **Ingress** (vanilla Kubernetes), owner-referenced to the
  CR for automatic cleanup.
- **GitOps wiring** — the dotvirt read-RBAC, the shared-controller apply role, the
  authoring-signal role, the `dotvirt-tenants` / `dotvirt-platform` AppProjects, the
  per-project ApplicationSet, the static platform Application, and the Argo
  repo-credentials. These are cluster-scoped, so a finalizer reclaims them on delete.
- **Forge** — bootstraps the platform git repo (`forge.EnsureRepo`, the imperative
  step a declarative installer can't do) and registers one org-level forge→ArgoCD
  webhook for instant sync.

`-dry-run` server-side-applies every rendered resource with `dryRun=All`: the API
server validates schema, admission, and RBAC, and nothing is persisted — a spec
check against a real cluster.

## Packaging

- **`make run`** — run the controller against the current kubecontext (prepend
  `ARGS=-dry-run` to validate the render against a real cluster, persisting nothing).
- **`make docker-build`** — build the operator image (from the repo-root context).
- **`make deploy`** — install the operator in-cluster: `config/default` (CRD + RBAC +
  the manager Deployment) applied with kustomize. Set the image via the `images:`
  block in `config/default/kustomization.yaml`. Distribution-agnostic — the same tree
  is Helm-able.
- **`make bundle`** — generate the OLM bundle for OperatorHub (needs `operator-sdk`
  on PATH; merges the CSV base in `config/manifests/bases/` with the generated CRD +
  RBAC + Deployment). Building/pushing the bundle image to a catalog is a release step.

## Get Forgejo creds

```
oc get secret -n dotvirt dotvirt-forgejo-admin -ojson |jq -r .data.password |base64 -d
```

user: dotvirt-bot

