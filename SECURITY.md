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

Every image CI pushes is signed with sigstore, keyless under the workflow that built
it, and also by a published key once one is committed. A cluster that enforces those
signatures refuses a tampered dotvirt operator or console image before it runs; the
operator README (Image signatures) gives the identities, `cosign verify`, and the
OpenShift and Kubernetes policies. A signature proves only that dotvirt's own
workflow built the image: it cannot vouch for that workflow's inputs or for who may
push a release tag.

## Supported versions

Only the latest released version is supported. dotvirt is pre-1.0 (`v1alpha1`); fixes
land on `main` and in the next release.
