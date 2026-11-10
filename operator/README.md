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
client versions. `pkg/forge` is a stdlib-only module of its own, so the operator
shares it without requiring the app module. The root `go.work` ties all three for
tooling; the `replace` in `go.mod` lets the operator also build standalone (its
container image).

## Platform-agnostic, OpenShift as a profile

The controller detects the distribution (`internal/platform`) and renders
accordingly: a **Route** on OpenShift, an **Ingress** on vanilla Kubernetes, an
**HTTPRoute** when `spec.ingress.gateway` names a Gateway (`spec.ingress.type` pins
one; `auto` takes a named Gateway, else follows the platform). It picks the ArgoCD
namespace/SA defaults the same way (`openshift-gitops` vs `argocd`). It installs
either as **plain manifests** (any cluster, no OLM: `make deploy`) or as an **OLM
bundle** (OperatorHub, OpenShift): same binary, two packagings.

## Layout

```
api/v1alpha1/        Dotvirt CRD types (spec/status + conditions)
internal/platform/   OpenShift-vs-Kubernetes detection
internal/deps/       discovery probe for the ArgoCD prerequisite and the soft features
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

1. **Dependencies** (`DependenciesReady`): probes for ArgoCD, a hard prerequisite
   it never installs (it waits and reports if it is absent), and for KubeVirt,
   OVN-K, NMState and CDI (soft: noted, the install proceeds).
2. **Forge** (`ForgeReady`): converges the trust anchors (below) first, then, with
   `spec.forge.managed`, stands up the eval-grade Forgejo (single pod, SQLite, a PVC
   that survives uninstall), resolves its external URL (an explicit `spec.forge.url`,
   or the host the router assigns to a hostless Route) and mints its scoped bot
   token. A bring-your-own forge is only observed: `BringYourOwn` when its
   `spec.forge.credentialsSecret` exists, `CredentialsSecretMissing` naming it when
   not (the dotvirt pod cannot start without it). No forge at all records
   `NotConfigured` (push-only).
3. **Secrets** (`SecretsReady`): generates the session key, the ApplicationSet-plugin
   token, the webhook secrets and, with SSO, the OAuth client secret. Create-once:
   they survive restarts and are never regenerated.
4. **Workload** (`WorkloadReady`): the ServiceAccount, drafts PVC, Service, Route,
   Ingress or HTTPRoute, NetworkPolicy (below) and the Deployment, owner-referenced to the CR for
   automatic cleanup. `True` only once the dotvirt pod is ready; `Progressing` until
   then, without holding back the later phases. Single replica, see below.
5. **GitOps wiring** (`ArgoReady`): mirrors the plugin token into the ArgoCD
   namespace and trusts the managed forge's Route there (below), then applies the
   bindings of the three static operand ClusterRoles (shipped in
   `config/rbac/operand_roles.yaml`; the operator only `bind`s them, never authors
   roles), one AppProject per tenant project plus `dotvirt-platform`, the per-project
   ApplicationSet, the static platform Application and the Argo repo-credentials.
   These live outside the CR's namespace, so a finalizer reclaims them on delete.
   A tenant AppProject (`dotvirt-p-<project>`) is derived from the namespaces
   labeled `dotvirt.io/project=<project>`: those namespaces are its only
   destinations and the `dotvirt.io/repo` they name its only source, so a manifest
   merged into one tenant's repo cannot land in another tenant's namespace.
   `dotvirt.io/path` beside it scopes the project to a folder of that repo, for a
   repo that holds something else too. It only locates content: Argo and the forge
   grant by repo, so a repo is one project's, and two projects naming one are refused.
   Platform space (`openshift-*`, `kube-*`, the dotvirt, operator and ArgoCD
   namespaces) is never a destination, and a repo that does not resolve to one URL
   admits no source. The rules live in `pkg/tenancy`, shared with the app's plugin
   generator. A labeled namespace appearing, leaving or changing project or repo
   re-runs the pass.
   `ArgoReady` turns `False` (`Degraded`) while a project is not served as labeled:
   a project whose AppProject name is invalid or held by something else is kept
   out of the ApplicationSet, since Argo would place its Application under that
   AppProject's permissions; a labeled namespace in platform space; or an error the
   ApplicationSet itself reports.
   Each AppProject names the ServiceAccount its Applications sync as once sync
   impersonation is on (below): `<argoNS>/dotvirt-p-<project>` for a tenant,
   `<argoNS>/dotvirt-platform-sync` for the platform tier. Names only: Argo
   impersonates by username and RBAC binds by name, so no object is created.
6. **Sync impersonation** (`SyncImpersonationReady`): on unless opted out, see
   [Sync impersonation](#sync-impersonation). Best-effort: never a halt,
   never `Available`.
7. **Platform repo** (`ForgeRepoReady`): ensures the platform git repo exists
   (`forge.EnsureRepo`, the imperative step a declarative installer can't do) and,
   for a managed forge, its owner org. Best-effort: a failure is recorded and the
   pass goes on. The condition is absent when no platform repo is configured.
8. **Argo webhook** (`ArgoWebhook`): registers one org-level forge-to-ArgoCD webhook
   for instant sync; `Unknown` when no ArgoCD URL resolves (Argo falls back to its
   poll).
9. **dotvirt webhook** (`DotvirtWebhook`): the forge-to-dotvirt hook is registered by
   the app itself; the operator only observes it.

`Available` is `True` only after a completed pass with the dotvirt pod ready. A
phase that fails or blocks sets it `False` with that phase's reason; the
best-effort phases (sync impersonation, platform repo, both webhooks) never do,
since the operand serves without them.
`status.phase` is `Provisioning`, `BlockedOnDependencies` or `Ready`.

A completed pass requeues after 10 minutes, so drift on the kinds the operator does
not watch (Services, AppProjects, bindings) heals without a CR edit; while the
platform repo is missing, after 1 minute instead. Edits to the CR trigger a pass
only when they change its spec (or delete it), never a status write.

### Prune

After a completed pass whose render changed, or at least every 5 minutes, the
operator deletes the objects carrying this install's instance labels that the pass
did not render: a Route left by a switch to Ingress,
the platform tier after `spec.forge.platformRepo` is cleared, everything in a former
ArgoCD namespace, a managed forge switched off. A tenant AppProject whose project
is gone, and the shared `dotvirt-tenants` AppProject of earlier versions, are kept
while an Application still references them (the former admitting nothing), then
pruned.
Secrets and ConfigMaps are swept only in the namespaces the install writes (the
ArgoCD namespace, and a former one still holding its argoproj objects), so a
labelled copy an admin made elsewhere survives.
Never pruned: anything without the labels, and Secrets, PersistentVolumeClaims and
the CA ConfigMaps in the install namespace (generated keys, the minted forge token
and the forge's git data are state, not render). A pass that halts prunes nothing.
Deleting the CR runs the same prune with nothing rendered, after taking sync
impersonation off the ArgoCD instance if the operator set it there (below).

The operator's delete grant names each object it renders (`rbac_markers.go`), so it
cannot delete any other Secret, binding or Application in the cluster. AppProjects
are the exception: tenant ones are named after their projects, so the grant covers
any AppProject and prune's label check is what keeps it to this install's. An object
only an older version rendered under another name is refused, logged and left for
an admin to delete; it never blocks prune or deletion.

The labels are `dotvirt.io/instance` (the CR name) and
`dotvirt.io/instance-namespace` (its namespace). Objects from versions that set only
the first are still recognized as this install's; the next apply relabels them.

`-dry-run` server-side-applies every rendered resource with `dryRun=All`: the API
server validates schema, admission, and RBAC, and nothing is persisted, a spec check
against a real cluster. Phases whose work dry-run cannot model (the secrets, the
forge bootstrap, the workload rollout, both webhooks, the platform repo) record a
`DryRun` condition instead, and so does `Available`: nothing serves.

## Serving through a Gateway

With `spec.ingress.gateway` the operator attaches HTTPRoutes for the UI and a managed
forge to a Gateway someone else runs. Two things stay with whoever runs it:

- The listener must allow routes from the Dotvirt's namespace and no other. dotvirt's
  HTTPRoute matches every path, and the longest path wins across routes on one
  hostname: a route another namespace attaches there with `/api/login` takes the
  login and the tokens. Gateway API has no hostname ownership check, unlike Routes.
- The operator adds no trust for the Gateway's certificate. A managed forge behind a
  private CA fails x509 at ArgoCD: use a publicly trusted certificate, or keep
  `spec.forge.url` on the in-cluster Service.

## Publishing

With `spec.publishing` a platform admin may turn publishing on for a project. The
platform repo then gains:

- one Gateway `publish` of `gatewayClassName`, in a project namespace, whose
  listeners take only `*.<project>.<domain>` and only HTTPRoutes from namespaces
  labeled with the project (`dotvirt.io/project`), a label no tenant role may change;
- with `clusterIssuer`, a cert-manager Certificate for that wildcard and an HTTPS
  listener serving it;
- `<namespace>-routes` in each namespace the Gateway reaches, binding
  `dotvirt-tenant-routes` (HTTPRoute writes) to the subjects of that namespace's
  admin grant and the project's sync account. It is left out where the cluster's
  `admin` role already writes HTTPRoutes (OpenShift).

The Gateway reaches the project namespaces on its network: it goes in the first
namespace of the project's largest network, and stays where git holds it. A
namespace with a VM Network of its own is not reached; a customer who wants a
gateway there runs their own, which is outside dotvirt for now (Gateway stays a
platform kind: a tenant listener could claim any hostname). A namespace that joins
the project later gets its grant from "Update publishing" on the project card.

Settings:

- `domain`: the zone each project gets a subdomain of.
- `gatewayClassName`: the class of each project's Gateway.
- `clusterIssuer` (optional): a ClusterIssuer able to issue each project's
  wildcard, so DNS-01. Without it the Gateways serve HTTP only.

Publishing is off until `domain` and `gatewayClassName` are both set.

What stays with the platform:

- DNS and addresses. Point `*.<domain>` at your edge, which routes each project's
  hostnames to its Gateway; a project with its own address gets its DNS pointed
  straight at it. Giving a project's Gateway an address is a platform-repo change a
  platform admin stages, never the tenant's. dotvirt writes no DNS records.
- The Gateway's Service is the GatewayClass's default; whether a LoadBalancer
  Service is reachable on a VM Network is not verified yet.
- A project on a VM Network is not offered publishing yet. Its gateway reaches
  portail's controller only through the apiserver, where one project could delay
  the others' route changes. It waits on a portail release that isolates Gateways.
- Shared Gateways. HTTPRoute write lets a namespace attach routes to any Gateway
  whose listeners admit its namespace. A Gateway shared across projects must keep
  `allowedRoutes` at `Same` or a selector of platform namespaces, or it serves
  tenant hostnames. That is Gateway API, but this grant turns it on.
- cert-manager. Where tenants hold Certificate writes (cert-manager aggregates
  them into `admin` by default), they can edit the staged Certificate and request
  any name the issuer can prove. Keep them from tenants on clusters that publish.

## Network policies

On OpenShift the dotvirt and managed Forgejo pods get an ingress NetworkPolicy
admitting their HTTP port only from:

- the install namespace (the managed forge delivers webhooks to dotvirt's Service);
- the ArgoCD namespace (the ApplicationSet plugin generator; repo-server on an
  in-cluster forge URL);
- the operator namespace (the forge bootstrap calls Forgejo's Service);
- a Gateway named in another namespace: that namespace, where its data plane runs.
  A host-network data plane needs its own policy beside this one.
- the router: the `policy-group.network.openshift.io/ingress` and `/host-network`
  namespace groups, and `openshift-ingress` by name for MicroShift. Browsers, a
  bring-your-own forge's webhooks, ArgoCD clones of a Route-exposed forge and the
  app's own forge API calls all arrive this way.

Egress is not restricted. Vanilla Kubernetes gets no policy: the ingress
controller's namespace is not knowable there, and a wrong guess cuts the console
off.

## Single replica

dotvirt runs one replica by design, not by default. Drafts live on a
ReadWriteOnce PVC, and git writes are serialized by an in-process lock; a second
replica would split drafts and race pushes. The Deployment therefore uses the
`Recreate` strategy, and scaling it is unsupported.

## The ArgoCD controller's rights

The `dotvirt-argocd-apply` binding grants the shared ArgoCD application
controller ServiceAccount apply rights on everything dotvirt's PRs emit, including
`MachineConfig`, `Subscription`/`OperatorGroup` and `KubeDescheduler` (the DRS
platform objects) and RoleBindings. It is cluster-wide: every Application that
controller reconciles gains them, not only dotvirt's. The tenant AppProjects
keep each tenant repo to namespaced workloads in its own namespaces;
Applications in other AppProjects on a shared ArgoCD are bounded only by their
own AppProject.
Give dotvirt its own ArgoCD instance where other teams' Applications must not
hold these rights.

The binding also names the platform tier's sync account, which applies the platform
repo once sync impersonation is on. Its own `dotvirt-platform-sync` binding adds the
Namespaces that repo declares and `bind` on `admin` and `dotvirt-tenant-egress`, the
roles project grants name. The controller stays a subject while impersonation is
optional, since it applies every tier while it is off.

## Sync impersonation

Without it, Argo applies every repo with its controller's own account.
A project's AppProject keeps its repo to its own namespaces and kinds, but inside
them Argo writes with the controller's rights, not the project's.
So the operator turns on Argo CD's sync impersonation unless
`spec.argocd.syncImpersonation.enabled: false` opts out, and each Application syncs
as its tier's ServiceAccount:

- `dotvirt-p-<project>` holds what the platform repo grants the project in each
  of its namespaces, beside the project's people: `<ns>-admins` (the `admin` role,
  or whatever narrower role it names) and, for an `admin` grant, `<ns>-egress`
  (`dotvirt-tenant-egress`, EgressFirewall writes, which `admin` lacks), both to the
  same subjects. A project's repo can then apply only what its people may: no
  NetworkAttachmentDefinition, nothing in another project's namespace, and a
  DataVolume clones only out of namespaces where its people may create
  `datavolumes/source` (an `edit` or `admin` grant). The people's EgressFirewall
  right is also what the UI checks before staging one.
- `dotvirt-platform-sync` holds `dotvirt-argocd-apply` and `dotvirt-platform-sync`.

Both are names only: Argo impersonates by username and RBAC binds by name, so the
platform repo's grants are all they are.

dotvirt stages the grants with every project, adoption and namespace it creates. A
namespace joining a project is granted admin to the people the project's other
`admin` grants name, never to its account alone, and never to someone holding a
narrower role elsewhere. A grant it rewrites keeps every person it names
but drops any other project's account. Releasing a project takes its account out of
its grants, so a later project of that name inherits nothing.

The setting (`application.sync.impersonation.enabled` in `argocd-cm`) is
instance-wide, and Argo enforces it: an Application whose AppProject maps no
ServiceAccount for it stops syncing. So the operator turns it on only when:

- the ArgoCD controller may impersonate ServiceAccounts in its namespace;
- each project's account holds a grant in each of its namespaces, and may write
  every tenant kind its repo declares there (as its Application reports managing
  them): a project whose grant is narrower than `admin` is ready while its repo
  declares nothing beyond it, and an EgressFirewall needs an `admin` grant, which
  `sync-access` pairs with `<ns>-egress`;
- no project's account may create VirtualMachines or clone disks
  (`datavolumes/source`) cluster-wide;
- no project namespace lets every ServiceAccount of the ArgoCD namespace create
  VirtualMachines, and no grant lets them all write a tenant kind or clone disks
  cluster-wide: such a grant would let a narrower project's repo write where its
  people may not. The condition names the binding, from the authorizer's reason;
- the platform account may create Namespaces, ClusterUserDefinedNetworks and
  RoleBindings, and bind every role the platform repo's RoleBindings bind (as the
  platform Application reports managing them). A role it may not bind holds the
  flip back with its name: grant the account `bind` on it. An account holding every
  right of such a role would be allowed by the apiserver, but reads as missing here.
  Once on, a RoleBinding the repo declares that is not on the cluster is reported,
  the trace a refused one leaves;
- no Application still uses the shared `dotvirt-tenants` AppProject;
- every other Application on the instance (in its namespace, or its
  `sourceNamespaces` or `application.namespaces`, globs and `/regex/` alike) has an
  AppProject whose `destinationServiceAccounts` map its destination, unless
  `spec.argocd.syncImpersonation.ignoreForeignApplications: true` accepts that the
  others stop syncing. An instance owner keeps an AppProject's old behaviour by
  mapping it to the controller's own account, e.g.
  `{server: "*", namespace: "*", defaultServiceAccount: "<argoNS>:<controller SA>"}`.
  An Application naming its cluster by a name other than `in-cluster` counts like
  any other when its AppProject maps nothing for its namespace, or maps it on every
  server (`*`). When the only entries for its namespace name specific servers, it
  is said, not counted: its server is in a cluster Secret, which the operator does
  not read. With `enabled` unset it holds the flip back, since nobody has read that;
  `enabled: true` accepts it.

It then server-side-applies that one key into the ArgoCD resource's
`spec.extraConfig` (`openshift-gitops` on OpenShift, `argocd` elsewhere), owning
nothing else there and never forcing: a key another manager holds is reported as
`Conflict`, never taken. The GitOps operator's own reconcile keeps fields it does
not manage, and the argocd-operator copies `extraConfig` into `argocd-cm`.
`SyncImpersonationReady` says `Pending` until `argocd-cm` carries it, then
`Enabled`. Without an ArgoCD resource it says `ManualStep`: set the key in
`argocd-cm` by hand.

`status.syncImpersonationInstance` records the resource the operator set it on. The
operator takes the key off there when `enabled` is set to false, when `spec.argocd`
moves to another instance, and when the CR is deleted, before prune removes
dotvirt's AppProjects. A cleanup that fails keeps the CR, and `Available` says why
(`CleanupFailed`).

Once on, it stays on while asked: a project whose grants go missing, or an
Application newly synced with no account mapped, is reported (`Degraded` names
them) instead of falling back to the controller's rights.

While off, `SyncImpersonationReady` is `Unknown`, or `False` with `CloneAnywhere`
when the controller's account may create `datavolumes/source` cluster-wide: a
DataVolume in any tenant repo could then clone a disk out of any namespace.
`Pending`, `NotReady` and `ManualStep` keep that warning, since the controller still
syncs until `argocd-cm` changes.

Readiness does not read clone sources: a DataVolume, or a VirtualMachine's
`dataVolumeTemplates`, cloning out of a namespace where the project's people may not
create `datavolumes/source` syncs while off and fails once on, as CDI then
authorizes it as the project's account. Check them before turning it on. A
golden-image namespace that grants `datavolumes/source` to every authenticated user
(`openshift-virtualization-os-images` does) keeps working.

Checks run as SubjectAccessReviews: two per project namespace and one per kind its
repo declares there, two per project, eight for the controller and the ArgoCD
namespace, five for the platform tier and one per further role its RoleBindings
bind, at most eight in flight. They come with a list of the Applications and
AppProjects and a read of each platform RoleBinding. A verdict is reused until a
project, namespace or the CR's spec changes, or for 5 minutes, so a merged grant is
seen within that, or at once after a spec edit. A pass held back on something a
person fixes comes back every 5 minutes; one that set the key, each minute until
`argocd-cm` carries it. While off, one review per 5 minutes.

### Upgrading an install that had it off

Nothing changes until every condition above holds: until then `SyncImpersonationReady`
says `NotReady` and lists what is missing, and the controller keeps syncing. Once they
hold, an install that never set `enabled` flips by itself. Before upgrading:

- Check clone sources, the one thing the conditions cannot read (see above): one that
  syncs today and fails once on will not be held back.
- If you opted out, read the CR: `enabled: false` set when the CR was created was
  dropped when the operator added its finalizer, and reads as unset. Set it again.

1. Upgrade the operator and the app image together: the app stages the sync grants
   only once the operator hands it the ArgoCD namespace.
2. For each project made before, stage its grants: `POST
   /api/projects/<project>/sync-access` as a platform admin, then merge the
   platform PR. Each namespace's `<ns>-admins` keeps its role and people and gains
   the project's account; for an `admin` grant, `<ns>-egress` grants those same
   subjects EgressFirewall writes, so the people gain them too. A grant file holding
   other objects is refused: edit it in git.
3. Move Applications off the shared `dotvirt-tenants` AppProject (the upgrade does
   this), and map an account for every other Application on the instance.
4. Read `SyncImpersonationReady`: `NotReady` lists what is missing, `Pending` means
   set, `Enabled` means on. Without an ArgoCD resource, as with community Argo CD
   installed from its manifests, it stops at `ManualStep` until the key is set by hand.

To stay off, or roll back, set `enabled: false`.

### Real-loop scenario (not enabled in the nightly)

On the MicroShift real loop (`hack/e2e-microshift.sh`: community Argo CD, whose
controller holds every right, and no ArgoCD resource):

1. After the project create and merge, expect `SyncImpersonationReady`
   `False`/`ManualStep`, the project's grants having come with it.
2. Set `application.sync.impersonation.enabled: "true"` in `argocd/argocd-cm`;
   expect `Enabled`.
3. Run `hack/e2e-roundtrip.sh`: the VM syncs.
4. Commit a DataVolume cloning a PVC from a namespace outside the project: CDI
   refuses it, since it authorizes `datavolumes/source` as the project's account.
5. Delete the project's `<ns>-admins` RoleBinding: the next VM sync fails and
   `SyncImpersonationReady` turns `Degraded` naming the project (within 10 minutes,
   or at once after a CR spec edit).

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

## Upgrading

- The CSV supports only the `AllNamespaces` install mode: one install per cluster,
  watched cluster-wide.
  - An OperatorGroup with `targetNamespaces` (OwnNamespace or SingleNamespace) fails
    the upgrade with `UnsupportedOperatorGroup`.
  - To migrate, delete that OperatorGroup and recreate it with `spec: {}`, then let
    OLM retry the install.
  - The `Dotvirt` resource and the workloads are untouched by the swap.
- The first reconcile after the upgrade restarts the dotvirt and managed Forgejo
  pods once (a new instance label on the pods, new Forgejo settings).
- Tenant Applications move from the shared `dotvirt-tenants` AppProject to one per
  project, each aimed at one of the project's own namespaces instead of `default`.
  Before upgrading:
  - Bump or unpin `spec.image` together with the operator. An older app image does
    not hand the ApplicationSet the destination namespace, so it stops updating
    every tenant Application (nothing is deleted) and `ArgoReady` reports the
    ApplicationSet's error until the image moves.
  - Give every tenant manifest a `metadata.namespace`. Argo applies one without it
    to the Application's destination namespace, no longer to `default`, re-creating
    its object there and pruning the old one, disks included. The inventory lists
    such documents per project (`unnamespaced`).
  - Move objects a tenant repo declares in a namespace outside its project. The
    project's AppProject refuses them, which fails that Application's whole sync.
    The inventory lists them per project (`outOfProject`).
  - Move every NetworkAttachmentDefinition out of tenant repos into the platform
    repo. Tenant AppProjects no longer admit NADs: a project's admin role may only
    read them, while Argo applies with its own rights, so a tenant NAD could join
    another project's VLAN or reach the underlay. A tenant repo still holding one
    fails to sync. The inventory lists such objects per project (`platformKinds`).

## Managed Forgejo credentials

Registration is disabled and viewing requires sign-in: the Route never serves the
manifests anonymously. The app and ArgoCD clone with the bot token; the pod mounts
no ServiceAccount token.

The bootstrap admin is `dotvirt-bot`. The operator reports the command that reveals
its password in `status.forgeAdminHint`; the value itself stays in the Secret:

```console
$ oc -n <install ns> get dotvirt dotvirt -o jsonpath='{.status.forgeAdminHint}'
# copy-paste and run the printed `oc extract` command
```

## Who opens pull requests

By default dotvirt opens each proposal's pull request with its forge account.
Then the forge sees that account as the poster, and the requester may approve
their own change.

`spec.forge.pullRequests: Users` leaves the pull request to the requester:

- dotvirt pushes the branch and answers with the forge's compare link;
- the requester opens the PR there, as themselves: the forge refuses their own approval;
- a PR opened on the head dotvirt pushed is adopted, from the forge webhook or the lane's refresh: its changes leave the draft, and later proposes extend it;
- one merged before dotvirt saw it open settles the draft the same way;
- dotvirt never opens, reopens or edits a PR.

Protect `dotvirt/proposed/**` in the forge so only dotvirt's account pushes there:
otherwise anyone who may push could put content into a PR and approve it.

The guarantee holds only while the requester is the PR's poster.
Anyone who may read the repo can open a PR from another user's pushed branch.
The requester could then approve their own change.
dotvirt adopts that PR all the same, as it matches the branch and head.
So require approvals from a reviewer team, or another rule the forge enforces, beyond "not the poster".
