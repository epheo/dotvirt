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

## Install namespace

The operator acts on one Dotvirt only: the one in its install namespace, resolved
on every pass in this order.

1. The namespace a cluster admin names with `OPERATOR_INSTALL_NAMESPACE` on the
   operator's Deployment (or its `-install-namespace` flag).
2. The operator's own namespace (`dotvirt-operator` from the bundled manifests and
   OperatorHub's suggestion), once it holds a Dotvirt.
3. The namespace ClusterRoleBinding `dotvirt` binds the ServiceAccount `dotvirt` in:
   the install an earlier operator provisioned. This keeps an install created
   elsewhere (in `dotvirt`, as the earlier sample did) working across the upgrade.
   Its `InstallNamespaceAdopted` condition names the namespace and suggests the
   override. Only a Dotvirt there created before the binding is adopted, and the
   binding must name one namespace. Deleting the binding by hand drops an adopted
   install to `WrongNamespace` (nothing is deleted): set the override to keep it.

Through OLM, set the override on the Subscription:

  ```yaml
  spec:
    config:
      env:
        - name: OPERATOR_INSTALL_NAMESPACE
          value: dotvirt
  ```

- A Dotvirt in any other namespace gets no finalizer, render or binding. Its
  `Available` condition reads `WrongNamespace` and says where the install lives.
- Why: OLM adds create rights on Dotvirts to every namespace's `admin` and `edit`
  roles. Acting on a Dotvirt in a tenant's namespace would bind the install's
  cluster-wide roles to accounts the tenant controls.
- The override, and the operator's own namespace, must name a namespace only
  cluster admins control. Where users may create projects (OpenShift
  self-provisioning), a user could create a namespace of that name first and own
  the install. Create the namespace before naming it.
- Deleting a Dotvirt outside the install namespace only drops the operator's
  finalizer: nothing is pruned on its say. To uninstall an install that lives
  outside it, name its namespace first, then delete it.
- The operator guard pins the install namespace, so it refuses a move: remove the
  guard, move the install, then install the guard again from the console there (or
  the template, which resolves the namespace in this same order). A console the
  install moved away from refuses to install it.
- Why step 3 is safe: only cluster admins and this operator may write
  ClusterRoleBinding `dotvirt`, and the operator wrote it for the install it acted
  on. Its limit: an operator before this rule acted on the oldest Dotvirt anywhere,
  so a tenant's could have been that install. After upgrading, check the namespace
  the binding names:

  ```sh
  kubectl get clusterrolebinding dotvirt -o jsonpath='{.subjects[?(@.name=="dotvirt")].namespace}'
  ```

  It must be the namespace you installed dotvirt in. If it names a tenant's
  namespace, in this order:

  1. Create the install in the operator's namespace, or name yours with the
     override. The tenant's Dotvirt is then ignored.
  2. Delete the tenant's Dotvirt. Ignored, it prunes nothing; deleted while still
     adopted, it would run its own cleanup.
  3. Delete what its install left: objects labelled
     `dotvirt.io/instance-namespace=<tenant namespace>` in the Argo namespace
     (AppProjects, the ApplicationSet, Secrets), and any binding still naming the
     tenant's accounts.

## What it provisions

From one `Dotvirt` resource the controller runs the phases below in order. Each
owns one status condition; a phase that fails or waits records why there, so
`kubectl get dotvirt` / `describe` explains a stuck install.

1. **Dependencies** (`DependenciesReady`): probes for ArgoCD, a hard prerequisite
   it never installs (it waits and reports if it is absent), and for KubeVirt,
   OVN-K, NMState and CDI (soft: noted, the install proceeds).
2. **Operator guard** (`OperatorGuard`): renders five ValidatingAdmissionPolicies and
   their bindings that bound the operator's own account, pinned to its exact username
   (one SelfSubjectReview at startup), the install namespace, and the ArgoCD namespace
   and controller (`spec.argocd`, else the defaults):
   - `dotvirt-operator-secrets`: Secrets only in those two namespaces, never of type
     `kubernetes.io/service-account-token` nor carrying `kubernetes.io/service-account.name`;
     the console's ServiceAccount never with its token unmounted;
   - `dotvirt-operator-deployments`: only the console (`dotvirt`, as `dotvirt`) and the
     forge (`dotvirt-forgejo`, as `dotvirt-forgejo`), in the install namespace. The
     console's images come from `quay.io/epheo/dotvirt` and the forge's from
     `codeberg.org/forgejo/forgejo` (any tag or digest), or exactly the image an admin
     chose (`spec.image`, the operator Deployment's `RELATED_IMAGE_*`). The console runs
     its image's entrypoint with no init container, no exec probe or hook, and no shared
     process namespace. Its volumes are its own (drafts claim, CA ConfigMaps, forge
     credential Secret, and the branding and metrics CA ConfigMaps an admin chose), each
     mounted whole at its own path: nothing over its binary or the system trust store.
     Its Secret-backed env is pinned by env name, Secret and key: its own Secrets, the
     forge credential and the OIDC client secret an admin chose; a forge credential an
     admin brought (`spec.forge.managed` false) goes only to the URL its Secret names.
     The forge's pods never carry the console's `app` label, read no Secret but their
     admin password, and mount only their own claim, a scratch directory and the router
     CA: no network or host volume. The operator's claims bind to no
     volume, selector or data source of its choosing (the binder's `volumeName` stays as
     it set it). The console's arguments stay free, so a release
     may add flags, except the settings that send users' tokens or its pages elsewhere,
     by flag or env: the metrics endpoints and CA only as rendered from `spec.metrics`,
     TLS verification off only with `spec.forge.insecureTLS`, the OIDC issuer only as
     `spec.auth.oidc.issuerURL`, and never a kubeconfig or
     a directory of pages. It keeps its own token mounted where client-go reads it, with
     no `KUBERNETES_SERVICE_*` or `envFrom`. Nothing is mounted at, above or inside the
     token directory. Every container runs non-root with a RuntimeDefault or
     Localhost seccomp profile, `allowPrivilegeEscalation: false`, all capabilities
     dropped and none added, unprivileged, off the host network, PID and IPC, with no
     host path;
   - `dotvirt-operator-bindings`: only the ClusterRoleBindings and RoleBinding the
     operator renders, each to the ClusterRole of its own name and to its rendered
     subjects only;
   - `dotvirt-operator-install`: the operator's updates to its Dotvirt resource keep
     spec, labels, annotations and owners as they are. It adds and removes its finalizer
     by a patch of the finalizers alone, so once the guard is in, the spec is the
     admin's;
   - `dotvirt-operator-frontdoor`: the console's Service selects only the console's pods
     on its port (ClusterIP, no external name, no unready pods), and its Route, Ingress
     or HTTPRoute leads only to that Service, on the console's host (`spec.ingress.host`,
     else the router's host on the cluster's ingress domain), edge-terminated, with no
     filter and no annotation the operator adds. Where no ingress domain can be read
     (MicroShift), the console's host is the one routers report for its Route. Every
     other exposure the operator writes names its own hosts: none the console's, no
     wildcard over it, no Ingress default backend, no HTTPRoute without hostnames. No
     Service the operator writes leaves the cluster: no node port, load balancer or
     external IP. The forge's own exposures are otherwise free: it holds no user token.

   The Route rules assume the apiserver serving Routes evaluates admission policies,
   and that OpenShift's route API marks a host it generates
   (`openshift.io/host.generated`) on create, which the rules allow. Verify both on
   each platform before relying on them.

   The operator never installs them: it reads them back by name and reports each in
   `status.adminObjects` as `NotInstalled`, `Outdated` (rendered for other pins or
   another version, or changed since) or `Installed`; see [Admin objects](#admin-objects).
   The apiserver evaluates them in-process, only for those kinds' writes, with the
   username check first. Best-effort: never a halt, never `Available`. A new release
   in the same repositories passes as it is, new console flags included; an admin
   changing `spec.image`, `spec.argocd`, `spec.forge.insecureTLS`, `spec.metrics`,
   `spec.ingress.host`, `spec.branding.configMap`, `spec.forge.credentialsSecret`,
   `spec.forge.managed`, `spec.auth.oidc.clientSecret`, the operator's `RELATED_IMAGE_*`
   or the namespace makes them `Outdated`, and until re-installed they refuse the operator's
   writes that need the change.
3. **Forge** (`ForgeReady`): converges the trust anchors (below) first, then, with
   `spec.forge.managed`, stands up the eval-grade Forgejo (single pod, SQLite, a PVC
   that survives uninstall), resolves its external URL (an explicit `spec.forge.url`,
   or the host the router assigns to a hostless Route) and mints its scoped bot
   token. A bring-your-own forge is only observed: `BringYourOwn` when its
   `spec.forge.credentialsSecret` exists, `CredentialsSecretMissing` naming it when
   not (the dotvirt pod cannot start without it). No forge at all records
   `NotConfigured` (push-only).
   A credential under any name but `dotvirt-forge` is a cluster admin's to grant:
   the operator's own role names only its own Secrets. `ForgeCredentialReady` is
   False (`NotGranted`) until the Role and RoleBinding `dotvirt-forge-credential`
   are in the install namespace, granting the operator's account get on that
   Secret (plus create and patch for a managed forge, which writes its minted token
   there). A cluster admin installs them from the console (the
   [Admin objects](#admin-objects) banner's **Install**, owned by the CR so they go
   with it), or with the [Forge credential grant](#forge-credential-grant) commands,
   every value derived from the cluster: the condition names none, since the
   operator writes it. The operator watches that Role and RoleBinding by name, so a
   grant (or its removal), in either order, takes effect at once. A grant changed
   under 5 minutes ago but still refused is rechecked every minute (the apiserver's
   RBAC caches catching up); past that it is wrong (a verb, the Secret's name or the
   account), and `ForgeCredentialReady` says `GrantIncomplete`: the same commands
   replace it, a half grant included. Until the grant works, conditions wait with
   `WaitingForCredential`, the steps needing the credential do not read it, and
   the rest of the install runs:
   - a bring-your-own forge: the ArgoCD tier waits before writing anything, since
     tenant repo refs resolve against the credential's url (the app's forge URL).
     New projects, and namespaces added to a project, do not sync, sync
     impersonation does not change, and the platform repo and both webhooks wait;
     what Argo already syncs keeps syncing.
   - a managed forge: no token is minted or replaced (before minting into an
     admin-named credential the operator asks whether it may store it, patch and,
     while it is absent, create, so it never mints a token it would lose), and the
     platform repo and both webhooks wait. The ArgoCD tier goes on (its base is `spec.forge.url`),
     keeping the repo credentials Argo already has; after a move to another ArgoCD
     namespace it has none there, so private repos do not clone until the grant.

   A credential named like one of the operator's own Secrets (`dotvirt-session`,
   ...) is refused (`NameTaken`): a managed forge's token would replace that
   Secret's keys. Name another Secret.
4. **Secrets** (`SecretsReady`): generates the session key, the ApplicationSet-plugin
   token, the webhook secrets and, with SSO, the OAuth client secret. Create-once:
   they survive restarts and are never regenerated.
5. **Workload** (`WorkloadReady`): the ServiceAccount, drafts PVC, Service, Route,
   Ingress or HTTPRoute, NetworkPolicy (below) and the Deployment, owner-referenced to the CR for
   automatic cleanup. `True` only once the dotvirt pod is ready; `Progressing` until
   then, without holding back the later phases. Single replica, see below.
6. **GitOps wiring** (`ArgoReady`): mirrors the plugin token into the ArgoCD
   namespace and trusts the managed forge's Route there (below), then applies the
   bindings of the static operand ClusterRoles (shipped in
   `config/rbac/operand_roles.yaml`; the operator only `bind`s them, never authors
   roles), one AppProject per tenant project plus `dotvirt-platform`, the per-project
   ApplicationSet, the static platform Application and the Argo repo-credentials.
   These live outside the CR's namespace, so a finalizer reclaims them on delete.
   dotvirt's own ServiceAccount reads Applications cluster-wide but may patch them
   only in the ArgoCD namespace (RoleBinding `dotvirt-sync-request`): a one-VM sync
   request and the refresh annotation, never spec. An Application elsewhere is
   not resynced or refreshed by dotvirt; Argo's own webhook and poll still apply.
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
   A generator failure the dotvirt pod's restart explains is not `Degraded`:
   `ArgoReady` is `Unknown` (`WaitingForDotvirt`) while the pod is down, then
   `Unknown` (`Refreshing`) while the operator asks Argo to run the generator again
   instead of waiting for its three-minute retry. A failure still there a minute
   after the operator asked is `Degraded`.
   Each AppProject names the ServiceAccount its Applications sync as once sync
   impersonation is on (below): `<argoNS>/dotvirt-p-<project>` for a tenant,
   `<argoNS>/dotvirt-platform-sync` for the platform tier. Names only: Argo
   impersonates by username and RBAC binds by name, so no object is created.
7. **Sync impersonation** (`SyncImpersonationReady`): on unless opted out, see
   [Sync impersonation](#sync-impersonation). Best-effort: never a halt,
   never `Available`.
8. **Platform repo** (`ForgeRepoReady`): ensures the platform git repo exists
   (`forge.EnsureRepo`, the imperative step a declarative installer can't do) and,
   for a managed forge, its owner org. Best-effort: a failure is recorded and the
   pass goes on. The condition is absent when no platform repo is configured.
9. **Argo webhook** (`ArgoWebhook`): registers one org-level forge-to-ArgoCD webhook
   for instant sync; `Unknown` when no ArgoCD URL resolves (Argo falls back to its
   poll).
10. **dotvirt webhook** (`DotvirtWebhook`): the forge-to-dotvirt hook is registered by
   the app itself; the operator only observes it.

When the forge refuses dotvirt's forge account the owner's webhooks (it owns no
org, or staff manage the hooks, e.g. as a system hook), both conditions read
`Unknown` with reason `RegistrationRefused`, carrying the forge's own reason.
The message says to make the account an owner of the org, or gives the hook to
create by hand as a Forgejo (Gitea) webhook (an org hook, a system hook, or one
on each project repo, repos created later included): its URL,
`application/json`, its events (push and pull_request for dotvirt, push only for
ArgoCD) and the Secret and key holding its shared secret, with the `oc extract`
command that prints it. Other reasons:

- `CredentialRejected`: the forge rejects the token itself (401); replace the
  token in the forge credential Secret's `token` key.
- `PerRepoHooks`: the owner is a user account, which has no org hooks; the app
  registers a hook on each project repo (not observed by the operator), and the
  ArgoCD hook is made by hand per repo or as a system hook.
- `OwnerNotFound`: no org or user of the platform repo's owner exists; check
  spec.forge.platformRepo and spec.forge.url.

Neither the operator nor the app asks the forge again for an hour, or until
spec.forge, the forge credential, the hook's secret or the ArgoCD URL changes,
or they restart, so a refusal costs about one forge call per hour. The operator
cannot observe deliveries, so check a hand-made hook in the forge's delivery
log.

`Available` is `True` only after a completed pass with the dotvirt pod ready. A
phase that fails or blocks sets it `False` with that phase's reason; the
best-effort phases (operator guard, sync impersonation, platform repo, both
webhooks) never do, since the operand serves without them.
`status.phase` is `Provisioning`, `BlockedOnDependencies` or `Ready`.

A completed pass requeues after 10 minutes, so drift on the kinds the operator does
not watch (Services, AppProjects, bindings) heals without a CR edit; while the
platform repo is missing, after 1 minute instead. Edits to the CR trigger a pass
only when they change its spec (or delete it), never a status write.

### Prune

After a completed pass whose render changed, and otherwise at most once every 5
minutes, the operator deletes the objects carrying this install's instance labels
that the pass did not render: a Route left by a switch to Ingress,
the platform tier after `spec.forge.platformRepo` is cleared, everything in a former
ArgoCD namespace, a managed forge switched off. A tenant AppProject whose project
is gone, and the shared `dotvirt-tenants` AppProject of earlier versions, are kept
while an Application still references them (the former admitting nothing), then
pruned.
Prune reads each name the install renders instead of listing: the operator may
not list Secrets, ConfigMaps or bindings beyond the two it watches by name
(argocd-cm and the forge credential's grant). Tenant AppProjects, named after
their projects, are the one kind prune lists, and only in the ArgoCD namespaces
the install wrote. The ArgoCD-namespace objects are swept only there: the current
namespace and the ones `status.argoNamespaces` records (each one a pass wrote
since the last completed prune), so a labelled copy an admin made elsewhere
survives.
Never pruned: anything without the labels, and Secrets, PersistentVolumeClaims and
the CA ConfigMaps in the install namespace (generated keys, the minted forge token
and the forge's git data are state, not render). A pass that halts prunes nothing.
Deleting the CR runs the same prune with nothing rendered, after taking sync
impersonation off the ArgoCD instance if the operator set it there (below).

The operator's own RBAC names each object it writes (`rbac_markers.go`), so it
cannot write any other Secret, ConfigMap, binding or Application in the cluster,
nor read any other Secret or ConfigMap. A rule without names says why beside it:
the reads that stay wider are any RoleBinding by name (the ones the platform
repo manages), every Application listed (sync impersonation), and Deployments
and Routes listed and watched (the cache, by label). AppProjects are the one
write exception: tenant ones are named after their projects, so the grant covers
any AppProject and prune's label check is what keeps it to this install's. An object only an older
version rendered under another name is never read, and left for an admin to delete.
A forge credential under an admin-chosen name is reached only through the
`dotvirt-forge-credential` grant a cluster admin installs (Forge, above).

The labels are `dotvirt.io/instance` (the CR name) and
`dotvirt.io/instance-namespace` (its namespace). Objects from versions that set only
the first are still recognized as this install's; the next apply relabels them.

`-dry-run` server-side-applies every rendered resource with `dryRun=All`: the API
server validates schema, admission, and RBAC, and nothing is persisted, a spec check
against a real cluster. Phases whose work dry-run cannot model (the secrets, the
forge bootstrap, the workload rollout, both webhooks, the platform repo) record a
`DryRun` condition instead, and so does `Available`: nothing serves.

## Admin objects

Some objects an install needs are ones its operator must not be able to create,
because the operator could then grant itself anything. The console renders them
itself and a cluster admin installs them; the operator only reports them.

A cluster admin signed in to the console sees a banner listing what is missing or
differs (and what differs), the pins the render uses, one **Install** button, and the
same render to download. Install applies the render under the admin's own token:
- the console reads every pin under the admin's token, never from anything the
  operator writes (its status, or the console's own environment): its own namespace
  (from its ServiceAccount mount, which the guard keeps from the operator), the one
  Dotvirt resource there (`spec.argocd`, `spec.image`, `spec.forge.insecureTLS`,
  `spec.metrics`), the operator's Deployment (among those named
  `dotvirt-operator-controller`, the one whose account may patch Dotvirt status there:
  its account and `RELATED_IMAGE_*`), and whether routes are served (the ArgoCD
  defaults);
- it is offered only when reviews say that token may read those, and read, create and
  patch every object an install may ask for (`pkg/adminobjects` allowlist);
- besides the guard, an install whose `spec.forge.credentialsSecret` names an admin's
  Secret (not `dotvirt-forge`, nor one the operator generates for itself) asks for
  the forge credential's grant: Role and RoleBinding `dotvirt-forge-credential` in the
  install namespace, get on that Secret (plus create and patch for a managed forge)
  for the operator's account, owned by the Dotvirt resource; the operator renders the
  same objects (one source) and watches them by name, so its forge steps go on at once;
- it takes back any field someone changed since (a forced server-side apply): it
  exists to restore the render, which is why the banner says what differs first;
  over a live object it keeps the owner references as they are, so a grant an admin
  made by hand stays theirs (adding an owner would also take delete on it);
- a forge credential grant no install asks for any more is listed `Stale`, with the
  command to remove it ([Forge credential grant](#forge-credential-grant)), never applied.

Without the console, use the release's `install/guard.yaml` for the guard (the forge
credential's grant is [below](#forge-credential-grant); the template, which renders
the guard for any install, leaves it out): derive each value from
the cluster as its header shows (never copy one from the operator's status or
conditions, which the operator writes), fill its placeholders and apply it with
`kubectl apply --server-side --field-manager=dotvirt`. kubectl reports a
conflict on any field someone else changed; review it, and add `--force-conflicts` to
take the field back.

That template is also the path after moving the install to another namespace or
reinstalling it elsewhere: the installed guard then refuses the operator's first
Secret write in the new namespace, before the console is deployed there, so no
console comes up to offer Install until the guard is re-applied for the new pins.

The operator does not watch these objects: their state in status follows on its next
pass (within 10 minutes), the console's banner at once.

A release must keep its operand inside the previous release's guard, or every upgrade
stops at the first refused write until an admin re-installs, while the old console
still reads `Installed`. envtest checks this against
`internal/controller/testdata/guard-previous.yaml`, the previous release's
`install/guard.yaml`: refresh it from the released asset at each release. A release
whose operand fails it ships with a re-install note. They stay after uninstall
(nothing that ran can delete them); they match only the operator's username, and an
admin may delete them.

### Forge credential grant

Without the console, a cluster admin grants the forge credential this way, every
value derived from the cluster, never from the operator's status or conditions,
which the operator writes. Derive `OP` (the operator's namespace/account) and
`INSTALL_NAMESPACE` as `install/guard.yaml`'s header shows, then:

```sh
INSTALL_NS="$INSTALL_NAMESPACE"
OPERATOR="${OP%/*}:${OP#*/}"
CREDENTIAL="$(kubectl -n "$INSTALL_NS" get dotvirt -o jsonpath='{.items[0].spec.forge.credentialsSecret}')"
VERBS=get
[ "$(kubectl -n "$INSTALL_NS" get dotvirt -o jsonpath='{.items[0].spec.forge.managed}')" = true ] && VERBS=get,create,patch
kubectl -n "$INSTALL_NS" delete role,rolebinding dotvirt-forge-credential --ignore-not-found
kubectl -n "$INSTALL_NS" create role dotvirt-forge-credential --verb="$VERBS" --resource=secrets --resource-name="$CREDENTIAL"
kubectl -n "$INSTALL_NS" create rolebinding dotvirt-forge-credential --role=dotvirt-forge-credential --serviceaccount="$OPERATOR"
```

The delete makes it replace a wrong or half grant too. Created this way, the grant
outlives the install: remove it with the CR. Once `spec.forge.credentialsSecret` is
unset or `dotvirt-forge` again, a grant left behind still lets the operator reach the
old Secret: status and the console's banner list it as `Stale`, to remove with

```sh
kubectl -n "$INSTALL_NS" delete role,rolebinding dotvirt-forge-credential
```

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
platform repo then gains, in the project's publish namespace `<project>-publish`:

- one Gateway `publish` of `gatewayClassName`, whose listeners take only
  `*.<project>.<domain>` and only HTTPRoutes from namespaces labeled with the
  project (`dotvirt.io/project`), a label no tenant role may change;
- with `clusterIssuer`, a cert-manager Certificate for that wildcard and an HTTPS
  listener serving it;
- `publish-view`, binding `dotvirt-tenant-gateway-view` (Gateway and Certificate
  reads, never Secrets) to the subjects of the admin grants of the namespaces the
  Gateway reaches. Without such a grant in the platform repo none is staged: a
  provider binds its own groups there.

And `<namespace>-routes` in each namespace the Gateway reaches, binding
`dotvirt-tenant-routes` (HTTPRoute writes) to the subjects of that namespace's admin
grant and the project's sync account. It is left out where the cluster's `admin`
role already writes HTTPRoutes (OpenShift).

The publish namespace holds nothing of the tenant's:

- It carries `dotvirt.io/publish=<project>` and never `dotvirt.io/project`, so no
  tenant Application targets it and no tenant grant reaches it; the shared tenant
  AppProject refuses `*-publish` too.
- dotvirt refuses project and namespace names ending in `-publish`, and never joins
  a namespace labeled `dotvirt.io/publish` to a project.
- It is created with the project, never when publishing is turned on: a namespace
  joins a VM Network only when created. New Project creates it once publishing is
  configured, on the project's VM Network when the project has one; a provider's
  onboarding creates it otherwise. A project made before the install published gets
  it from "Create `<project>-publish`" on its Configure tab, on the project's VM
  Network, or on the cluster's own network where one of its namespaces is. Until it
  exists, the project says publishing waits on it.
- A namespace a user requested is never one: it carries `openshift.io/requester`,
  and that user is its admin. On OpenShift any user may request any free name
  (`self-provisioner`), so one may take `<project>-publish` before the platform
  repo's namespace syncs. The project then cannot publish, and its Configure tab
  says to delete that namespace, then create `<project>-publish` from that tab.

The Gateway reaches the project namespaces on its publish namespace's network.

A project's VM Network is one primary ClusterUserDefinedNetwork, which New Project
creates with the project. It spans the namespaces labeled `dotvirt.io/network=<project>`:
its first namespace, its publish namespace, and each namespace that joins it from New
Namespace, all labeled at creation, since OVN takes a primary network only then. Only
the platform writes namespace labels, so no tenant moves a namespace between
networks. dotvirt reads a namespace's network from the cluster, never from that
label alone, and the label key is the selector a provider's own onboarding may use
too. A released namespace keeps its label, so its VMs keep their network: it never
joins another project, and a new project by the released one's name cannot make a
VM Network while such namespaces, or a network selecting them, remain.

A namespace with a VM Network of its own is not reached; a customer who wants a
gateway there runs their own, which is outside dotvirt for now (Gateway stays a
platform kind: a tenant listener could claim any hostname). A namespace that joins
the project later gets its grant from "Update publishing" on the project card.

Settings:

- `domain`: the zone each project gets a subdomain of.
- `gatewayClassName`: the class of each project's Gateway.
- `clusterIssuer` (optional): a ClusterIssuer able to issue each project's
  wildcard, so DNS-01. Without it the Gateways serve HTTP only.
- `portailNamespace` (optional, default `portail-system`): where portail's
  controller and its proxy Role live.

Publishing is off until `domain` and `gatewayClassName` are both set.

What stays with the platform:

- DNS and addresses. Point `*.<domain>` at your edge, which routes each project's
  hostnames to its Gateway; a project with its own address gets its DNS pointed
  straight at it. Giving a project's Gateway an address is a platform-repo change a
  platform admin stages, never the tenant's. dotvirt writes no DNS records.
- The Gateway's Service is the GatewayClass's default; whether a LoadBalancer
  Service is reachable on a VM Network is not verified yet.
- A project on a VM Network is offered publishing once the publishing
  GatewayClass carries `portail.epheo.eu/GatewaysIsolated=True` (portail with
  per-Gateway poll caps and local token verification): until then one project could
  delay the others' route changes. Its Gateway reaches portail's controller only
  through the apiserver, so dotvirt stages its GatewayParameters and binds portail's
  `portail-controller-proxy` Role, in `portailNamespace`, to its pods' account. That
  Role comes with portail's `deploy/components/apiserver-proxy`.
- Shared Gateways. HTTPRoute write lets a namespace attach routes to any Gateway
  whose listeners admit its namespace. A Gateway shared across projects must keep
  `allowedRoutes` at `Same` or a selector of platform namespaces, or it serves
  tenant hostnames. That is Gateway API, but this grant turns it on.
- cert-manager. The staged Certificate lives in the publish namespace, out of
  tenants' reach. Where tenants hold Certificate writes in their own namespaces
  (cert-manager aggregates them into `admin` by default), they can still request
  any name a ClusterIssuer can prove. Keep them from tenants on clusters that
  publish.

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

Until sync impersonation is in effect, the `dotvirt-argocd-apply` binding grants the
shared ArgoCD application controller ServiceAccount apply rights on everything
dotvirt's PRs emit, including `MachineConfig`, `Subscription`/`OperatorGroup` and
`KubeDescheduler` (the DRS platform objects) and RoleBindings. It is cluster-wide:
every Application that controller reconciles gains them, not only dotvirt's. The
tenant AppProjects keep each tenant repo to namespaced workloads in its own
namespaces; Applications in other AppProjects on a shared ArgoCD are bounded only by
their own AppProject.

Once impersonation is in effect (Argo reads the setting, dotvirt's AppProjects keep
their accounts, and a pass has reported `SyncImpersonationReady` on), the controller
applies nothing of dotvirt's, and the operator drops it from that binding. It keeps
`dotvirt-argocd-read`, the same kinds with read verbs only: Argo diffs and
health-checks with its own account. Turning impersonation off binds the controller
again in the same pass, before the setting leaves `argocd-cm`.
That read covers MachineConfigs, which the platform tier declares for rebalancing's
PSI kernel argument. Rendered MachineConfigs carry the cluster pull secret, and list
and watch cannot be scoped by name, so on an ArgoCD whose controller does not
already read every kind, this role is what lets it read them. OpenShift GitOps'
controller already reads every kind.
While impersonation is off, give dotvirt its own ArgoCD instance where other teams'
Applications must not hold these rights.

The binding also names the platform tier's sync account, which applies the platform
repo once sync impersonation is on. Its own `dotvirt-platform-sync` binding adds the
Namespaces that repo declares and `bind` on `admin` and `dotvirt-tenant-egress`, the
roles project grants name.

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

## OpenShift SSO

dotvirt offers "Sign in with OpenShift" (see [Sign-in screen](#sign-in-screen)):
the OAuth access token the cluster hands back is a normal bearer token, so it rides
the same TokenReview + per-request pass-through path as a pasted one.

It is on by default wherever the cluster runs the OpenShift OAuth server and
`spec.auth.oidc` is unset. MicroShift, and an OpenShift that signs in through an
external OIDC issuer, serve no OAuthClients, so it stays off there. Set
`spec.auth.openShiftSSO: false` to keep it off:

```yaml
spec:
  auth:
    openShiftSSO: false
```

The operator generates the client credential and, once the console host is assigned,
publishes a ready-to-apply command in `status.ssoOAuthClient`: one `oc apply` that
registers the cluster-scoped `OAuthClient` with the right redirect URI. Registering that
cluster-scoped object stays a cluster-admin act the operator deliberately leaves to you
(it holds no `oauthclients` grant); the client secret is read from the generated Secret
at apply time, so it never lands in status.

A cluster admin signed in to the console can register it instead (Finish SSO). The
console names the client `dotvirt-<namespace>` after its own namespace and takes its
redirect, for registering and for signing users in alike, from the install (its own
account reads it to sign users in, the admin's token to register): `spec.ingress.host`, else the host
OpenShift's router gives the console's hostless Route (`dotvirt-<namespace>.<cluster
ingress domain>`). It never takes either from the public URL the operator writes into
its environment: a redirect of the operator's choosing would receive every login code,
and the operator holds the client secret. Review `status.ssoOAuthClient` the same way
before running it: its redirect comes from the operator.

The simplest way: sign in to the console once with a token (`oc whoami -t`) as a
cluster admin and click "Finish SSO setup"; it registers the client with your own
rights. Or run the command yourself:

```console
$ oc -n <install ns> get dotvirt dotvirt -o jsonpath='{.status.ssoOAuthClient}'
# copy-paste and run the printed `oc apply` command
```

Upgrading from 0.2.0 or earlier: an install that never set `spec.auth.openShiftSSO`
turns SSO on. Until a cluster admin finishes setup, the console shows them the banner,
and everyone can still sign in with a token. Set `spec.auth.openShiftSSO: false` before
upgrading to keep it off.

The server-side code exchange calls the cluster's oauth Route, which the router CA
signs; the Deployment points `DOTVIRT_OAUTH_CA` at the mounted ingress-CA copy, so
no extra trust configuration is needed.

## OIDC sign-in (optional)

dotvirt can offer "Sign in with <name>" for any OpenID Connect issuer the apiserver trusts, in place of OpenShift SSO.
The issuer's ID token is the user's bearer token, so it rides the same TokenReview and per-request pass-through path as a pasted one.

The apiserver must accept dotvirt's client ID as an audience of that issuer (its `AuthenticationConfiguration`, or `--oidc-client-id`).
Register `https://<console host>/api/auth/callback` as the client's redirect URI.

```yaml
spec:
  auth:
    oidc:
      issuerURL: https://idm.example.com/oauth2/openid/dotvirt
      clientID: dotvirt
      clientSecret: {name: dotvirt-oidc, key: secret}
      scopes: [profile, groups]
      displayName: Example ID
```

- `issuerURL`: exactly as the tokens' `iss` claim names it; dotvirt refuses an issuer whose discovery document names another.
- `clientSecret`: a Secret key in dotvirt's namespace; unset for a public client, which PKCE alone protects. The console reads it when it starts: restart it after a rotation.
- `scopes`: asked beside `openid`; those whose claims the apiserver maps to a username and groups.

Sign-in uses PKCE (S256) and a nonce.
The session's tokens stay in dotvirt's memory; the cookie holds a random handle.
dotvirt renews the ID token with the refresh token once three quarters of its life has passed, once per session however many requests arrive.
An issuer that refuses the refresh ends the session, and so does a renewal naming another subject (`iss` and `sub`).
The issuer must send a new ID token on refresh, as Kanidm and Keycloak do; one that does not signs users out once per ID token life.
Logout revokes the refresh token at the issuer, when it publishes a `revocation_endpoint`.
A restart of dotvirt signs every OIDC session out: the next sign-in is one redirect through the issuer.

## Sign-in screen

With a sign-in flow ready (OIDC, or OpenShift SSO with its OAuthClient registered), the sign-in screen goes straight to it.
A user already signed in at the provider comes back signed in, with no click, to the page they opened.

| The screen | What it shows |
|---|---|
| A flow is ready | Nothing: it redirects to the provider |
| `?sso_error` (the last sign-in failed) | One "Sign in with ..." button and the failure |
| `?signed_out`, or a sign-out just ended the session on this page | One button: a redirect would sign the user straight back in through the provider's own session |
| A redirect already ran in this tab within a minute | One button: the provider round trip left no session, so it is not retried in a loop |
| The tab cannot record its redirects (browser storage blocked) | One button, every time: nothing else would stop such a loop |
| No flow ready (none configured, or OpenShift SSO not finished) | The token form, as the only way in |
| `?token` | The token form, whatever is configured |

`https://<console host>/?token` is the break-glass for admins: a pasted token (`oc whoami -t`, or `kubectl create token`) signs in when the provider is down.
The screen links to it nowhere.

A sign-in returns only to a page of the console itself: dotvirt refuses another origin, any `/api/` path (decoded, as the router reads it), and a return whose part before the `?` (fragment included) cleaning would change.
A provider that cannot be reached when a sign-in starts lands on the failure button, with the detail in dotvirt's log.
Each sign-in keeps its own state cookie, so tabs signing in at once do not undo each other.

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

## Image signatures

Every image CI pushes to quay.io/epheo is signed with sigstore (`hack/sign-images.sh`).
Enforcing them is opt-in for admins and adds no load to dotvirt: the container runtime
checks a signature when it pulls.

- **What is signed:** `dotvirt`, `dotvirt-operator`, `dotvirt-operator-bundle` and
  `dotvirt-operator-catalog`, by digest, the multi-arch index and each platform
  manifest. The signature sits beside the image as a `sha256-<hex>.sig` tag, the layout
  container runtimes read.
- **Keyless, always:** each signature is certified for the workflow that pushed the
  image, with no key to leak. The identities, all with OIDC issuer
  `https://token.actions.githubusercontent.com`:
  - a release: `https://github.com/epheo/dotvirt/.github/workflows/release.yaml@refs/tags/vX.Y.Z`;
  - a release candidate: `https://github.com/epheo/dotvirt/.github/workflows/preview.yaml@refs/heads/main`;
  - a per-commit app image from main: `https://github.com/epheo/dotvirt/.github/workflows/ci.yaml@refs/heads/main`.
- **By a key:** every image also carries a signature by the key whose public half is
  `operator/install/cosign.pub`. OpenShift can enforce only that one (below).
- **What the key accepts:** the same key signs releases, release candidates and the
  per-commit app images from main. A policy on this key therefore admits all three, not
  releases only. A stricter, release-only policy needs a second key that only
  release.yaml holds; the keyless identities already tell the three apart.
- **Before v0.2.4:** releases are unsigned. Enforce only once the cluster runs a signed
  release, or a rollback to an older one is refused.

Verify a release by hand (cosign 3 needs `--new-bundle-format=false` for this layout):

```sh
cosign verify --new-bundle-format=false \
  --certificate-identity-regexp '^https://github\.com/epheo/dotvirt/\.github/workflows/release\.yaml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  quay.io/epheo/dotvirt@sha256:<digest>
```

With the key: `cosign verify --new-bundle-format=false --key operator/install/cosign.pub quay.io/epheo/dotvirt@sha256:<digest>`.

### Enforcing on OpenShift

OpenShift matches a keyless certificate by email only, and a workflow certificate
carries none, so a ClusterImagePolicy enforces the key signature:

```yaml
apiVersion: config.openshift.io/v1
kind: ClusterImagePolicy
metadata:
  name: dotvirt
spec:
  scopes:
  - quay.io/epheo/dotvirt
  - quay.io/epheo/dotvirt-operator
  - quay.io/epheo/dotvirt-operator-bundle
  - quay.io/epheo/dotvirt-operator-catalog
  policy:
    rootOfTrust:
      policyType: PublicKey
      publicKey:
        keyData: <base64 -w0 operator/install/cosign.pub>
        rekorKeyData: <curl -fsSL https://rekor.sigstore.dev/api/v1/log/publicKey | base64 -w0>
    signedIdentity:
      matchPolicy: MatchRepository
```

- `MatchRepository` is required: a cosign signature names the repository without a
  tag or digest, which the default `MatchRepoDigestOrExact` refuses.
- The scopes cover the operand, the operator, and the bundle and catalog OLM pulls.
  Images dotvirt runs but does not build (the managed Forgejo, ArgoCD) are outside them.
- The API is `config.openshift.io/v1` from OpenShift 4.20. OpenShift 4.19 serves it as
  `v1alpha1`, only behind the `TechPreviewNoUpgrade` feature set. Check with
  `oc api-resources | grep -i imagepolicies`.
- After a key rotation, update `keyData` (SECURITY.md, Image signing key).
- The Machine Config Operator rolls the policy out to every node; see your version's
  documentation for how it updates them.
- Mirrored (disconnected) installs must mirror the `.sig` tags too, or every dotvirt
  image is refused.

### Enforcing on other Kubernetes

The sigstore policy-controller matches workflow identities, so it enforces the
keyless signatures with no key:

```yaml
apiVersion: policy.sigstore.dev/v1beta1
kind: ClusterImagePolicy
metadata:
  name: dotvirt
spec:
  images:
  - glob: "quay.io/epheo/dotvirt*"
  authorities:
  - keyless:
      url: https://fulcio.sigstore.dev
      identities:
      - issuer: https://token.actions.githubusercontent.com
        subjectRegExp: '^https://github\.com/epheo/dotvirt/\.github/workflows/release\.yaml@refs/tags/v[0-9]+\.[0-9]+\.[0-9]+$'
    ctlog:
      url: https://rekor.sigstore.dev
```

Add the preview identity to accept release candidates too. The policy-controller
enforces only in namespaces labelled `policy.sigstore.dev/include=true`.

## Upgrading

- The operator acts only on the Dotvirt in its install namespace (see Install
  namespace). An existing install keeps working: one in the operator's namespace
  as before, one elsewhere (`dotvirt`, from the earlier sample) adopted from the
  ClusterRoleBinding the earlier operator provisioned for it. Its
  `InstallNamespaceAdopted` condition suggests setting `OPERATOR_INSTALL_NAMESPACE`
  explicitly; check the namespace it names, as Install namespace says.
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

## Where browsers open the forge

Every forge link dotvirt shows (pull requests, compare pages, repositories) starts with `spec.forge.url`.
Set `spec.forge.webURL` when browsers must open the forge elsewhere:

- the forge's URL is in-cluster, which no browser reaches;
- an app of yours fronts the forge, serving `<owner>/<repo>`, `.../pulls/<n>` and `.../compare/<base>...<head>`.

dotvirt's API and git calls still go to `spec.forge.url`.
Unset, links start with `spec.forge.url`.
A managed forge also takes `webURL` as its `ROOT_URL`, so its own pages and clone links name it.

## Metrics

`spec.metrics.url` turns on the Performance tab, host load and alarms.
dotvirt queries with each caller's own token, never its ServiceAccount's.

- A caller allowed `get` on `prometheuses/api` (`monitoring.coreos.com`) named `k8s` in `openshift-monitoring` queries `url` across every namespace.
  Through dotvirt no one else reaches it, but the endpoint must still check the token itself: unauthenticated, anyone with network reach to it reads every namespace's series.
- Every other caller queries `tenancyURL`, once per namespace, with a `namespace=<ns>` parameter and a `{namespace="<ns>"}` matcher.
  That endpoint must allow only a caller who may `get` `pods.metrics.k8s.io` in that namespace, and force the query to it (kube-rbac-proxy plus prom-label-proxy, as OpenShift's Thanos querier does on 9092).
  That check is what keeps tenants apart.

On OpenShift, `url: https://thanos-querier.openshift-monitoring.svc.cluster.local:9091` is enough: the tenancy port and the service CA are derived.
Elsewhere, set both endpoints and the CA that signs them:

```yaml
spec:
  metrics:
    url: https://prometheus-wide.monitoring.svc:8443
    tenancyURL: https://prometheus-tenancy.monitoring.svc:8443
    ca: {name: metrics-ca, key: ca.crt}
```

- `tenancyURL` unset, outside OpenShift: callers without the cluster-wide right get no metrics.
  It must differ from `url`.
- `ca`: a ConfigMap key in dotvirt's namespace, trusted for both endpoints; it needs an https `url` or `tenancyURL`.
  Unset, an https `.svc` `url` trusts OpenShift's service CA, any other the system pool; with `spec.forge.insecureTLS`, that other skips verification.
  It is mounted optional: missing, metrics TLS fails verification and the console stays up.
  The console reads it when it starts: restart it after a rotation.

The series read: KubeVirt's `kubevirt_vmi_*` (labels `namespace`, `name`, `node`), kube-state-metrics' `kube_node*`, node-exporter's `node_cpu_seconds_total` and `node_memory_*` with `instance` set to the node name, and firing `ALERTS` carrying `namespace`.

## Branding

`spec.branding` dresses the console as a provider's; unset, it is dotvirt's own.

- `productName`: shown in place of dotvirt throughout the console, and in the pull request titles, descriptions and commits it writes to the forge. Messages the server writes say "the console". At most 40 characters, no control characters.
- `links`: up to 5 masthead links back to your own app (account, billing). Each a text `label` of at most 32 characters and an `https` `url` with a host and no credentials. They open in the same tab, without a Referer.
  An optional `icon` names an `.svg` or `.png` file of `configMap`: the link then shows as that icon alone, its label the tooltip and accessible name. A missing file leaves the link as text.
  A link whose URL has the console's own origin is shown as the current item, marked and not a link.
- `configMap`: a ConfigMap in dotvirt's namespace holding:
  - `theme.css`, loaded after the console's own stylesheet, so it may override its tokens (`--color-accent` with its hover fill `--color-accent-deep` and their ink `--color-accent-on`; the status fills `--color-ok`, `--color-warn`, `--color-danger` with their ink `--color-ok-on`, `--color-warn-on`, `--color-danger-on`; surfaces, ink, `--font-sans`); dark-theme values go under `:root[data-theme='dark']`, where the console declares its own;
    The console pairs each of these fills with its own ink (white, or dark on amber and on the dark theme's status fills) unless a theme says otherwise; a theme that changes a fill sets its ink with it (and, for the accent, `--color-accent-deep` too), each pair at 4.5:1 or more.
  - `logo.svg` or `logo.png`, shown beside the product name;
  - the links' icons.

dotvirt serves these files itself, each up to 256 KiB, from the same origin: the console's content-security policy stays as it is, so a stylesheet that loads a remote font or image is refused. Embed fonts as `data:` URLs instead.
An SVG logo or icon opened on its own runs sandboxed, with nothing to load. dotvirt serves only the icons a link names, never another file of the ConfigMap.
The ConfigMap is mounted optional: missing, the console keeps dotvirt's look. An edit shows on the next page load.
The sign-in screen reads the branding before any session, so it holds nothing private.

## Who opens pull requests

By default dotvirt opens each proposal's pull request with its forge account.
Then the forge sees that account as the poster, and the requester may approve
their own change.

`spec.forge.pullRequests: Users` leaves the pull request to the requester:

- dotvirt pushes the branch and answers with the forge's compare link;
- the requester opens the PR there, as themselves: the forge refuses their own approval;
- a PR opened on the head dotvirt pushed is adopted, from the forge webhook or the lane's refresh: its changes leave the draft, and later proposes extend it;
- one merged before dotvirt saw it open settles the draft the same way;
- dotvirt never opens, reopens or edits a PR, except the starter-template offer below.

The first segment under `dotvirt/proposed/` is the requester (`dotvirt/proposed/<user>/<project>-<hash>`), apart from two reserved segments that name no user:
- `revert/<user>/<project>-...`: a revert, by the next segment;
- `_seed/starter-templates`: the starter-template offer.

Review services match these segments exactly and case-sensitively: `revert` is a revert, `Revert` a user.
No first segment starts with `_` or equals `revert`. A first segment that would, or that starts with `u-`, gains a leading `u-` (`_bob` is `u-_bob`, `revert` is `u-revert`, `u-bob` is `u-u-bob`, `bob` stays `bob`). The escape is one-to-one: strip one leading `u-` from the first segment to read the name back.
The escape applies to the first segment only: a revert's user segment (`revert/<user>/...`) is the plain ref-safe name, so never strip `u-` there.
The segment is a readable form, not an identity: names are made ref-safe first, so `uw:alice` and `uw-alice` share a segment, and their branches stay distinct by the hash. Credit a change to the requester dotvirt records where it records one, not to the segment alone.

Proposal branches created before the upgrade keep their old names: the escape is not applied to them retroactively.
After the upgrade, a user whose ref-safe name starts with `_` or `u-`, or equals `revert`, proposes on a new branch.
A pull request still open on the old branch is no longer updated by the console: merge it as it is, or close it and stage those changes again.

`spec.forge.pullRequests: Delegated` acts with the requester's own forge account.
It suits a forge whose users sign in through the same identity provider as the console.
This build sets up the connection only: dotvirt still pushes and opens pull requests with its own account until the write path ships.

- Register dotvirt at the forge as an OAuth2 application: a confidential client, with the redirect URI `https://<console host>/api/forge/callback`.
- Put its client secret in a Secret in dotvirt's namespace, and set `spec.forge.userAuth`: `clientID`, `clientSecret` (that Secret and key), and `authorizeURL`, the forge's `/login/oauth/authorize` as users' browsers reach it.
- Set `spec.forge.url`: users' tokens go only to the forge the spec names, never to the url of the forge credential Secret. With a managed forge this also pins the managed forge's host.
- `tokenURL` defaults to `spec.forge.url` plus `/login/oauth/access_token`; set it when dotvirt reaches the token endpoint at another address.
- The kubelet reads the client secret for the console's pod: the operator never does, so the Secret needs no grant. Restart the console after a rotation.
- Users must be able to sign in to the forge at `authorizeURL`; the rest of the forge may stay private.
- The first sign-in of each console session sends the user to that page once per session and console process. The forge asks for consent the first time only, then answers at once. Leaving the page unanswered leaves the console usable; the next sign-in, or the user menu's "Connect git account", asks again.
- No user is sent there while dotvirt cannot reach the forge (its API probed at most every 30 seconds, or a forge call of the flow failing): the user menu still offers the connect. The browser reaches the authorize page through `authorizeURL`, which this check only approximates.
- The token rides the console session only: kept in memory, never written to disk, dropped at sign-out, at session expiry, and by the user menu's "Disconnect git account". A restart drops every connection; the next connect is the forge's silent answer.
- Disconnecting forgets the token; the grant itself stays at the forge until the user, the forge's admin, or deleting the forge account revokes it.
- dotvirt asks the scopes `write:repository read:user`, fixed for good: the forge refuses a grant asked with other scopes than it holds.

Leave `dotvirt/proposed/**` unprotected in the forge.
dotvirt rebuilds a proposal's branch on every propose and force-pushes it.
Forgejo refuses force pushes and deletions on any protected branch, for everyone.
dotvirt never overwrites a branch someone else moved: it pushes with a lease on the head it last pushed.
Anyone who may push can still add commits to a proposal's branch; the PR shows them.

The guarantee holds only while the requester is the PR's poster.
Anyone who may read the repo can open a PR from another user's pushed branch.
The requester could then approve their own change.
dotvirt adopts that PR all the same, as it matches the branch and head.
So require approvals from a reviewer team, or another rule the forge enforces, beyond "not the poster".

Protect the base branch's `.argocd-source.yaml` files too.
Argo CD's repo-server merges `.argocd-source.yaml` and `.argocd-source-<app>.yaml`, found at an Application's path, into the Application's source settings.
Such a file can change which files sync and how, the source type included, whatever the repo's layout says; no Argo CD setting turns this off.
dotvirt refuses to stage or revert one in, and lists one already in a project's repo as an issue; removing one stays open.
A push from outside the console is the forge's to refuse: on Forgejo, add the protected file patterns `.argocd-source*.yaml;**/.argocd-source*.yaml` to the base branch's rule and turn on "Apply the rule to administrators".
A pull request changing one, removal included, then cannot merge; only a forge instance administrator can still merge past it.

## Starter templates

A project repo with no template library is offered one, once: two example VirtualMachineTemplates under `templates/`.
The offer is always a pull request opened by dotvirt's forge account, in every `pullRequests` mode: no user asked for it, and a base branch that refuses direct pushes still takes it.

- Branch: `dotvirt/proposed/_seed/starter-templates`, into the base branch.
- Marker: the PR also adds `templates/.dotvirt-seeded`. Merged, it keeps the library from being offered again, even after its examples are deleted.
- Content: the branch is rebuilt from the base branch with exactly the two examples and the marker, whatever was on it before. If it moves between that push and the pull request, dotvirt closes the pull request rather than show content it did not write; that withdraws the offer for good, as any closed offer does. The Templates tab shows an open offer only while its head is the commit the running console pushed: a later push to the branch, or a console restart, hides it there; the pull request stays in the lane.
- When: a repo dotvirt creates is offered right away. Any other repo is offered when its pull request lane is next read, if its base branch holds neither the marker nor a template. Offers run one at a time.
- Once: a pull request from the seed branch in any state, open, merged or closed, its branch since deleted included, means the offer was made. Closing it unmerged declines it for good.
- Folder projects (`dotvirt.io/path`) are never offered: their mirror is scoped to the project's folder, and reading the repo root to decide or write the offer would evict it. They never were: the starter templates used to be committed only to a repo dotvirt had just created, which is always a whole-repo project.
- To skip it: commit `templates/.dotvirt-seeded` (any content) to the base branch, or close the offer's pull request.
- A failed offer is shown on the Templates tab, by its kind only (a hook's refusal keeps the hook's words), and retried at most once an hour; the detail is in the console's log. A withdrawn offer is shown as such and not retried.

Nothing in `templates/` is applied to the cluster, the marker included.
Protect the base branch as you like: the offer only needs `dotvirt/proposed/**` writable, as every proposal does.
