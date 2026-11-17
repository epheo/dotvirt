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
it, and by the key whose public half is `operator/install/cosign.pub`. A cluster that
enforces those signatures refuses a tampered dotvirt operator or console image before
it runs; the operator README (Image signatures) gives the identities, `cosign verify`,
and the OpenShift and Kubernetes policies. A signature proves only that dotvirt's own
workflow built the image: it cannot vouch for that workflow's inputs or for who may
push a release tag.

## Image signing key

- **What it accepts:** one key signs releases, release candidates and the per-commit
  app images from main, so a policy on it admits all three. A release-only policy needs
  a second key held only by release.yaml; until then, the keyless identities tell them
  apart.
- **Custody:** the private key is the `COSIGN_PRIVATE_KEY` secret (with
  `COSIGN_PASSWORD`) of the `QUAY` GitHub environment, read only by the jobs that sign,
  never by the build. The maintainer keeps an encrypted offline backup.
- **Rotation:**
  1. Generate a new pair; replace the environment secrets and commit the new
     `operator/install/cosign.pub`.
  2. Re-sign the four image digests of every supported release with the new key before
     announcing it (`cosign sign --yes --recursive --new-bundle-format=false
     --use-signing-config=false --key <new key> <image>@<digest>`). OpenShift's
     `PublicKey` policy holds one `keyData`, so the switch is a cutover: an image signed
     only by the old key is refused on its next pull once an admin moves `keyData` to
     the new key.
  3. Admins then update `keyData` in their ClusterImagePolicy.
- **Compromise:** rotate as above, and treat images signed by the old key after the
  suspected exposure as untrusted.

## Supported versions

Only the latest released version is supported. dotvirt is pre-1.0 (`v1alpha1`); fixes
land on `main` and in the next release.
