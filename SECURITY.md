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

The operator's own account is bounded by two ValidatingAdmissionPolicies a
cluster admin installs from the console (`dotvirt-operator-secrets`,
`dotvirt-operator-deployments`): no ServiceAccount token Secret, no Secret
outside the install and ArgoCD namespaces, and Deployments only in the install
namespace, as dotvirt's own accounts, off the host. The operator renders them
but holds no right to create, change or delete them, so it cannot loosen them.
They stay after an uninstall, match only the operator's account, and an admin
may delete them.

## Supported versions

Only the latest released version is supported. dotvirt is pre-1.0 (`v1alpha1`); fixes
land on `main` and in the next release.
