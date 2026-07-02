# Shared helpers for the release + preview orchestrators (hack/release.sh, hack/preview.sh).
# SOURCED, not executed. Holds the parts that are byte-identical between the two flows:
# image build/push, digest resolution + pinning, and the stable-channel head extraction.
# Each orchestrator keeps only its divergent tail (channel handling, commit-vs-restore,
# validation, manager.yaml update). Sourcing this also cds to the repo root and pins the
# tool versions, so the orchestrators stay thin.
LIB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "$LIB_DIR/versions.env"
cd "$LIB_DIR/.."   # repo root — every path below is root-relative

REG="${REG:-quay.io/epheo}"                  # images live under quay.io/epheo
TOOL="${CONTAINER_TOOL:-podman}"
PLATFORMS="${PLATFORMS:-linux/amd64,linux/arm64}"   # multi-arch: operands run on amd64 + arm64 nodes
OPM="${OPM:-go run github.com/operator-framework/operator-registry/cmd/opm@$OPM_VERSION}"
SHA="$(git rev-parse --short HEAD)"
CSV=operator/config/manifests/bases/dotvirt-operator.clusterserviceversion.yaml
TMPL=operator/catalog-template.yaml

# digest <image-ref> -> sha256:<hex>   the immutable digest of a pushed tag (a manifest
# list, once multi-arch — which is exactly what we want to pin).
digest() { skopeo inspect --format '{{.Digest}}' "docker://$1"; }

# repin <image-name-without-tag> <sha256:digest> <file>...   swap a pinned digest in place.
# Anchored on '@' so .../dotvirt never matches .../dotvirt-operator.
repin() { local ref="$1" d="$2"; shift 2; sed -i -E "s#${ref}@sha256:[0-9a-f]{64}#${ref}@${d}#g" "$@"; }

# build_push <dockerfile> <context> <tag> [build args...]   build a multi-arch image
# ($PLATFORMS) and push it as a manifest list. Bridges docker (buildx builds+pushes in one
# step) and podman (build a --manifest, then push it). Extra args (e.g. --build-arg VERSION=)
# pass through. digest() then resolves the manifest-list digest — exactly what we pin.
build_push() {
	local file="$1" ctx="$2" tag="$3"; shift 3
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

# current_stable_head -> the released version at the head of the stable-v0 channel (e.g. 0.0.6).
# The catalog template is the single source for the channel graph; both preview (stable stays
# put) and the release tag-path (PREV = what this release replaces) read the head from here.
# stable-v0 is listed first in the template, so the first match is the released head.
current_stable_head() { grep -m1 -oE 'dotvirt-operator\.v[0-9][^ ]*' "$TMPL" | sed 's/dotvirt-operator\.v//'; }
# current_stable_bundle -> the stable head's pinned bundle image (preserved in the preview catalog).
current_stable_bundle() { grep -m1 -oE "$REG/dotvirt-operator-bundle@sha256:[0-9a-f]{64}" "$TMPL"; }
