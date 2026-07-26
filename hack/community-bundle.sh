#!/usr/bin/env bash
# Stage the committed OLM bundle as the two community-catalog submission trees:
#   <dest>/operatorhub/  k8s-operatorhub/community-operators                  (OperatorHub.io)
#   <dest>/openshift/    redhat-openshift-ecosystem/community-operators-prod  (OpenShift Community tab)
# Both ship the same bundle bytes CI tested; only the per-repo packaging differs.
#
# Two modes. After the first submission the OpenShift repo's semver.yaml is OWNED by the
# release bot (it appends each merged bundle to it), and ci.yaml may have been touched by an
# OCP-version promotion. Re-emitting the skeletons on an update would revert that accumulated
# state and drop older versions from the catalog. So:
#   --update (DEFAULT, every version after the first): stage ONLY the version dir
#            (manifests, metadata, tests, plus the OpenShift release-config.yaml). Leave
#            ci.yaml / semver.yaml / Makefile as the repo/bot has them.
#   --first  (bootstrap a NEW operator): also emit ci.yaml, the semver.yaml skeleton, and
#            the FBC Makefile.
# Update is the default because a forgotten --first fails loud (an incomplete PR the pipeline
# rejects), whereas the other way round would silently clobber the bot-owned semver.yaml.
#
# It does NOT fork, push, or open a PR. The two community PRs are a manual, DCO-signed step
# (see CONTRIBUTING.md):
#   - k8s-operatorhub/community-operators                  (every commit signed off)
#   - redhat-openshift-ecosystem/community-operators-prod  (squashed to ONE signed commit)
#
#   hack/community-bundle.sh 0.0.28 /tmp/dotvirt-submit            # update (default)
#   hack/community-bundle.sh --first 0.0.27 /tmp/dotvirt-submit    # first submission
set -euo pipefail

FIRST=false
while [ $# -gt 0 ]; do
	case "$1" in
	--first) FIRST=true; shift ;;
	--update) FIRST=false; shift ;;
	--*)
		echo "unknown flag: $1" >&2
		exit 1
		;;
	*) break ;;
	esac
done

VERSION="${1:?usage: community-bundle.sh [--first|--update] <version> <dest-dir>}"
DEST="${2:?usage: community-bundle.sh [--first|--update] <version> <dest-dir>}"
cd "$(dirname "$0")/.."

PKG=dotvirt-operator
BUNDLE=operator/bundle
CSV="$BUNDLE/manifests/$PKG.clusterserviceversion.yaml"

# The committed bundle's CSV must carry the version we're submitting; otherwise the tree
# would ship a stale bundle. Regenerate first (make bundle / hack/release.sh).
got="$(awk '/^  version: /{print $2; exit}' "$CSV")"
if [ "$got" != "$VERSION" ]; then
	echo "error: bundle CSV is version '$got', not '$VERSION'; run 'make -C operator bundle VERSION=$VERSION' (or hack/release.sh) first" >&2
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
if $FIRST; then
	# ci.yaml is repo-level + static; only the first submission establishes it. semver-mode:
	# their pipeline derives the upgrade edge from the CSV's spec.version (no spec.replaces).
	cat >"$DEST/operatorhub/operators/$PKG/ci.yaml" <<'EOF'
---
updateGraph: semver-mode
reviewers:
  - epheo
EOF
fi

# ---- OpenShift community: FBC-native ----
stage "$DEST/openshift"
# release-config.yaml is per-version and drives the release pipeline to append THIS bundle
# to the catalogs, so it ships for every version.
cat >"$DEST/openshift/operators/$PKG/$VERSION/release-config.yaml" <<'EOF'
---
catalog_templates:
  - template_name: semver.yaml
    channels: [Stable]
EOF
if $FIRST; then
	# Skeleton on purpose: after each merge the release bot appends the published bundle
	# (quay.io/community-operator-pipeline-prod/$PKG:<version>) here, driven by
	# release-config.yaml. Major channels only, so the generated channel is "stable-v0".
	mkdir -p "$DEST/openshift/operators/$PKG/catalog-templates"
	cat >"$DEST/openshift/operators/$PKG/catalog-templates/semver.yaml" <<'EOF'
---
Schema: olm.semver
GenerateMajorChannels: true
GenerateMinorChannels: false
EOF
	# Floor v4.18 = com.redhat.openshift.versions (CUDN GA); bump the ceiling to the newest
	# catalogs/vX.Y dir in community-operators-prod at submission time. review-needed keeps
	# their new-OCP-version catalog-promotion PRs gated on our review.
	cat >"$DEST/openshift/operators/$PKG/ci.yaml" <<'EOF'
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
	# The canonical per-operator Makefile their FBC tooling expects.
	curl -fsSL -o "$DEST/openshift/operators/$PKG/Makefile" \
		https://raw.githubusercontent.com/redhat-openshift-ecosystem/operator-pipelines/main/fbc/Makefile
fi

# Validate the staged bundle with the suite the community pipelines gate on (identical bytes
# in both trees, so validating one covers both).
if command -v operator-sdk >/dev/null 2>&1; then
	operator-sdk bundle validate "$DEST/operatorhub/operators/$PKG/$VERSION" --select-optional suite=operatorframework
else
	echo "note: operator-sdk not on PATH; skipped validation of the staged bundle" >&2
fi

mode=update
$FIRST && mode="first submission"
cat <<EOF

Staged $PKG v$VERSION ($mode) (LOCAL artifacts, no fork/push/PR):
  $DEST/operatorhub/operators/$PKG/   -> k8s-operatorhub/community-operators
  $DEST/openshift/operators/$PKG/     -> redhat-openshift-ecosystem/community-operators-prod
EOF
if $FIRST; then
	cat <<EOF

Copy the whole operators/$PKG/ into each DCO-signed branch and open a PR.
EOF
else
	cat <<EOF

Copy ONLY operators/$PKG/$VERSION/ into each DCO-signed branch. Do NOT re-add ci.yaml /
semver.yaml / Makefile: the release bot owns semver.yaml (re-adding the skeleton drops
older versions), and ci.yaml may carry OCP-version promotions.
EOF
fi
cat <<EOF

Optional full parity run of their static suite against the OpenShift tree:
  pip install git+https://github.com/redhat-openshift-ecosystem/operator-pipelines.git
  static-tests --repo-path $DEST/openshift --suites operatorcert.static_tests.community \\
    --output-file /tmp/static-tests.json --verbose $PKG $VERSION; cat /tmp/static-tests.json

See CONTRIBUTING.md for the per-repo PR conventions.
EOF
