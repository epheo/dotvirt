#!/usr/bin/env bash
# Sign each image listed in $1 (one <name>@sha256:<hex> line per image) with sigstore and
# verify it, failing on any image that does not verify. Run by the release, preview and
# image workflows right after they push; each image is signed by digest, never by tag.
#
# Keyless, always: Fulcio certifies this workflow run's GitHub OIDC identity for minutes
# and Rekor records the signature, so no long-lived key exists to leak or rotate. The
# certificate names the workflow file at the ref it ran from, for example
#   https://github.com/epheo/dotvirt/.github/workflows/release.yaml@refs/tags/v0.2.4
# OpenShift's ClusterImagePolicy matches a keyless certificate by email only, which a
# workflow certificate never carries, so enforcing on OpenShift needs a second signature
# by a key: COSIGN_PRIVATE_KEY (with COSIGN_PASSWORD), whose public half is committed as
# operator/install/cosign.pub. Both or neither: one without the other fails here.
#
# Legacy signature layout (a sha256-<hex>.sig tag beside the image): containers/image, and
# so CRI-O and OpenShift, reads only that layout, not cosign 3's bundle referrers.
# Idempotent: an image that already verifies is not signed again.
set -euo pipefail
refs="${1:?usage: sign-images.sh <file with one name@sha256:digest per line>}"
cd "$(dirname "${BASH_SOURCE[0]}")/.."

identity="${GITHUB_SERVER_URL:-https://github.com}/${GITHUB_WORKFLOW_REF:?run inside a GitHub workflow}"
issuer=https://token.actions.githubusercontent.com
pub=operator/install/cosign.pub
legacy=(--new-bundle-format=false)
sign=(sign --yes --recursive --use-signing-config=false "${legacy[@]}")

keyed=
if [ -n "${COSIGN_PRIVATE_KEY:-}" ] || [ -f "$pub" ]; then
	[ -n "${COSIGN_PRIVATE_KEY:-}" ] && [ -f "$pub" ] ||
		{ echo "::error::the signing key and $pub come together: set both or neither"; exit 1; }
	keyed=1
fi

# keyless_ok / key_ok <ref>: the image carries a signature this run's identity, or the
# committed key, accounts for. A key that is not $pub's half fails key_ok after signing.
keyless_ok() { cosign verify "${legacy[@]}" --certificate-identity "$identity" --certificate-oidc-issuer "$issuer" "$1" >/dev/null 2>&1; }
key_ok() { cosign verify "${legacy[@]}" --key "$pub" "$1" >/dev/null 2>&1; }

[ -s "$refs" ] || { echo "::error::$refs lists no image to sign"; exit 1; }
while read -r ref; do
	[ -n "$ref" ] || continue
	[[ "$ref" =~ @sha256:[0-9a-f]{64}$ ]] || { echo "::error::not a digest reference: $ref"; exit 1; }
	keyless_ok "$ref" || cosign "${sign[@]}" "$ref"
	keyless_ok "$ref" || { echo "::error::$ref does not verify as signed by $identity"; exit 1; }
	if [ -n "$keyed" ]; then
		key_ok "$ref" || cosign "${sign[@]}" --key env://COSIGN_PRIVATE_KEY "$ref"
		key_ok "$ref" || { echo "::error::$ref does not verify against $pub"; exit 1; }
	fi
	echo "signed $ref${keyed:+ (keyless and $pub)}"
done <"$refs"
