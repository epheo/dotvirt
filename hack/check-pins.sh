#!/usr/bin/env bash
# Fails when a pinned third-party operand image is behind upstream: its tag now
# names another digest (a patch release), or a newer major exists. Dependabot
# reads only Dockerfiles, so nothing else watches these pins.
set -euo pipefail
cd "$(dirname "$0")/.."

rc=0

# check ENV RELEASES_API: ENV is the RELATED_IMAGE_* the manager manifest pins,
# RELEASES_API a Gitea-style releases endpoint whose tags are vMAJOR.MINOR.PATCH.
check() {
	local env=$1 api=$2 pin ref want tag have major latest
	pin=$(awk -v n="name: $env" '$0 ~ n { getline; print $2; exit }' operator/config/manager/manager.yaml)
	ref=${pin%@*} want=${pin#*@sha256:} tag=${ref##*:}
	have=$(skopeo inspect --retry-times 3 --raw "docker://$ref" | sha256sum | cut -d' ' -f1)
	if [ "$have" != "$want" ]; then
		echo "$env: $ref is now sha256:$have, pinned sha256:$want"
		rc=1
	fi
	major=${tag%%[.-]*}
	latest=$(curl -fsS --retry 3 "$api?limit=20" |
		jq -r '[.[] | select((.prerelease or .draft) | not) | .tag_name | ltrimstr("v") | split(".")[0] | tonumber] | max')
	if [ "$latest" -gt "$major" ]; then
		echo "$env: major $latest released, pinned $tag"
		rc=1
	fi
}

check RELATED_IMAGE_FORGEJO https://codeberg.org/api/v1/repos/forgejo/forgejo/releases

[ "$rc" -eq 0 ] && echo "pins current"
exit "$rc"
