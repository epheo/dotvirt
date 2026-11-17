# Security Policy

## Reporting a vulnerability

Please report security issues **privately** — do not open a public issue for an
unfixed vulnerability.

- Preferred: GitHub **Security → Report a vulnerability** on
  <https://github.com/epheo/dotvirt> (private advisory).
- Or email **github@epheo.eu** with the details and, if possible, a reproduction.

You can expect an acknowledgement within a few days. Once a fix is available it is
released as a new digest-pinned version (see `hack/release.sh`) and, where relevant,
a GitHub Security Advisory.

## Scope

dotvirt's runtime **owns nothing**: it reads git/cluster/Argo and proposes pull
requests, riding the calling user's RBAC. The privileged install RBAC lives only
in the **operator** (install-time). One forge credential serves the operator, the
app (git and the forge API, including creating project repos) and ArgoCD's
repo-creds. Reports that concern the operator's privileges, the forge credential,
the user-token pass-through, or the GitOps PR-merge gate are especially welcome.

The operator acts only on the Dotvirt in its install namespace: one a cluster admin
names on its Deployment, else its own namespace, else the install it already
provisioned (from ClusterRoleBinding `dotvirt`, which only cluster admins and the
operator may write). OLM lets every namespace admin create a Dotvirt in their
namespace; earlier versions acted on the oldest Dotvirt anywhere, so a tenant's
could become the install while none existed and receive its cluster-wide
bindings. After upgrading, check that ClusterRoleBinding `dotvirt` names the
namespace you installed in (operator README, Install namespace).

### The operator guard

The operator needs wide rights to install: Secrets and Deployments by name, the
bindings of its operand roles, and Argo objects. A cluster admin can install, from the
console, admission policies that refuse the operator's own account some of what those
rights allow. The console renders them from what the admin's token reads, never from
anything the operator writes; the operator holds no right to create, change or delete
them. Until an admin installs them, nothing below is enforced.

The operator's role names only its own Secrets. A forge credential an admin names
(`spec.forge.credentialsSecret`) is granted the same way: a Role and RoleBinding
`dotvirt-forge-credential` in the install namespace, on that one Secret, rendered by the
console from the install's spec and installed under the admin's token; the operator
cannot grant it to itself.

What the guard closes:
- minting a ServiceAccount token through a Secret, and writing Secrets outside the
  install and ArgoCD namespaces;
- running anything but the console and the forge, each as its own account and from its
  own repository (or the image an admin chose), on the host, as root, privileged or with
  added capabilities, or the console with another entrypoint, an init container, an exec
  probe or hook, or a shared process namespace (the operator holds no right on pods, so
  it cannot exec into one or add an ephemeral container either);
- mounting anything over the console's binary or trust store: its volumes are its own,
  from the claim, ConfigMaps and Secret rendered or an admin chose, each mounted whole at
  its own path;
- giving the forge any Secret but its admin password, by env, `envFrom` or volume, or any
  volume but its own claim, a scratch directory and the router CA: no network volume
  (NFS, iSCSI, RBD, CephFS and the like), which would reach any export the nodes can;
- giving the console any Secret it was not rendered with: the kubelet resolves any Secret
  in the namespace a pod names, whatever the operator may read, and the console sends
  some env out (the forge token, to the forge URL). Each Secret-backed env is pinned
  whole, by env name, Secret and key: the console's own, the forge credential, and the
  OIDC client secret an admin chose;
- sending a forge credential an admin brought anywhere but the URL its Secret names;
- sending the OIDC client secret an admin chose to an issuer the admin did not name: the
  console's issuer is `spec.auth.oidc.issuerURL`, whose token endpoint receives that
  secret at every sign-in;
- binding the operator's claims to a volume, selector or data source of its choosing;
- unmounting the console's token through its ServiceAccount;
- leading the console's host anywhere but the console: its Service selects only the
  console's pods and port, its Route, Ingress or HTTPRoute leads only there, on the
  console's host, and no other exposure may claim that host, by name or by wildcard, or
  serve every host;
- exposing a Service outside the cluster: no node port, load balancer or external IP;
- sending the tokens signed-in users give the console anywhere an admin did not choose:
  no kubeconfig, apiserver address, `envFrom`, proxy or trust store of the operator's
  choosing; nothing mounted over the console's ServiceAccount token and cluster CA; no
  host alias or resolver beside the cluster's DNS; the metrics endpoints and CA only as
  the admin set them; the OAuth CA only the router CA; TLS verification off only as the
  admin set it; no cross-origin allowance; no pages served from elsewhere;
- binding any operand role to itself or another subject;
- rewriting its own install's spec, where an admin chose the images, the ArgoCD target,
  the metrics endpoints and the forge credential.

The CAs the console trusts come from ConfigMaps the operator writes (the router and
service CAs, and the admin's metrics CA it mounts). A CA of the operator's choosing
there intercepts nothing alone: the console reaches the pinned metrics host and the
OAuth server through the cluster's DNS, with no proxy, host alias or resolver of the
operator's choosing, so presenting a certificate for them takes a network position the
operator does not have.

The OpenShift SSO client the console registers (Finish SSO), and signs users in with,
is named and redirected from the install, never from the console's environment.

What stays open:
- the console's other configuration the operator writes as flags or `DOTVIRT_*` env,
  among them the image upload proxy, which receives the upload tokens the console mints
  for each upload;
- the `oc apply` command in `status.ssoOAuthClient`, which the operator writes: review
  its redirect before running it, or use Finish SSO;
- an older official release: any tag or digest in the official repositories passes,
  rollbacks included;
- reads: the operator lists Deployments, Routes, Namespaces and Argo Applications and
  AppProjects cluster-wide;
- Argo objects: AppProjects, Applications, the ArgoCD resource, `argocd-cm`,
  `argocd-rbac-cm` and `argocd-secret` reach Argo's own rights;
- the forge's commands, and its exposures on hosts of its own: it holds no user token;
- a managed forge's URL: the operator runs that forge and mints its credential;
- the storage class, size and access modes of the operator's claims;
- where no ingress domain can be read (MicroShift) and `spec.ingress.host` is unset, the
  console's host is the one routers report for its Route when the guard is installed:
  one the operator chose for that Route before then, or none yet admitted, is what the
  guard pins;
- the Route rules hold only where the apiserver serving Routes evaluates admission
  policies; verify it on each platform.

## Supported versions

Only the latest released version is supported. dotvirt is pre-1.0 (`v1alpha1`); fixes
land on `main` and in the next release.
