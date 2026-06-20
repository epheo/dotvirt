#!/usr/bin/env bash
# e2e-roundtrip.sh — measure dotvirt's GitOps round-trip latency end to end.
#
# Stages a throwaway VM through the real pipeline and times each hop of the
# deterministic propagation the event-bus refactor optimizes:
#   stage-create -> propose (PR) -> merge -> ArgoCD sync -> reflector -> inventory
# then the symmetric delete. Self-cleaning: the VM is removed at the end.
#
# Auth: the dotvirt API runs under your `oc` token; the PR merge ("accept in
# Forgejo") uses dotvirt's own bot token from the dotvirt-forge secret. In
# production a human merges (the GitOps gate) — this automates it for measurement.
#
#   BASE=https://dotvirt.apps.hetznet.epheo.eu PROJECT=team-a hack/e2e-roundtrip.sh
set -euo pipefail

BASE="${BASE:-https://dotvirt.apps.hetznet.epheo.eu}"
FORGE="${FORGE:-https://forgejo.apps.hetznet.epheo.eu}"
PROJECT="${PROJECT:-team-a}"; NS="${NS:-$PROJECT}"
OWNER="${OWNER:-dotvirt}"; REPO="${REPO:-$PROJECT}"
VM="${VM:-e2e-rt-$(date +%s)}"
TIMEOUT="${TIMEOUT:-240}" # per-wait ceiling, seconds

TOK="$(oc whoami -t)"
FTOK="$(kubectl get secret dotvirt-forge -n dotvirt -o jsonpath='{.data.token}' | base64 -d | tr -d '[:space:]')"

api()   { curl -ksS -H "Authorization: Bearer $TOK" "$@"; }
forge() { curl -ksS -H "Authorization: token $FTOK" -H 'Content-Type: application/json' "$@"; }
now()   { date +%s.%N; }
el()    { awk -v s="$1" -v e="$2" 'BEGIN{printf "%.1f", e-s}'; } # elapsed seconds

# wait_for <label> <predicate-fn> : poll until the predicate succeeds; echo elapsed.
wait_for() {
  local label="$1" fn="$2" start; start=$(now)
  while :; do
    if "$fn"; then el "$start" "$(now)"; return 0; fi
    awk -v s="$start" -v e="$(now)" -v to="$TIMEOUT" 'BEGIN{exit (e-s>to)?0:1}' &&
      { echo "TIMEOUT"; echo "  !! timed out waiting for $label" >&2; return 0; }
    sleep 1
  done
}

vm_in_cluster()  { kubectl get vm "$VM" -n "$NS" >/dev/null 2>&1; }
vm_gone_cluster(){ ! kubectl get vm "$VM" -n "$NS" >/dev/null 2>&1; }
inv_q()          { api --max-time 20 "$BASE/api/inventory" | jq -e --arg ns "$NS" --arg n "$VM" "$1" >/dev/null 2>&1; }
vm_in_inv()      { inv_q '[.projects[].namespaces[]?.vms[]?|select(.namespace==$ns and .name==$n)]|length>0'; }
vm_synced_inv()  { inv_q '[.projects[].namespaces[]?.vms[]?|select(.namespace==$ns and .name==$n and .sync=="Synced")]|length>0'; }
vm_gone_inv()    { ! vm_in_inv; }

# merge_pr <num> : merge, retrying while Forgejo finishes computing mergeability.
merge_pr() {
  local pr="$1" i code
  for i in $(seq 1 20); do
    code=$(forge -o /dev/null -w '%{http_code}' --max-time 25 \
      -X POST "$FORGE/api/v1/repos/$OWNER/$REPO/pulls/$pr/merge" -d '{"Do":"merge"}')
    [ "$code" = "200" ] && return 0
    sleep 2
  done
  echo "  !! merge of PR #$pr failed (last HTTP $code)" >&2; return 1
}

propose() { # <title> -> echoes the PR number
  api --max-time 30 -X POST "$BASE/api/draft/propose?project=$PROJECT" \
    -H 'Content-Type: application/json' -d "{\"title\":\"$1\",\"message\":\"e2e round-trip\"}" |
    jq -r '.prNumber'
}

printf '\n── dotvirt round-trip: %s/%s ──\n' "$NS" "$VM"

# ============================ CREATE ============================
t0=$(now)
api --max-time 30 -X POST "$BASE/api/vms" -H 'Content-Type: application/json' -d "{
  \"name\":\"$VM\",\"namespace\":\"$NS\",\"instancetype\":\"u1.medium\",\"preference\":\"fedora\",
  \"osImage\":{\"name\":\"fedora\",\"namespace\":\"openshift-virtualization-os-images\"},
  \"diskSize\":\"10Gi\",\"running\":false}" >/dev/null
PR=$(propose "e2e: create $VM")
printf '  create → PR #%s          %ss\n' "$PR" "$(el "$t0" "$(now)")"
tm=$(now); merge_pr "$PR"
printf '  merge PR #%s             %ss\n' "$PR" "$(el "$tm" "$(now)")"
printf '  merge → VM in cluster    %ss\n' "$(wait_for 'VM in cluster' vm_in_cluster)"
printf '  merge → VM in inventory  %ss\n' "$(wait_for 'VM in inventory' vm_in_inv)"
printf '  merge → VM Synced        %ss\n' "$(wait_for 'VM Synced' vm_synced_inv)"
printf '  TOTAL create → Synced    %ss\n' "$(el "$t0" "$(now)")"

# ============================ DELETE ============================
t1=$(now)
api --max-time 30 -X POST "$BASE/api/vms/$NS/$VM/delete" >/dev/null
PRD=$(propose "e2e: delete $VM")
printf '\n  delete → PR #%s          %ss\n' "$PRD" "$(el "$t1" "$(now)")"
tmd=$(now); merge_pr "$PRD"
printf '  merge PR #%s             %ss\n' "$PRD" "$(el "$tmd" "$(now)")"
printf '  merge → gone from cluster   %ss\n' "$(wait_for 'VM gone (cluster)' vm_gone_cluster)"
printf '  merge → gone from inventory %ss\n' "$(wait_for 'VM gone (inventory)' vm_gone_inv)"
printf '  TOTAL delete → gone      %ss\n' "$(el "$t1" "$(now)")"
printf '── done ──\n'
