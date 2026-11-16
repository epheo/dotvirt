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

The operator needs wide rights to install: Secrets and Deployments by name, the
bindings of its operand roles, and Argo objects. A cluster admin can install, from the
console, admission policies that refuse the operator's own account some of what those
rights allow. The console renders them from what the admin's token reads, never from
anything the operator writes; the operator holds no right to create, change or delete
them. Until an admin installs them, nothing below is enforced.

What the guard closes:
- minting a ServiceAccount token through a Secret, and writing Secrets outside the
  install and ArgoCD namespaces;
- running anything but the console and the forge, each as its own account and from its
  own repository (or the image an admin chose), on the host, as root, privileged or with
  added capabilities, or the console with another entrypoint, an init container, an exec
  probe or hook, or a shared process namespace (the operator holds no right on pods, so
  it cannot exec into one or add an ephemeral container either);
- leading the console's host anywhere but the console: its Service selects only the
  console's pods and port, its Route, Ingress or HTTPRoute leads only there, on the
  console's host, and no other exposure may claim that host;
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
  among them the OIDC issuer, which allows phishing an admin's credentials at a page of
  the operator's choosing, and the image upload proxy, which receives the upload tokens
  the console mints for each upload;
- the `oc apply` command in `status.ssoOAuthClient`, which the operator writes: review
  its redirect before running it, or use Finish SSO;
- an older official release: any tag or digest in the official repositories passes,
  rollbacks included;
- reads: the operator lists Deployments, Routes, Namespaces and Argo Applications and
  AppProjects cluster-wide;
- Argo objects: AppProjects, Applications, the ArgoCD resource, `argocd-cm`,
  `argocd-rbac-cm` and `argocd-secret` reach Argo's own rights.

## Supported versions

Only the latest released version is supported. dotvirt is pre-1.0 (`v1alpha1`); fixes
land on `main` and in the next release.
