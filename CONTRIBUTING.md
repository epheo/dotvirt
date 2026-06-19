# Contributing to dotvirt

Thanks for helping. dotvirt is a monorepo: the app (root Go module + the SvelteKit
SPA under `web/`) and the installer **operator** (its own Go module under `operator/`,
tied by `go.work`). See [`operator/README.md`](operator/README.md) for the operator's
architecture.

## Develop & test

```sh
# App
go test ./...                       # backend
( cd web && npm ci && npm run check && npm run build )   # SPA

# Operator (from operator/)
make -C operator manifests generate # regenerate CRD + RBAC + DeepCopy (commit the result)
make -C operator build              # build the manager
make -C operator bundle             # regenerate + validate the OLM bundle
make -C operator scorecard          # scorecard (needs a cluster)
```

CI (`.github/workflows/ci.yaml`) runs `go vet`/`go test`, the SPA build, the operator
build, a **generated-manifests-in-sync** check, `operator-sdk bundle validate
--select-optional suite=operatorframework` + `opm validate`, and an on-cluster
kind + OLM install + scorecard. Run the equivalents locally before pushing.

## Sign your commits (DCO)

Every commit must carry a `Signed-off-by:` trailer matching the author (the
[Developer Certificate of Origin](https://developercertificate.org/)):

```sh
git commit -s -m "…"
```

This is enforced on the community-operator submissions (below) and expected here.

## Releasing

Two channels: **`alpha`** is released versions only (what external consumers
subscribe to); **`candidate`** carries release-candidates *and* releases (the QA/test
cluster subscribes here). Build with the CI-pinned `operator-sdk` (see the version in
`.github/workflows/ci.yaml`) so the bundle's builder metadata matches.

**Preview (QA on a test cluster), throwaway — never a published release:**
`hack/preview.sh` builds + pushes preview images and a `candidate`-only catalog, then
restores the working tree (nothing committed).

```sh
VERSION=0.0.6-rc.1 hack/preview.sh
kubectl apply -f operator/install/catalogsource-preview.yaml   # roll a candidate cluster
```

**Release:** `hack/release.sh` cuts a digest-pinned release — builds and pushes the
app, operator, bundle, and catalog to `quay.io/epheo`, resolves each immutable
`@sha256`, and pins them into `DefaultImage`, the CSV (`relatedImages` + the manager
Deployment), the catalog template, and the `CatalogSource`. Never pushes `:latest`.

```sh
VERSION=0.0.6 PREV=0.0.5 hack/release.sh    # PREV = the version this replaces
git commit -am "release v0.0.6" && git tag v0.0.6 && git push origin v0.0.6
```

`main` is branch-protected, so the digest-pinned release commit lands via a PR (the
tag already points at it); merge that PR so `main` carries its own release commit.
Roll a cluster with `kubectl apply -f operator/install/catalogsource.yaml`.

> A preview/rc and a release both `replace` the prior *released* version, so there's
> no OLM upgrade edge *between* previews (or preview→release). To move a cluster off a
> preview, delete its CSV and re-create the Subscription (same channel) — OLM then
> resolves to the catalog's current head.

## Submitting to OperatorHub / OpenShift OperatorHub

The community catalogs **build their own index** from the per-version bundle
(`operator/bundle/{manifests,metadata}`). Our FBC under `operator/catalog/` +
`operator/install/catalogsource.yaml` is for **self-hosting** and is *not* part of a
community submission.

Generate the submission tree from the committed, CI-tested bundle:

```sh
hack/community-bundle.sh 0.0.6 /tmp/dotvirt-submit
# -> /tmp/dotvirt-submit/operators/dotvirt-operator/0.0.6/{manifests,metadata}
#    /tmp/dotvirt-submit/operators/dotvirt-operator/ci.yaml
```

Then open **two** DCO-signed PRs (same bundle, separate repos):

| Catalog | Repo | Notes |
| --- | --- | --- |
| OperatorHub.io | [`k8s-operatorhub/community-operators`](https://github.com/k8s-operatorhub/community-operators) | Must work on vanilla Kubernetes. The Route/SCC RBAC rules are harmless there (rules for absent API groups are allowed); the reconciler auto-selects Ingress vs Route. `com.redhat.openshift.versions` is ignored. |
| OpenShift OperatorHub (Community tab) | [`redhat-openshift-ecosystem/community-operators-prod`](https://github.com/redhat-openshift-ecosystem/community-operators-prod) | Honors `com.redhat.openshift.versions: "v4.18"`. This repo is migrating to FBC fragments — re-check its required layout at submission time. |

Each repo's CI runs the same `operator-sdk bundle validate` the `bundle` target runs,
installs the bundle on a throwaway cluster, and runs scorecard. The `ci.yaml` lists
`reviewers` (for merge) and `updateGraph: replaces-mode` (our CSV uses `spec.replaces`,
not semver).

### Notes / future work

- **Multi-arch.** Images are `linux/amd64` only; the CSV advertises
  `operatorframework.io/arch.amd64` + `os.linux`. To support arm64 KubeVirt hosts,
  build multi-arch (`podman build --platform linux/amd64,linux/arm64` /
  `buildx` + `manifest`) in `hack/release.sh` and add the matching
  `operatorframework.io/arch.arm64` label to the CSV.
- **Channels.** `alpha` is the only *published* channel today (matching the
  `v1alpha1` API; `candidate` is internal QA — see Releasing). Add a `stable` channel
  when the API graduates.
