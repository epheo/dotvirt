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

### The operator guard

The operator needs wide rights to install (Secrets and Deployments, cluster-wide
bindings, Argo objects). A cluster admin can install, from the console, admission
policies that refuse the operator's own account some of what those rights allow. The
console renders them itself; the operator holds no right to create, change or delete
them. Until an admin installs them, nothing below is enforced.

What the guard closes:
- minting a ServiceAccount token through a Secret, and writing Secrets outside the
  install and ArgoCD namespaces;
- running a Deployment outside the install namespace, as another account, on the
  host, as root, privileged or with added capabilities;
- running its own code in the console, where signed-in admins' tokens arrive: images
  must come from the official repositories or be the exact image an admin chose;
- binding any operand role to itself or another subject: bindings carry only their
  rendered names, roles and subjects.

What stays open:
- reads: the operator still reads Secrets and other objects cluster-wide;
- Argo objects: AppProjects, Applications, the ArgoCD resource, `argocd-cm`,
  `argocd-rbac-cm` and `argocd-secret` reach Argo's own rights;
- an older official release: any tag or digest in the official repositories passes,
  rollbacks included;
- configuration through the console's environment, such as pointing sign-in at
  another issuer.

## Supported versions

Only the latest released version is supported. dotvirt is pre-1.0 (`v1alpha1`); fixes
land on `main` and in the next release.
