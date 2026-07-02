#!/usr/bin/env bash
# Stage the committed OLM bundle as a community-operators submission tree: copies
# operator/bundle/{manifests,metadata,tests} into
# <dest>/operators/dotvirt-operator/<version>/ and writes the package ci.yaml, so the
# submitted artifact is mechanically identical to the CI-tested bundle.
#
# It does NOT fork, push, or open a PR. The two community PRs are a manual, DCO-signed
# step (see CONTRIBUTING.md):
#   - k8s-operatorhub/community-operators                  (OperatorHub.io)
#   - redhat-openshift-ecosystem/community-operators-prod  (OpenShift Community tab)
#
#   hack/community-bundle.sh 0.0.6 /tmp/dotvirt-submit
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

OUT="$DEST/operators/$PKG/$VERSION"
rm -rf "$OUT"
mkdir -p "$OUT"
cp -r "$BUNDLE/manifests" "$OUT/manifests"
cp -r "$BUNDLE/metadata" "$OUT/metadata"
[ -d "$BUNDLE/tests" ] && cp -r "$BUNDLE/tests" "$OUT/tests"

# Package-level ci.yaml: semver-mode + the reviewer who can approve the submission PR
# in the community repos. semver-mode lets the community pipeline derive the upgrade
# edge from the CSV's spec.version, so the submitted bundle CSV carries NO spec.replaces
# (a first submission has no prior version in their catalog to replace anyway). The
# replaces edge in our FBC (operator/catalog-template.yaml) is for SELF-HOSTING only and
# is not part of a community submission.
cat > "$DEST/operators/$PKG/ci.yaml" <<'EOF'
---
updateGraph: semver-mode
reviewers:
  - epheo
EOF

# Validate the staged copy with the same suites the community CI gates on.
if command -v operator-sdk >/dev/null 2>&1; then
	operator-sdk bundle validate "$OUT" --select-optional suite=operatorframework
else
	echo "note: operator-sdk not on PATH — skipped validation of the staged bundle" >&2
fi

cat <<EOF

Staged $PKG v$VERSION (LOCAL artifact — no fork/push/PR):
  $OUT/{manifests,metadata,tests}
  $DEST/operators/$PKG/ci.yaml

To submit, copy operators/$PKG/ into a DCO-signed branch of each community repo
(see CONTRIBUTING.md) and open a PR.
EOF
