# Shared helpers for the release + preview orchestrators (hack/release.sh, hack/preview.sh).
# SOURCED, not executed. Holds the parts that are byte-identical between the two flows:
# image build/push (reusing what an earlier run of the version pushed), digest resolution +
# pinning, the channel entry shape, and the stable-channel head extraction.
# Each orchestrator keeps only its divergent tail (channel handling, commit-vs-restore,
# validation, manager.yaml update). Sourcing this also cds to the repo root and pins the
# tool versions, so the orchestrators stay thin.
LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$LIB_DIR/versions.env"
cd "$LIB_DIR/.."   # repo root — every path below is root-relative

REG="${REG:-quay.io/epheo}"                  # images live under quay.io/epheo
TOOL="${CONTAINER_TOOL:-podman}"
PLATFORMS="${PLATFORMS:-linux/amd64,linux/arm64}"   # multi-arch: operands run on amd64 + arm64 nodes
# Without this tag opm links gpgme through cgo, absent on CI runners.
OPM="${OPM:-go run -tags containers_image_openpgp github.com/operator-framework/operator-registry/cmd/opm@$OPM_VERSION}"
CSV=operator/config/manifests/bases/dotvirt-operator.clusterserviceversion.yaml
TMPL=operator/catalog-template.yaml

# digest <image-ref> -> sha256:<hex>   the immutable digest of a pushed tag (a manifest
# list, once multi-arch — which is exactly what we want to pin).
digest() { skopeo inspect --format '{{.Digest}}' "docker://$1"; }

# repin <image-name-without-tag> <sha256:digest> <file>...   swap a pinned digest in place.
# Anchored on '@' so .../dotvirt never matches .../dotvirt-operator.
repin() { local ref="$1" d="$2"; shift 2; sed -i -E "s#${ref}@sha256:[0-9a-f]{64}#${ref}@${d}#g" "$@"; }

# pushed <ref> -> succeeds when <ref> is on the registry. Any error but "manifest unknown"
# aborts: reading it as absent would rebuild, moving a tag something already pins.
pushed() {
	local err
	err="$(skopeo inspect --raw "docker://$1" 2>&1 >/dev/null)" && return 0
	grep -q 'manifest unknown' <<<"$err" && return 1
	echo "$err" >&2
	exit 1
}

# reuse <tag> -> succeeds when this run keeps <tag> where an earlier run of this version
# pushed it. Rebuilding would move the tag, and quay prunes the digest that run already
# pinned into its pin PR, its draft Release, and any cluster that applied them. Every later
# image pins this one, so once one is rebuilt FORCE=1 rebuilds the rest.
reuse() {
	if [ "${FORCE:-}" != 1 ] && pushed "$1"; then
		echo "   reusing $1 (FORCE=1 rebuilds)"
		return 0
	fi
	FORCE=1
	return 1
}

# build_push <dockerfile> <context> <tag> [build args...]   build a multi-arch image
# ($PLATFORMS) and push it as a manifest list, unless reuse keeps the pushed tag. Bridges
# docker (buildx builds+pushes in one step) and podman (build a --manifest, then push it).
# Extra args (e.g. --build-arg VERSION=) pass through. digest() then resolves the
# manifest-list digest, which is what we pin.
build_push() {
	local file="$1" ctx="$2" tag="$3"; shift 3
	reuse "$tag" && return 0
	case "$TOOL" in
	docker)
		docker buildx build --platform "$PLATFORMS" "$@" -f "$file" -t "$tag" --push "$ctx"
		;;
	*)
		$TOOL manifest rm "$tag" 2>/dev/null || true
		$TOOL build --platform "$PLATFORMS" --manifest "$tag" "$@" -f "$file" "$ctx"
		$TOOL manifest push --all "$tag" "docker://$tag"
		;;
	esac
}

# push_bundle -> regenerate operator/bundle at $VERSION and push it as :v$VERSION. A reused
# tag keeps the earlier run's bundle; the regenerated tree differs from it only in createdAt.
push_bundle() {
	make -C operator bundle VERSION="$VERSION" >/dev/null
	reuse "$REG/dotvirt-operator-bundle:v$VERSION" ||
		make -C operator bundle-build bundle-push VERSION="$VERSION" CONTAINER_TOOL="$TOOL" >/dev/null
}

# push_catalog -> render $TMPL into operator/catalog and push it as :v$VERSION. A pushed tag
# is reused only when it serves this exact render: a re-run with another PREV renders a
# different catalog, and shipping the earlier one would contradict the committed pins.
push_catalog() {
	local tag="$REG/dotvirt-operator-catalog:v$VERSION"
	make -C operator catalog VERSION="$VERSION" >/dev/null
	if [ "${FORCE:-}" != 1 ] && pushed "$tag" &&
		$OPM render "$tag" -o yaml 2>/dev/null | cmp -s - operator/catalog/dotvirt-operator/catalog.yaml; then
		echo "   reusing $tag (serves this exact catalog)"
		return 0
	fi
	FORCE=1
	build_push operator/catalog.Dockerfile operator "$tag" --build-arg OPM_VERSION="$OPM_VERSION"
}

# entry <version> [replaces] -> one olm.channel entry for $TMPL. The catalog keeps only the
# channel heads, and replaces is an edge from the one version it names, so alone it strands
# every other install: an older release, an rc (each rc replaces the release, not the
# previous rc), or the release whose pin PR had not merged when the next tag was cut.
# skipRange is the edge OLM follows from any older installed CSV, in the catalog or not.
entry() {
	echo "      - name: dotvirt-operator.v$1"
	[ -z "${2:-}" ] || echo "        replaces: dotvirt-operator.v$2"
	echo "        skipRange: '>=0.0.0 <$1'"
}

# current_stable_head -> the released version at the head of the stable-v0 channel (e.g. 0.0.6).
# The catalog template is the single source for the channel graph; both preview (stable stays
# put) and the release tag-path (PREV = what this release replaces) read the head from here.
# stable-v0 is listed first in the template, so the first match is the released head.
current_stable_head() { grep -m1 -oE 'dotvirt-operator\.v[0-9][^ ]*' "$TMPL" | sed 's/dotvirt-operator\.v//'; }
# current_stable_bundle -> the stable head's pinned bundle image (preserved in the preview catalog).
current_stable_bundle() { grep -m1 -oE "$REG/dotvirt-operator-bundle@sha256:[0-9a-f]{64}" "$TMPL"; }
