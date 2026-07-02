#!/usr/bin/env bash
# Cut a PREVIEW (release-candidate) into the `candidate-v0` channel only — for QA on a test
# cluster (hetznet subscribes to candidate-v0). It never touches `stable-v0`, so a preview is
# never a published release. THROWAWAY: it builds + pushes preview images and a preview catalog
# (digest-pinned), writes operator/install/catalogsource-preview.yaml, then restores the
# working tree (no committed change). Apply that CatalogSource to a candidate-v0 cluster
# to roll it to the rc.
#
#   VERSION=0.0.6-rc.1 hack/preview.sh
#
# The rc replaces the current released (stable-v0) head, so promotion is roll-forward:
# 0.0.5 -> 0.0.6-rc.1 (preview) -> 0.0.6 (`make release`).
set -euo pipefail
VERSION="${VERSION:?set VERSION=x.y.z-rc.N}"
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/lib.sh"

# Current released head (preserved unchanged in the preview catalog's stable-v0 channel).
REL_VER="$(current_stable_head)"
REL_BUNDLE="$(current_stable_bundle)"
[ -n "$REL_VER" ] && [ -n "$REL_BUNDLE" ] || { echo "could not read current release head from $TMPL"; exit 1; }
echo ">> preview v$VERSION into candidate-v0 (stable-v0 stays v$REL_VER)"

restore() { git checkout -- "$CSV" "$TMPL" operator/internal/install/dotvirt.go operator/config/manager/manager.yaml \
  operator/config/default/kustomization.yaml operator/bundle operator/bundle.Dockerfile operator/catalog 2>/dev/null || true; }
trap restore EXIT   # preview is throwaway — never leave committed files changed

echo ">> app  -> $REG/dotvirt:$SHA"
build_push Containerfile . "$REG/dotvirt:$SHA"
D_APP="$(digest "$REG/dotvirt:$SHA")"
repin "$REG/dotvirt" "$D_APP" operator/internal/install/dotvirt.go operator/config/manager/manager.yaml

echo ">> operator -> $REG/dotvirt-operator:v$VERSION"
build_push operator/Dockerfile . "$REG/dotvirt-operator:v$VERSION"
D_OP="$(digest "$REG/dotvirt-operator:v$VERSION")"
repin "$REG/dotvirt-operator" "$D_OP" "$CSV"
sed -i -E "s#(digest: )sha256:[0-9a-f]{64}#\1${D_OP}#" operator/config/default/kustomization.yaml

echo ">> bundle -> $REG/dotvirt-operator-bundle:v$VERSION"
make -C operator bundle VERSION="$VERSION" >/dev/null
make -C operator bundle-build bundle-push VERSION="$VERSION" CONTAINER_TOOL="$TOOL" >/dev/null
D_BUNDLE="$(digest "$REG/dotvirt-operator-bundle:v$VERSION")"

echo ">> catalog (stable-v0=v$REL_VER unchanged, candidate-v0=v$VERSION)"
cat > "$TMPL" <<YAML
schema: olm.template.basic
entries:
  - schema: olm.package
    name: dotvirt-operator
    defaultChannel: stable-v0
  - schema: olm.channel
    package: dotvirt-operator
    name: stable-v0
    entries:
      - name: dotvirt-operator.v$REL_VER
  - schema: olm.channel
    package: dotvirt-operator
    name: candidate-v0
    entries:
      - name: dotvirt-operator.v$VERSION
        replaces: dotvirt-operator.v$REL_VER
  - schema: olm.bundle
    image: $REL_BUNDLE
  - schema: olm.bundle
    image: $REG/dotvirt-operator-bundle@$D_BUNDLE
YAML
make -C operator catalog VERSION="$VERSION" >/dev/null
build_push operator/catalog.Dockerfile operator "$REG/dotvirt-operator-catalog:v$VERSION" --build-arg OPM_VERSION="$OPM_VERSION"
D_CAT="$(digest "$REG/dotvirt-operator-catalog:v$VERSION")"

cat > operator/install/catalogsource-preview.yaml <<YAML
# PREVIEW catalog (v$VERSION, candidate-v0 channel — stable-v0 stays v$REL_VER). Apply to a
# candidate-v0 cluster (hetznet) to QA the rc; NOT a release, throwaway. Re-cut
# with hack/preview.sh. This file is .gitignore'd.
apiVersion: operators.coreos.com/v1alpha1
kind: CatalogSource
metadata:
  name: dotvirt-catalog
  namespace: openshift-marketplace
spec:
  sourceType: grpc
  image: $REG/dotvirt-operator-catalog@$D_CAT
  displayName: dotvirt (preview v$VERSION)
  publisher: epheo
YAML

cat <<EOF

Preview v$VERSION published to candidate-v0 (stable-v0 untouched at v$REL_VER):
  app      $REG/dotvirt@$D_APP
  operator $REG/dotvirt-operator@$D_OP
  catalog  $REG/dotvirt-operator-catalog@$D_CAT

Roll a candidate-v0 cluster (e.g. hetznet) to it:
  kubectl apply -f operator/install/catalogsource-preview.yaml
(Working tree restored — nothing committed. Promote with: VERSION=<final> PREV=$REL_VER make release)
EOF
