#!/usr/bin/env bash
# Wait for an OLM Subscription to resolve and its CSV to reach Succeeded, dumping
# cluster diagnostics on failure (the CI OLM install jobs share this assertion).
#
#   wait-csv.sh NAMESPACE SUBSCRIPTION [EXPECTED_CSV]
set -euo pipefail
ns="$1" sub="$2" expected="${3:-}"

diag() {
	echo "=== diagnostics ==="
	kubectl get catalogsources -A || true
	kubectl -n "$ns" get pods -o wide || true
	kubectl -n "$ns" describe subscription "$sub" || true
	kubectl -n olm get pods || true
	kubectl -n olm logs deploy/catalog-operator --tail 100 || true
}

csv=""
for _ in $(seq 60); do
	csv="$(kubectl -n "$ns" get subscription "$sub" -o jsonpath='{.status.installedCSV}' 2>/dev/null || true)"
	[ -n "$csv" ] && break
	sleep 10
done
if [ -z "$csv" ]; then
	diag
	echo "::error::subscription $ns/$sub never resolved to a CSV — catalog unreachable or not serving" >&2
	exit 1
fi
if [ -n "$expected" ] && [ "$csv" != "$expected" ]; then
	echo "::error::subscription $ns/$sub resolved to $csv, expected $expected" >&2
	exit 1
fi
if ! kubectl -n "$ns" wait "csv/$csv" --for=jsonpath='{.status.phase}'=Succeeded --timeout=10m; then
	diag
	kubectl -n "$ns" describe "csv/$csv" || true
	echo "::error::CSV $csv did not reach Succeeded" >&2
	exit 1
fi
kubectl -n "$ns" get csv
