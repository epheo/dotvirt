# dotvirt installer operator

Provisions a full dotvirt install from a single `Dotvirt` resource. It is the
**install-time provisioner**; dotvirt's runtime still owns nothing (rides user RBAC,
writes only git). The two identities are deliberately distinct: the operator holds
the privileged install RBAC + a forge-**admin** credential; the app keeps its narrow
clone/push token.

## Why a separate Go module (same repo)

The operator lives in the dotvirt monorepo (shared `pkg/forge`, lockstep versioning,
atomic binary + render PRs, one CI) but is its **own** Go module so controller-runtime's
large `k8s.io/*` dependency tree doesn't constrain the app binary's KubeVirt/Argo
client versions. The root `go.work` ties them for tooling; the `replace` in `go.mod`
lets the operator also build standalone (its container image).

## Platform-agnostic, OpenShift as a profile

The controller detects the distribution (`internal/platform`) and renders
accordingly: a **Route** on OpenShift, an **Ingress** on vanilla Kubernetes
(`spec.ingress.type` pins one; `auto` follows the platform). It picks the ArgoCD
namespace/SA defaults the same way (`openshift-gitops` vs `argocd`). It installs
either as **plain manifests** (any cluster, no OLM: `make deploy`) or as an **OLM
bundle** (OperatorHub, OpenShift): same binary, two packagings.

## Layout

```
api/v1alpha1/        Dotvirt CRD types (spec/status + conditions)
internal/platform/   OpenShift-vs-Kubernetes detection
internal/deps/       discovery probe for the ArgoCD/KubeVirt prerequisites
internal/controller/ the phased reconcile (one status condition per phase)
internal/install/    typed renderers for every applied resource
cmd/                 manager entrypoint (leader election, health, metrics)
config/              generated CRD + RBAC (make manifests) + samples
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

From one `Dotvirt` resource the controller runs the phases below in order. Each
owns one status condition; a phase that fails or waits records why there, so
`kubectl get dotvirt` / `describe` explains a stuck install.

1. **Dependencies** (`DependenciesReady`): probes for ArgoCD and KubeVirt, hard
   prerequisites it never installs (it waits and reports if either is absent), and
   for OVN-K, NMState and CDI (soft: noted, the install proceeds).
2. **Forge** (`ForgeReady`): converges the trust anchors (below) first, then, with
   `spec.forge.managed`, stands up the eval-grade Forgejo (single pod, SQLite, a PVC
   that survives uninstall), resolves its external URL (an explicit `spec.forge.url`,
   or the host the router assigns to a hostless Route), mints its scoped bot token
   and ensures the owner org. A bring-your-own forge is left alone; no forge at all
   records `NotConfigured` (push-only).
3. **Secrets** (`SecretsReady`): generates the session key, the ApplicationSet-plugin
   token, the webhook secrets and, with SSO, the OAuth client secret. Create-once:
   they survive restarts and are never regenerated.
4. **Workload** (`WorkloadReady`): the ServiceAccount, drafts PVC, Service, Route or
   Ingress, and the Deployment, owner-referenced to the CR for automatic cleanup. A
   single replica by design: the drafts PVC is ReadWriteOnce, so the Deployment
   recreates rather than rolls.
5. **GitOps wiring** (`ArgoReady`): mirrors the plugin token into the ArgoCD
   namespace and trusts the managed forge's Route there (below), then applies the
   bindings of the three static operand ClusterRoles (shipped in
   `config/rbac/operand_roles.yaml`; the operator only `bind`s them, never authors
   roles), the `dotvirt-tenants` / `dotvirt-platform` AppProjects, the per-project
   ApplicationSet, the static platform Application and the Argo repo-credentials.
   These live outside the CR's namespace, so a finalizer reclaims them on delete.
6. **Argo webhook** (`ArgoWebhook`): registers one org-level forge-to-ArgoCD webhook
   for instant sync; `Unknown` when no ArgoCD URL resolves (Argo falls back to its
   poll).
7. **dotvirt webhook** (`DotvirtWebhook`): the forge-to-dotvirt hook is registered by
   the app itself; the operator only observes it.
8. **Platform repo** (`ForgeRepoReady`): ensures the platform git repo exists
   (`forge.EnsureRepo`, the imperative step a declarative installer can't do). Last,
   so its retry never delays another phase.

`Available` rolls a completed pass up; `status.phase` is `Provisioning`,
`BlockedOnDependencies` or `Ready`.

`-dry-run` server-side-applies every rendered resource with `dryRun=All`: the API
server validates schema, admission, and RBAC, and nothing is persisted, a spec check
against a real cluster. Phases whose work dry-run cannot model (the secrets, the
forge bootstrap, both webhooks, the platform repo) record a `DryRun` condition instead.

## Trust anchors

A zero-config install verifies every TLS hop instead of switching verification off.
On OpenShift the operator reads the router CA from
`openshift-config-managed/default-ingress-cert` once per pass and:

- copies it into the install namespace as the `dotvirt-ingress-ca` ConfigMap,
  converged every pass because the CA rotates. The app mounts it for its forge and
  OAuth calls, the managed Forgejo for its webhook deliveries. The copy lands before
  either pod starts: a Go process loads its trust roots once, so a copy that arrives
  later stays invisible until a restart. The mounts are optional, so a missing copy
  degrades to the system pool rather than blocking a pod.
- requests the injector-filled `dotvirt-service-ca` ConfigMap, which verifies
  in-cluster serving certs (the sample's Thanos querier).
- merges the managed forge's host with that CA into `argocd-tls-certs-cm` in the
  ArgoCD namespace, so repo-server verifies the forge Route (repo-creds templates
  ignore `insecure`). Only that host's key is applied and owned: other entries are
  untouched, and the key is left behind on uninstall. This one blocks: without it
  every Application wedges in an x509 ComparisonError, so `ArgoReady` names the
  missing CA instead.

This is why the operator's role holds `get` on ConfigMaps (the CA lives in another
namespace) beside the `create`/`patch` every server-side apply needs.

## OpenShift SSO (optional)

dotvirt can offer "Sign in with OpenShift" beside the always-present token login:
the OAuth access token the cluster hands back is a normal bearer token, so it rides
the same TokenReview + per-request pass-through path as a pasted one.

Set `spec.auth.openShiftSSO: true`:

```yaml
spec:
  auth:
    openShiftSSO: true
```

The operator generates the client credential and, once the console host is assigned,
publishes a ready-to-apply command in `status.ssoOAuthClient`: one `oc apply` that
registers the cluster-scoped `OAuthClient` with the right redirect URI. Registering that
cluster-scoped object stays a cluster-admin act the operator deliberately leaves to you
(it holds no `oauthclients` grant); the client secret is read from the generated Secret
at apply time, so it never lands in status:

```console
$ oc -n <install ns> get dotvirt dotvirt -o jsonpath='{.status.ssoOAuthClient}'
# copy-paste and run the printed `oc apply` command
```

The server-side code exchange calls the cluster's oauth Route, which the router CA
signs; the Deployment points `DOTVIRT_OAUTH_CA` at the mounted ingress-CA copy, so
no extra trust configuration is needed.

## Packaging

- **`make run`**: run the controller against the current kubecontext (prepend
  `ARGS=-dry-run` to validate the render against a real cluster, persisting nothing).
- **`make docker-build`**: build the operator image (from the repo-root context).
- **`make deploy`**: install the operator in-cluster: `config/default` (CRD + RBAC +
  the manager Deployment) applied with kustomize. Set the image via the `images:`
  block in `config/default/kustomization.yaml`.
- **`make bundle`**: generate the OLM bundle for OperatorHub (needs `operator-sdk`
  on PATH; merges the CSV base in `config/manifests/bases/` with the generated CRD +
  RBAC + Deployment). Building/pushing the bundle image to a catalog is a release step.

## Managed Forgejo credentials

The bootstrap admin is `dotvirt-bot`. The operator reports the command that reveals
its password in `status.forgeAdminHint`; the value itself stays in the Secret:

```console
$ oc -n <install ns> get dotvirt dotvirt -o jsonpath='{.status.forgeAdminHint}'
# copy-paste and run the printed `oc extract` command
```
