#!/usr/bin/env bash
# Cut a digest-pinned dotvirt OLM release. Builds + pushes the app, operator, bundle, and
# catalog to quay.io/epheo, resolves each image's immutable @sha256 digest, and pins it into
# the operand fallback (DefaultImage) + the RELATED_IMAGE_* env, the operator Deployment (via
# the kustomize digest), the catalog template, and the CatalogSource. The bundle's
# relatedImages is assembled from those env vars by operator-manifest-tools at `make bundle`.
# NEVER pushes a moving :latest — only the per-commit SHA (app) and the immutable :vVERSION tag.
#
#   VERSION=0.0.6 PREV=0.0.5 hack/release.sh        # PREV = the version this replaces
#
# Idempotent: re-running rebuilds and re-pins. Afterwards the working tree holds the
# digest-pinned files — commit them as the release commit and tag v$VERSION. The images are
# already published; `kubectl apply -f operator/install/catalogsource.yaml` rolls a cluster
# forward (OLM upgrades within the alpha channel).
set -euo pipefail
VERSION="${VERSION:?set VERSION=x.y.z}"
PREV="${PREV:?set PREV=x.y.z — the version $VERSION replaces}"
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

echo ">> [1/4] app  -> $REG/dotvirt:$SHA (+ :v$VERSION)"
build_push Containerfile . "$REG/dotvirt:$SHA" --build-arg VERSION="$VERSION"
skopeo copy --multi-arch all "docker://$REG/dotvirt:$SHA" "docker://$REG/dotvirt:v$VERSION"
D_APP="$(digest "$REG/dotvirt:$SHA")"; echo "   app digest: $D_APP"
repin "$REG/dotvirt" "$D_APP" operator/internal/install/dotvirt.go operator/config/manager/manager.yaml
sed -i -E "s#replaces: dotvirt-operator\.v[0-9.]+#replaces: dotvirt-operator.v$PREV#" "$CSV"

echo ">> [2/4] operator -> $REG/dotvirt-operator:v$VERSION"
build_push operator/Dockerfile . "$REG/dotvirt-operator:v$VERSION" --build-arg VERSION="$VERSION"
D_OP="$(digest "$REG/dotvirt-operator:v$VERSION")"; echo "   operator digest: $D_OP"
repin "$REG/dotvirt-operator" "$D_OP" "$CSV"
sed -i -E "s#(digest: )sha256:[0-9a-f]{64}#\1${D_OP}#" operator/config/default/kustomization.yaml
sed -i -E "s#(dotvirt-operator:)v[0-9.]+#\1v$VERSION#" operator/config/manager/manager.yaml

echo ">> [3/4] bundle -> $REG/dotvirt-operator-bundle:v$VERSION"
make -C operator bundle VERSION="$VERSION" >/dev/null
make -C operator bundle-build bundle-push VERSION="$VERSION" CONTAINER_TOOL="$TOOL" >/dev/null
D_BUNDLE="$(digest "$REG/dotvirt-operator-bundle:v$VERSION")"; echo "   bundle digest: $D_BUNDLE"

echo ">> [4/4] catalog -> $REG/dotvirt-operator-catalog:v$VERSION"
sed -i -E "s#(name: dotvirt-operator\.v)[0-9.]+#\1$VERSION#; s#(replaces: dotvirt-operator\.v)[0-9.]+#\1$PREV#" "$TMPL"
repin "$REG/dotvirt-operator-bundle" "$D_BUNDLE" "$TMPL"
make -C operator catalog VERSION="$VERSION" >/dev/null
build_push operator/catalog.Dockerfile operator "$REG/dotvirt-operator-catalog:v$VERSION" --build-arg OPM_VERSION="$OPM_VERSION"
D_CAT="$(digest "$REG/dotvirt-operator-catalog:v$VERSION")"; echo "   catalog digest: $D_CAT"
repin "$REG/dotvirt-operator-catalog" "$D_CAT" operator/install/catalogsource.yaml

echo ">> validate"
operator-sdk bundle validate ./operator/bundle --select-optional suite=operatorframework
$OPM validate operator/catalog

cat <<EOF

Release v$VERSION published (all digest-pinned, replaces v$PREV):
  app      $REG/dotvirt@$D_APP
  operator $REG/dotvirt-operator@$D_OP
  bundle   $REG/dotvirt-operator-bundle@$D_BUNDLE
  catalog  $REG/dotvirt-operator-catalog@$D_CAT

Commit the pinned files + tag, then roll a cluster:
  git commit -am "release v$VERSION" && git tag v$VERSION
  kubectl apply -f operator/install/catalogsource.yaml
EOF
