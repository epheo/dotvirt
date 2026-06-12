# Roadmap — vCenter parity, elegantly

The aim: a UI as close to vCenter's organization, navigation, and feature set as
KubeVirt allows, on the most elegant architecture possible. Two principles bound
every item here:

- **A thin lens that owns nothing.** VM config lives in git (PRs, ArgoCD syncs);
  runtime state is imperative under the *user's* token, RBAC-gated. Power and
  config stay PR-gated because Argo self-heal owns `spec.running`.
- **IA/workflow parity, not pixel parity.** The "dated vCenter" look is the
  accepted artistic direction; what we match is vCenter's page organization,
  verb split (Summary = now · Monitor = over time · Configure = settings),
  lenses, Actions model, and Recent Tasks workflow.

Ordering decisions: a **structural week first** (the parity features stack ~8
endpoints and several action-menu entries on exactly these seams), then
**UI parity before productionizing** — ship/in-cluster is the last phase and can
be pulled earlier at any time. Image registry: **ghcr.io/epheo/dotvirt**.

Sizes: S < half day · M = 1–3 days · L = 1 week+.

## Phase 1 — Structural week

Architecture elegance; everything later builds on these seams.

| # | Task | Size | Sketch |
|---|------|------|--------|
| 1.1 | **One change bus** | S | Hub accepts the shared `changed` chan; delete `forward()` (`cmd/dotvirt/main.go:188-203`); retire the `RepoSet.SetOnChange` dual path (`internal/git/repset.go`) |
| 1.2 | **Proposals background refresher** | M | Per-token open-PR sets refreshed on git-change signal + slow timer; `proposalsFor` (`internal/api/handlers.go:129`) becomes a pure cache read — `forge.FindPR` leaves the WS broadcast hot path |
| 1.3 | **Split god files** | S | `api/handlers.go` (724 L, 30 handlers) → `scope.go` + `handlers_{inventory,draft,runtime,snapshots,metrics,events,appset}.go`; `changeset/changeset.go` (477 L) → `staging/view/propose/revert/drift.go` (same-package moves) |
| 1.4 | **Kill duplications** | S | Drop `manifest.DiskAdd/NetworkAdd` for `model.*` (manifest already imports model); extract a `withWorktree(branch, fn)` clone→mutate→push helper shared by `git/write.go` `Commit` + `git/editcommit.go` `CommitChangeset` |
| 1.5 | **Read-path efficiency** | S | `ListVMEvents` gets a `FieldSelector` (`cluster.go:287` lists ALL events per namespace today); exporter reads VM objects from the clusterstate snapshot instead of per-tick LISTs (`export.go:70`) |
| 1.6 | **Frontend action registry** (keystone) | M | `web/src/lib/actions.ts` descriptors `{id, label, kind, enabled(vm), danger}` covering Restart/Pause/Unpause/Migrate + Edit Settings/Snapshot/Console/Download manifest (free OVF-export analog)/Delete (+ Clone later); `ActionMenu.svelte` renders any list; port VMDetail's Actions ▾ onto it — header menu, context menu (2.2), and bulk bar become three projections of one registry |

## Phase 2 — vCenter IA completion

Highest parity-per-effort.

| # | Task | Size | Sketch |
|---|------|------|--------|
| 2.1 | **Global search** | S | Ctrl+K palette in the `+page.svelte` header over the streamed inventory (VMs by name/IP/label, projects, namespaces, nodes) → `setScope`/`selected`; clickable label chips → `label:k=v` filter (= tags parity). Zero backend |
| 2.2 | **Right-click context menu** | M | `ContextMenu.svelte` (reuse the click-away overlay pattern); `oncontextmenu` on `InventoryTree`'s vmRow + `VMTable` rows; renders the 1.6 registry; bulk variants when the row is in the picked set; small container-row registry (New VM here / Open repo) |
| 2.3 | **Configure tab** (VM + container) | M | VM tabs → Summary · Monitor · **Configure** · Snapshots · Console; Configure = sub-rail (Hardware / Options / Labels) of read-only sections from `model.VM`, per-section Edit opens the existing `EditSettings` (new `initialSection` prop); slim Summary (move the Disks/Networks/Labels tables here). Container Configure: repo URL + namespaces; quota backfills from 3.5 |
| 2.4 | **Permissions tab** | M | `cluster.Client.Permissions(ctx, ns)` via SelfSubjectRulesReview (pattern at `cluster.go:174-186`) → curated capability list; **must check `subresources.kubevirt.io`** (vnc, restart…) or the tab contradicts the Actions menu; `GET /api/permissions?namespace=`; tab on VM + container — the vCenter quartet complete |
| 2.5 | **Migration progress rows** (vMotion parity) | M | `clusterstate.liveFromVMI` reads `vmi.Status.MigrationState` (already watched, currently unread) → `LiveVM.Migration{source, target, started, completed, failed}` → `model.VM` → TaskDock active-migrations rows + a "Migrating to X…" banner on the VM Summary. Zero new watches or polls |

## Phase 3 — Feature heavies

| # | Task | Size | Sketch |
|---|------|------|--------|
| 3.1 | **Clone** | M | `internal/cluster/clone.go` mirrors `snapshot.go` (dynamic client, user token): GVR `clone.kubevirt.io/v1beta1 virtualmachineclones`, Create/List with phase; registry entry + name-prompt modal. **Owns-nothing wrinkle:** a clone creates config state, so the target lands `NotTracked` — pair with "Adopt into git" (extend `changeset.Adopt` to creates, manifest read off the `running` branch) |
| 3.2 | **Container Monitor → Performance sub-rail** | M | `metrics.Client.ScopeMetrics(token, namespaces, node, rng)` with `topk(5, sum by(namespace,name)(…))` per chart; **prereq: `rangeQuery` returns only the first series (`metrics.go:393-396`) — extend to multi-series with labels** (3.4 needs it too); `GET /api/metrics/scope`; events\|performance sub-rail in `ContainerMonitor` like VMDetail's |
| 3.3 | **Networks + Storage lenses** | S+M | Networks: third tree lens grouping by `vm.networks[].network` — frontend-only. Storage: decode `spec.dataVolumeTemplates[]` (size + storageClassName) in `manifest/parse.go` → `model.Disk` gains DV size/class (also fixes the Disks tables, which show size for emptyDisk only today); add StorageClasses to `cluster/options.go` + the wizard |
| 3.4 | **Perf-chart depth** | M | IOPS chart (`kubevirt_vmi_storage_iops_*`), per-NIC/per-drive variants (`sum by(interface)/by(drive)`, needs 3.2's multi-series), stacked memory, `1mo` range (retention-bounded) |
| 3.5 | **Quota-aware project capacity** | M | `cluster.Client.ListQuotas` (user token) + `GET /api/quotas?project=\|namespace=`; quota band under the ClusterSummary rings at project/namespace scope; backfill into 2.3's container Configure |
| 3.6 | **Triggered alarms** | S/M | `metrics.Client.Alerts(token, namespaces)` = the existing `vector()` helper querying `ALERTS{alertstate="firing", namespace=~…}` (no Alertmanager dependency); third dock tab "Alarms" + a header count badge (the drift-amber styling sets the precedent). Alarm *definitions*: non-goal |
| 3.7 | **Catalog browser** (content-library-lite) | S/M | Read-only browser over instancetypes/preferences/DataSources/NADs — the data already ships in `GET /api/options`; tree section or nav entry + a detail drawer. Creating/editing them: non-goal (platform objects) |

## Phase 4 — Ship & productionize

Last by decision (UI-first); pull earlier at any time.

| # | Task | Size | Sketch |
|---|------|------|--------|
| 4.1 | **Merge `feat/observability-summary` → main** | S | Linear branch; main hasn't diverged |
| 4.2 | **Makefile + Forgejo Actions CI + image push** | M | `.forgejo/workflows/ci.yaml` (origin is a Forgejo — GH-Actions-compatible syntax; needs a registered runner): vet/test/lint, `npm run check && build`, main-branch job builds the `Containerfile` → push **ghcr.io/epheo/dotvirt** (PAT secret). Playwright e2e stays a `make e2e` target against the dev cluster (needs a live cluster) |
| 4.3 | **Complete deploy + first in-cluster apply** | M | `deploy/dotvirt.yaml`: add `DOTVIRT_METRICS_URL` (in-cluster thanos-querier), pin the image tag, add a Route (none exists — the file ends at the Service); extend `metrics.New` with a CA-bundle path (service-CA ConfigMap) so in-cluster Thanos isn't `-insecure-tls`. Verify login/WS/VNC/metrics through the Route |
| 4.4 | **Verify the ApplicationSet plugin loop** | S | Label a fresh namespace → Argo app auto-provisioned → VM syncs → appears in dotvirt; watch for ConfigMap baseUrl/token mismatch |
| 4.5 | **Forgejo webhook → instant updates** | M | `POST /api/webhooks/forge` (HMAC `X-Forgejo-Signature`, open-path like the appset plugin); on push/PR events: `RepoSet.Poke(repoURL)` + nudge the 1.2 refresher + hub; `forge.Client.EnsureWebhook` auto-registration on first repo open; lets the git poll interval stretch to minutes |

## Phase 5 — Stretch

Opportunistic, after parity.

- **5.1 Node maintenance-lite** (M): cordon/uncordon (`node.spec.unschedulable`
  patch, user token, SSAR-gated visibility) + Evacuate = Migrate over the node's
  VMs with 2.5's progress rows. Full drain: non-goal.
- **5.2 Console thumbnail on Summary** (S/M, conditional): KubeVirt's
  `vnc/screenshot` subresource, if the cluster's version exposes it.
- **5.3 Overcommit ratio chips** (S): Allocated:Total is already in the
  ClusterSummary payload — render "vCPU 3.2:1".
- **5.4 Image upload / OVF-import analog** (L, unscheduled): CDI
  UploadTokenRequest + upload-proxy streaming.

## Dependencies

- 1.6 before 2.2 and 3.1 (Clone slots into the registry once, not retrofitted
  into three menus)
- 1.3 before every new endpoint (2.4, 3.1, 3.2, 3.5, 3.6)
- 1.2 before 4.5 (the webhook nudges the refresher, not the TTL cache it
  replaces)
- 3.2's multi-series `rangeQuery` before 3.4 · 1.4's model unification before
  3.3-Storage · 2.5 before 5.1
- 4.1 → 4.2 → 4.3 → 4.4/4.5 in order (CI guards merged main; deploy needs the
  pushed image; webhook + appset loop need in-cluster)

## Non-goals

Serial console (VNC covers parity) · DRS/HA (cluster policy) · scheduled tasks
(no CRD; breaks owns-nothing) · alarm definitions (PrometheusRule = platform
config) · tag-category manager (labels + search filter suffice) · guest
customization specs (cloud-init covers it) · datastore file browser (no API) ·
VM rename (k8s can't; clone+delete once 3.1 lands) · pixel-level Clarity
styling (IA/workflow parity is the goal).
