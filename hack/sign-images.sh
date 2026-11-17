#!/usr/bin/env bash
# Sign each image listed in $1 (one <name>@sha256:<hex> line per image) with sigstore and
# verify it, failing on any image that does not verify. $2, when given, is the number of
# images the caller pushed: a new image nobody records for signing fails here instead of
# shipping unsigned. Run by the release, preview and image workflows in a job of its own,
# after the push; each image is signed by digest, never by tag.
#
# Keyless, always: Fulcio certifies this workflow run's GitHub OIDC identity for minutes
# and Rekor records the signature, so no long-lived key exists to leak or rotate. The
# certificate names the workflow file at the ref it ran from, for example
#   https://github.com/epheo/dotvirt/.github/workflows/release.yaml@refs/tags/v0.2.4
# OpenShift's ClusterImagePolicy matches a keyless certificate by email only, which a
# workflow certificate never carries, so enforcing on OpenShift needs a second signature
# by the key whose public half this ref commits as operator/install/cosign.pub. The
# private half is COSIGN_PRIVATE_KEY (with COSIGN_PASSWORD); a ref without cosign.pub
# signs keyless only.
#
# Legacy signature layout (a sha256-<hex>.sig tag beside the image): containers/image, and
# so CRI-O and OpenShift, reads only that layout, not cosign 3's bundle referrers.
#
# cosign signs a multi-arch index before its platform manifests, and a runtime checks the
# platform manifest it pulls, so both the skip decision and the final check cover the
# index and every platform manifest: a run that died halfway signs the rest on re-run.
set -euo pipefail
refs="$(realpath "${1:?usage: sign-images.sh <file with one name@sha256:digest per line> [image count]}")"
want="${2:-}"
cd "$(dirname "${BASH_SOURCE[0]}")/.."

identity="${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_WORKFLOW_REF:?run inside a GitHub workflow}"
pub=operator/install/cosign.pub
legacy=(--new-bundle-format=false)
sign=(sign --yes --recursive --use-signing-config=false "${legacy[@]}")
keyless=(--certificate-identity "$identity" --certificate-oidc-issuer https://token.actions.githubusercontent.com)
bykey=(--key "$pub")

keyed=
if [ -f "$pub" ]; then
	[ -n "${COSIGN_PRIVATE_KEY:-}" ] ||
		{ echo "::error::this ref commits $pub, so its images must also be signed by that key: COSIGN_PRIVATE_KEY is unset"; exit 1; }
	keyed=1
elif [ -n "${COSIGN_PRIVATE_KEY:-}" ]; then
	echo "::warning::COSIGN_PRIVATE_KEY is set but this ref commits no $pub: signing keyless only"
fi

# instances <ref> -> the index and each platform manifest it lists. Attestation manifests
# (platform os "unknown") are never pulled by a runtime and are left out.
instances() {
	echo "$1"
	skopeo inspect --raw "docker://$1" |
		jq -r '.manifests[]? | select(.platform.os != "unknown") | .digest' |
		sed "s#^#${1%@*}@#"
}

# signed <cosign verify args...>: every one of $inst verifies (silent).
signed() {
	local i
	for i in $inst; do
		cosign verify "${legacy[@]}" "$@" "$i" >/dev/null 2>&1 || return 1
	done
}

# verify <cosign verify args...>: as signed, failing with cosign's own reason.
verify() {
	local i
	for i in $inst; do
		cosign verify "${legacy[@]}" "$@" "$i" >/dev/null || { echo "::error::$i does not verify"; return 1; }
	done
}

[ -s "$refs" ] || { echo "::error::$refs lists no image to sign"; exit 1; }
n="$(grep -c . "$refs")"
[ -z "$want" ] || [ "$n" = "$want" ] ||
	{ echo "::error::$refs lists $n images but $want were pushed: record each pushed image (hack/lib.sh published)"; exit 1; }

while read -r ref <&3; do
	[ -n "$ref" ] || continue
	[[ "$ref" =~ @sha256:[0-9a-f]{64}$ ]] || { echo "::error::not a digest reference: $ref"; exit 1; }
	inst="$(instances "$ref")" # a failed listing stops here, never shrinks the check to the index
	signed "${keyless[@]}" || cosign "${sign[@]}" "$ref"
	verify "${keyless[@]}"
	if [ -n "$keyed" ]; then
		signed "${bykey[@]}" || cosign "${sign[@]}" --key env://COSIGN_PRIVATE_KEY "$ref"
		verify "${bykey[@]}"
	fi
	echo "signed $ref and its platform manifests${keyed:+ (keyless and $pub)}"
done 3<"$refs"
