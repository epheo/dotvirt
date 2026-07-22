#!/usr/bin/env bash
# Stage the committed OLM bundle as the two community-catalog submission trees:
#   <dest>/operatorhub/  k8s-operatorhub/community-operators                  (OperatorHub.io)
#   <dest>/openshift/    redhat-openshift-ecosystem/community-operators-prod  (OpenShift Community tab)
# Both ship the same bundle bytes CI tested; only the per-repo packaging differs.
# The OpenShift repo is FBC-native: ci.yaml declares the catalog mapping, the semver
# template starts as a skeleton, and release-config.yaml makes their release pipeline
# append each merged bundle to the catalogs itself (single-PR releases).
#
# It does NOT fork, push, or open a PR. The two community PRs are a manual, DCO-signed
# step (see CONTRIBUTING.md):
#   - k8s-operatorhub/community-operators                  (every commit signed off)
#   - redhat-openshift-ecosystem/community-operators-prod  (squashed to ONE signed commit)
#
#   hack/community-bundle.sh 0.0.26 /tmp/dotvirt-submit
set -euo pipefail

VERSION="${1:?usage: community-bundle.sh <version> <dest-dir>}"
DEST="${2:?usage: community-bundle.sh <version> <dest-dir>}"
cd "$(dirname "$0")/.."

PKG=dotvirt-operator
BUNDLE=operator/bundle
CSV="$BUNDLE/manifests/$PKG.clusterserviceversion.yaml"

# The committed bundle's CSV must carry the version we're submitting — otherwise the
# tree would ship a stale bundle. Regenerate first (make bundle / hack/release.sh).
got="$(awk '/^  version: /{print $2; exit}' "$CSV")"
if [ "$got" != "$VERSION" ]; then
	echo "error: bundle CSV is version '$got', not '$VERSION' — run 'make -C operator bundle VERSION=$VERSION' (or hack/release.sh) first" >&2
	exit 1
fi

stage() {
	local out="$1/operators/$PKG/$VERSION"
	rm -rf "$out"
	mkdir -p "$out"
	cp -r "$BUNDLE/manifests" "$out/manifests"
	cp -r "$BUNDLE/metadata" "$out/metadata"
	[ -d "$BUNDLE/tests" ] && cp -r "$BUNDLE/tests" "$out/tests"
}

# ---- OperatorHub.io: bundle dirs only (that repo has no FBC mode) ----
stage "$DEST/operatorhub"
# semver-mode: their pipeline derives the upgrade edge from the CSV's spec.version, so
# the submitted bundle CSV carries NO spec.replaces (a first submission has no prior
# version in their catalog to replace anyway). The replaces edges in our FBC
# (operator/catalog-template.yaml) are for SELF-HOSTING only.
cat > "$DEST/operatorhub/operators/$PKG/ci.yaml" <<'EOF'
---
updateGraph: semver-mode
reviewers:
  - epheo
EOF

# ---- OpenShift community: FBC-native ----
stage "$DEST/openshift"
# Skeleton on purpose: after each PR merges, their release pipeline republishes the
# bundle as quay.io/community-operator-pipeline-prod/dotvirt-operator:<version> and a
# bot PR appends it here, driven by the bundle's release-config.yaml. Major channels
# only, so the generated channel is "stable-v0" — the same name as the self-hosted
# catalog and the bundle's channel annotation.
mkdir -p "$DEST/openshift/operators/$PKG/catalog-templates"
cat > "$DEST/openshift/operators/$PKG/catalog-templates/semver.yaml" <<'EOF'
---
Schema: olm.semver
GenerateMajorChannels: true
GenerateMinorChannels: false
EOF
# Floor v4.18 = com.redhat.openshift.versions (CUDN GA); bump the ceiling to the newest
# catalogs/vX.Y dir in community-operators-prod at submission time. review-needed keeps
# their new-OCP-version catalog-promotion PRs gated on our review.
cat > "$DEST/openshift/operators/$PKG/ci.yaml" <<'EOF'
---
reviewers:
  - epheo
fbc:
  enabled: true
  version_promotion_strategy: review-needed
  catalog_mapping:
    - template_name: semver.yaml
      type: olm.semver
      catalog_names:
        - v4.18
        - v4.19
        - v4.20
        - v4.21
        - v4.22
EOF
cat > "$DEST/openshift/operators/$PKG/$VERSION/release-config.yaml" <<'EOF'
---
catalog_templates:
  - template_name: semver.yaml
    channels: [Stable]
EOF
# The canonical per-operator Makefile their FBC tooling expects (renders + validates
# the catalogs from the templates via fbc/render_catalogs.sh).
curl -fsSL -o "$DEST/openshift/operators/$PKG/Makefile" \
	https://raw.githubusercontent.com/redhat-openshift-ecosystem/operator-pipelines/main/fbc/Makefile

# Validate the staged bundle with the suite the community pipelines gate on (identical
# bytes in both trees, so validating one covers both).
if command -v operator-sdk >/dev/null 2>&1; then
	operator-sdk bundle validate "$DEST/operatorhub/operators/$PKG/$VERSION" --select-optional suite=operatorframework
else
	echo "note: operator-sdk not on PATH — skipped validation of the staged bundle" >&2
fi

cat <<EOF

Staged $PKG v$VERSION (LOCAL artifacts — no fork/push/PR):
  $DEST/operatorhub/operators/$PKG/   -> k8s-operatorhub/community-operators
  $DEST/openshift/operators/$PKG/     -> redhat-openshift-ecosystem/community-operators-prod

Optional full parity run of their static suite against the OpenShift tree:
  pip install git+https://github.com/redhat-openshift-ecosystem/operator-pipelines.git
  static-tests --repo-path $DEST/openshift --suites operatorcert.static_tests.community \\
    --output-file /tmp/static-tests.json --verbose $PKG $VERSION; cat /tmp/static-tests.json

To submit, copy each operators/$PKG/ into a DCO-signed branch of the matching community
repo and open a PR (see CONTRIBUTING.md).
EOF
