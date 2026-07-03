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
--select-optional suite=operatorframework` + `opm validate`, and an on-cluster kind + OLM
install + scorecard. Run the equivalents locally before pushing.

## Sign your commits (DCO)

Every commit must carry a `Signed-off-by:` trailer matching the author (the
[Developer Certificate of Origin](https://developercertificate.org/)):

```sh
git commit -s -m "…"
```

This is enforced on the community-operator submissions (below) and expected here.

## Releasing

Two channels (OLM convention, `-v0` for the `v1alpha1` API): **`stable-v0`** is
released versions only (what external consumers subscribe to); **`candidate-v0`**
carries release-candidates *and* releases (the QA/test cluster subscribes here). All
tool versions (`operator-sdk`, `opm`,
`operator-manifest-tools`, …) are pinned in `hack/versions.env` — the single source
the Makefile, the `hack/` scripts, and the workflows all read — so local and CI builds
match.

**Preview (QA on a test cluster), throwaway — never a published release:**
`hack/preview.sh` builds + pushes preview images and a `candidate-v0`-only catalog, then
restores the working tree (nothing committed). Run it locally, or trigger the `preview`
workflow (`workflow_dispatch`, version input), which uploads the CatalogSource artifact.

```sh
VERSION=0.0.6-rc.1 hack/preview.sh
kubectl apply -f operator/install/catalogsource-preview.yaml   # roll a candidate-v0 cluster
```

**Release:** push a `v*` tag — the `release` workflow cuts a digest-pinned, multi-arch
(`linux/amd64` + `linux/arm64`) release: it builds and pushes the app, operator, bundle,
and catalog to `quay.io/epheo`, resolves each immutable `@sha256`, and pins them into
`DefaultImage` + the manager's `RELATED_IMAGE_*` env (from which `operator-manifest-tools`
assembles the bundle's `relatedImages` at `make bundle`), the operator Deployment, the
catalog template, and the `CatalogSource`. Never pushes `:latest`. `PREV` (the version
this replaces) is derived from the current `stable-v0` head.

```sh
git tag v0.0.6 && git push origin v0.0.6
```

`main` is branch-protected, so the run opens a **digest-pin PR** — the tag marks intent,
the merged PR is the record. Merge it so `main` carries the pinned release commit, then
roll a cluster with `kubectl apply -f operator/install/catalogsource.yaml`. To re-run or
override `PREV`, trigger the workflow via `workflow_dispatch` (version + prev inputs); to
cut one entirely locally, run `VERSION=0.0.6 PREV=0.0.5 hack/release.sh` and commit + tag
the pinned tree yourself.

> A preview/rc and a release both `replace` the prior *released* version, so there's
> no OLM upgrade edge *between* previews (or preview→release). To move a cluster off a
> preview, delete its CSV and re-create the Subscription (same channel) — OLM then
> resolves to the catalog's current head.

## Seamless upgrades

The CSV claims `capabilities: Seamless Upgrades` (operator capability **level 2**): a new
operator version installs over the old one *and* keeps managing the `Dotvirt` instance the
old one created, rolling the operand without the user touching the CR. Two things back this:

- **The operand rolls with the operator** — `TestOperandImageRollsWithOperatorUpgrade`
  (`operator/internal/install`) pins the render invariant: an unpinned CR takes the operand
  image from `RELATED_IMAGE_DOTVIRT` (which OLM sets on the manager from each new CSV) and
  falls back to the compiled-in, digest-pinned `DefaultImage` — so a newer operator
  re-reconciles an existing CR into a rolled operand, and an explicit `spec.image` still wins.
- **The OLM upgrade graph is valid** — `operator-sdk bundle validate` + `opm validate` (the
  `validate` job) check the CSV/channel/`replaces` wiring on every PR, and the `bundle-test`
  job installs the bundle on kind via OLM, running the PR's own operator image. Whether the
  *published* release (the digest-pinned catalog on quay) still installs is the
  `verify-published-bundle` workflow's subject — a daily canary that also gates each
  release cut and pin-recording PR.

To keep the claim honest as the API evolves, hold this discipline:

- **Additive-only within `v1alpha1`.** Never make an existing field required, change its
  type, or repurpose its meaning — an old CR must still validate and reconcile under the new
  operator. New fields are optional with a safe zero-value default.
- **The operand must roll on operator upgrade, not require a CR edit.** The app image is the
  operator's pinned `DefaultImage` (the CR leaves `spec.image` empty), so bumping the
  operator bumps the operand — keep it that way.
- **A second stored API version needs a conversion webhook.** The first time you introduce
  `v1beta1`/`v1` alongside `v1alpha1`, ship conversion so existing stored objects upgrade;
  only then bump the channel to `stable-v1` and migrate subscribers.

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
`reviewers` (for merge) and `updateGraph: semver-mode`: the community pipeline derives
the upgrade edge from the CSV's `spec.version`, so the **submitted bundle CSV carries no
`spec.replaces`** (a first submission has no prior version in their catalog to replace).
The `replaces` edge in our FBC (`operator/catalog-template.yaml`) is for **self-hosting
only** and is not part of a community submission.

### Notes / future work

- **Channels.** `stable-v0` is the *published* channel today; `candidate-v0` is
  internal QA (rc + releases — see Releasing). The `-v0` suffix tracks the `v1alpha1`
  API; add `stable-v1` (and migrate subscribers) when the API graduates.
