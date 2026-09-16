# Architecture review

A read of the whole tree at v0.0.37 (Go app, operator, SvelteKit SPA), asking one
question: what stands between this codebase and "best in class, efficient, and
maintainable by one person". Nothing here is fixed; this is the map of what should be.

Baseline: `go build`, `go vet`, `go test ./...`, `svelte-check` and the 87 vitest
unit tests all pass on this tree.

## Verdict

The bones are right and unusually well explained. The big ideas hold everywhere:
SA-owned reflector snapshots filtered per token, one event bus with version-stamped
caches, `restfactory` as the identity boundary, git as the only write target, the
draft-to-PR coordinator behind one interface, the `manifest` editor that splices
bytes instead of re-serializing YAML, and on the SPA side one typed API client,
`resource()`/`action()` primitives, a discriminated-union modal host and URL-owned
selection. Comments are overwhelmingly "why". Tests target the logic that deserves it.

The cost for a single maintainer is almost entirely of one shape: **a good primitive
exists, and a third to a half of the call sites predate it and still hand-roll the
idiom.** The second shape is **the same concept written two to five times in packages
that do not reference each other**, so one new Kubernetes kind or one new dialog
touches four or five files with no compile-time link between them. There is one real
correctness defect (VM create is rendered twice with a fresh bcrypt salt), a handful of
stale or doubled comments, and a small amount of dead code.

## The seven themes

These are the shapes to fix. Every per-area finding below is an instance of one of them.

1. **Kind knowledge has no single home.** The resource → API group → resource name →
   scope → Kubernetes kind → form label table is restated in `draft.Resource`
   (`Kinds`, `CreateLabel`, `EditLabel`), `api` (`clusterResourceSSAR`,
   `namespacedResources`, `backingGroup`, `ssarRef`s, the route table), `netstate`
   (GVR vars), `netgen` (apiVersion literals), `git.clusterScoped`,
   `changeset.adoptResource`, and, on the other side of the module boundary, the
   operator's AppProject whitelists. Seven `StageCreateX` methods with identical
   signatures, seven interface entries and seven routes exist only because the table
   is not data. One registry `{Resource, Kind, Group, Version, Plural, ClusterScoped,
   Renderer, Identity}` in `model` (or a tiny `kinds` package) would let every
   consumer derive its view of it, and would collapse the create family to one
   `StageCreate(resource, spec)`.

2. **Reflector scaffolding is written four times.** `clusterstate`, `argo`, `desched`
   and `netstate` each inline the indexer constructor, the reflector start, the
   readiness signalling (three different designs), and, for the two discovery-gated
   ones, a verbatim 20-line "wait for the API, then watch" loop. `internal/reflect`'s
   own doc says it holds the reusable pieces; the loop, the indexer, and a `Ready`
   type belong there. `clusterstate` is also the one snapshot that does not use
   `TrackHealth`, so the inventory can warn that drift or the network catalog is
   stale but never that the VM snapshot is.

3. **Two of everything at the transport edge.** Two error-to-status systems
   (`fail`/`statusFor` for `model.Err*`, `runtimeFail` for k8s errors), chosen per
   call site, so a tenant's RBAC denial on `GET .../snapshots` is a 500 while the
   `POST` beside it is a 403. Five ways to decode a request body producing two
   different 400 wordings. `http.Error` with literal strings beside `fail(invalid())`.
   Local `%w: %v` wrapping that bypasses `unavailable()` and echoes dial errors (and
   possibly tokens) to the client on the metrics routes. `writeJSON` in both `auth`
   and `api`.

4. **The one un-rendered draft entry.** Every create path stores the rendered
   manifest at stage time except `StageCreate` for VMs, which stores `*vmgen.Spec`
   and re-renders at preview and again at propose. `vmgen.Manifest` bcrypts the
   cloud-init password on each call, so the YAML reviewed is not the YAML committed;
   the plaintext password is persisted in the draft JSON; and spec validation runs at
   propose without `ErrInvalid`, so a bad spec stages fine and 500s later. Rendering
   once at stage time (as every sibling does) removes `Entry.Spec`, `draft`'s import
   of `vmgen`, the second "create as changes" renderer, and the defect.

5. **`internal/git` is not a git plane.** It imports `manifest` to apply VM edits
   inside `CommitChangeset` and to parse VMs in `objects.go`, and `GroupNamespaces`
   has nothing to do with git. A `Transform func([]byte) ([]byte, error)` on
   `ChangesetItem` keeps the "re-read from the fresh write clone" property without
   the dependency; the VM index over a repo belongs beside `changeset`. Inside the
   package there are two commit paths sharing 80% of their body, three branch
   resolvers (only one maps to `ErrNoBranch`), two memo caches built differently, and
   a declared-files index that decodes every YAML document twice.

6. **On the SPA, primitives exist but adoption is partial.** `inventory.options` is
   loaded once by the layout and then re-fetched privately by five components with
   five error policies. `resource()` exists but the keyed load with staleness guard is
   hand-rolled in about ten places; `action()` exists but the busy/`Unauthorized`/toast
   block is copied in about ten more (13 `instanceof Unauthorized` in components,
   which should be near zero). `onstaged` is threaded through 107 prop sites while
   `drafts` is a module singleton refreshed directly at 25. And dialogs are hosted by
   two systems: `ui.modal` (right) and six booleans in `VMDetail` plus the
   `detailIntent`/`requestDetail` hand-off that exists only to reach them from outside.

7. **Comment hygiene is good; the residue is doubled and stale comments.** The rule
   "why, never what" is followed. What remains: a few consecutive blocks that say the
   same thing (one superseded by the other), comments that describe behaviour the code
   no longer has, comments that exist to defend a duplication ("mirrors X, do not
   deduplicate"), and a handful of one-line doc comments on trivial helpers.

## Go app: API, cluster, snapshots

Ranked.

- **Unify error mapping.** Fold the `apierrors` cases into `statusFor`
  (Forbidden→403, NotFound→404, Conflict/BadRequest→409) and delete `runtimeFail`
  (`internal/api/handlers_runtime.go:40-65`). Raw k8s errors reach `respond` today
  from snapshots, clones, events, upload status and permissions.
- **One body decoder.** `peek[T]` (`api.go:470`) is nearly it; most callers never use
  `raw`. Replace the hand-written `json.NewDecoder(...).Decode` + literal message in
  `handlers_draft.go:28,119,306`, `handlers_nodes.go:60,89`, `handlers_runtime.go:77`,
  `handlers_trace.go:17`, `handlers_upload.go:28`, and the silently discarded decodes
  in `handlers_snapshots.go:31` and `handlers_clone.go:34`.
- **Move the reflector loop into `reflect`.** `desched.go:68-89` and
  `netstate.go:144-176` are the same function; `cache.NewIndexer(...)` is inlined at
  `clusterstate.go:169`, `argo/snapshot.go:59`, `desched.go:56`, `netstate.go:77`.
  Give `clusterstate` `TrackHealth`.
- **Honor the `Deps` contract or drop it.** "Nil pieces degrade gracefully"
  (`api.go:191`) is false for `Draft`: `sourceFiles` (`handlers_objects.go:195`) and
  `appendTemplates` (`handlers_templates.go:47`) dereference it unguarded. Either
  `NewServer` rejects nil or those routes are not registered.
- **Redact through `unavailable()` everywhere.** `scopeNamespaces`
  (`handlers_metrics.go:66`) wraps with `%w: %v` and the 503 body carries the dial
  error. `handleOptions`/`handleStorageClasses` answer 401 where every other route
  answers 503 for a missing identity.
- **`handleOptions` and `handleStorageClasses` are the same handler**
  (`handlers_inventory.go:165-200`, `handlers_storage.go:13-37`); one
  `saCached[T]` removes both and puts them on the normal preamble.
- **`vmScope` exists (`scope.go:127`) but six handlers spell it out**: metrics,
  usage, manifest, screenshot, upload status, upload token.
- **Drift attach is copied three times** (`handlers_networks.go:199-203` drops
  `Health`; `:233-248`; `handlers_policies.go:88-96`). One `s.driftFor(backing, ns,
  name)` owning the nil check and the `backingGroup` lookup.
- **Namespace-label lookup** is a linear scan in `handlers_effective.go:94-100` and
  `handlers_trace.go:101-106`; `clusterstate.State.NamespaceLabels(name)` via the
  indexer is O(1) and one place.
- **`sourceFiles` reads a git index per project per request** on the polled
  `/api/networks` and `/api/policies`. The codebase's own idiom (a `ttlcache` entry
  stamped with `bus.Version(GitChanged)`) fits directly.
- **Two identical `Adoptable` structs** (`cluster/adopt.go:48`,
  `changeset/drift.go:132`) copied field by field at `handlers_draft.go:213-218`, with
  a comment forbidding the dedupe. `model.Adoptable` satisfies the stated goal (the
  coordinator stays cluster-free) since both already import `model`.
- **Smaller:** CORS allow-methods omits PUT (`api.go:452`) while two PUT routes
  exist, so dev preflight fails for them. `handleRevert` nudges the proposals
  refresher but does not track the project (`handlers_draft.go:383`), the exact bug
  `handlePropose` comments on. Object reads pay the uncached write-SSAR
  (`scope.go:306-310`). `cluster.Client.dynamic()` nil-guards ten call sites while
  `adopt.go` and `netsource.go` use `c.dyn` directly. DRS band semantics
  (`handlers_metrics.go:130-169`) live in `api` while the threshold vocabulary is
  `drsgen`'s. `liveVMs` in `main.go:38-59` is real logic in an untested package;
  `clusterstate` already imports `cluster`. `stream.SetAllowedOrigin` is package-global
  mutable state. Three linear by-name project searches (`hasProject`, `findProject`,
  `byName`). Commit-hash validation with the same error string three times in
  `handlers_draft.go`.
- **Dead:** `argo.Snapshot.Synced()` has no callers and `Drift()`'s doc still points
  at it; `api.readAll` is a one-line alias of `io.ReadAll`.

## Go app: changeset, git, manifest, gen, forge

Ranked.

- **Render the VM create at stage time** (theme 4). `staging.go:45`,
  `view.go:70`, `propose.go:190`, `vmgen.go:275`, `helpers.go:64` vs
  `history.go:312`.
- **One PR open-or-recover.** `Propose` does find→reopen→create
  (`propose.go:96-122`); `Revert` does create→find and never reopens
  (`revert.go:62-76`). Both also call `c.repos.Get` directly and return the raw error
  that `Coordinator.read()` exists to mask (`changeset.go:85-88`).
- **Existence checks via `FileOnBranch` errors.** `LookupOnBranch` was added to tell
  absent from failed; `template.go:75,129,166` and `drs.go:45,75,134` still treat any
  read error as "does not exist". A transient failure stages a create over a live
  file.
- **Split `git` from `manifest`** (theme 5). `editcommit.go:30,88`, `objects.go`,
  `provider.go:66`.
- **Collapse the redundant work inside `git.Repo`.** `Revert` diffs the same commit
  twice (`revert.go:28,32`); three branch resolvers (`git.go:246`, `history.go:233`,
  `history.go:73-80`); `DeclaredFilesOnBranch` decodes every document twice
  (`declared.go:47-51`); `Commit` and `CommitChangeset` share one tail
  (`write.go:117-160`, `editcommit.go:46-125`); `CommitResult` and `EditResult` are
  one type.
- **Four path/content structs.** `git.File`, `git.ManifestFile`,
  `changeset.LiveManifest`, `drsgen.File`. One `model.File` removes three conversions
  and lets `vmtemplate` stop importing `git` (it imports it for a constant and a
  struct).
- **Helpers written without checking the neighbour.** `siblingRepoURL`
  (`tenancy.go:302`) is `forge.OwnerPrefixURL` + name; `manifest.ifaceName` and
  `vmgen.multusNetwork`'s inline derivation; `manifest.runStrategyFor` and
  `vmgen.runStrategy` (with `model.Power` already owning the vocabulary);
  `manifest.blankDVTemplate` and `vmgen.blankDataVolumeTemplate` (the comment says
  "mirroring"); `vmgen.toAnyMap` sorts keys before inserting into a map (a no-op),
  `netgen.toStrAny` is the right five lines; `changeset.requireDNS1123` wraps
  `validate` in `ErrInvalid` but three sites do it by hand (have `validate` return
  `ErrInvalid` itself); `netgen.SameDocument` is a generic YAML-equality util that
  `changeset/objects.go` imports `netgen` for; four hand-rolled kind/name/namespace
  header decoders (`git/declared.go:14`, `netgen/decode.go:16,95`,
  `manifest/parse.go:15-20`, `vmtemplate/parse.go:55`); four hand-rolled `"://"`
  parsers in `pkg/forge` next to one that uses `net/url`.
- **Vocabulary spread.** `ClusterScopeNS = "cluster"` is defined in
  `changeset/staging.go:75`, documented as a literal in `model/draft.go:140`, used by
  `api`; the `""` ↔ `"cluster"` translation is inlined four times. `DraftItem.Resource`
  carries draft names from `view.go` and Kubernetes kind names from `history.go`, and
  the SPA has to know which endpoint it came from. `draft.Resource` carries UI labels
  (`CreateLabel`, `EditLabel`) in the persistence package while the SPA has
  `vocab.ts`.
- **The coordinator.** 46 exported methods mirrored by a 46-method `api.Draft`. 14
  need neither identity nor the store (history, commit, proposals, manifest,
  templates, declared files, DRS state, object spec, drift, ownership); a `Reader`
  embedded in `Coordinator` splits `api.Draft` into read and write halves and makes
  the read half testable without a `draft.Store`. Within: `History` /
  `NamespaceHistory` / `ObjectHistory` are one body three times
  (`history.go:27-91`); `StageCreateProject` re-implements `stageProjectAdoption`
  (`tenancy.go:72-105` vs `:262-298`); `requireRepo` is called twice on three paths;
  `AdoptNamespace` is a one-line alias of `AdoptObjects` with a nine-line doc;
  `Manifest` walks the whole tree for a path it already holds (`gitread.go:29-38`);
  the six-field `draft.Entry` literal appears ~14 times.
- **`pkg/forge` is not `pkg/`.** It imports `internal/tlsconf` (`forge.go:17`), so
  no other module can use it; move `tlsconf` in or move `forge` under `internal/`.
  Five hand-built HTTP requests each set headers and drain bodies; `Client` copies
  three fields from `Factory`; two constructors where one with a `TokenSource` would
  do. `OpenProposals` does two HTTP calls per open PR on every `GitChanged` and every
  backstop tick; refresh reviews only for PRs whose head SHA moved.
- **`manifest` seams.** `findVM` (`edit.go:193-222`) has an unreachable branch: a
  single `yaml.Unmarshal` into a `*yaml.Node` always yields one `DocumentNode`, so a
  VM in the second document of a multi-doc file is silently not found and the comment
  describes behaviour the code does not have. `domainNode` re-implements
  `templateSpecNode`. Three separate "scan forward while indented deeper" loops in
  `lineeditor.go`. `edit.go:238` says "insert as first child" but `insertChild`
  appends. `netgen/namespace.go:30-37` glues one function's doc onto another.
- **Small:** `draft.Open` returns an always-nil error. `vmtemplate.Dir` aliases
  `git.TemplatesDir` but `changeset/template.go` builds the path by hand three times
  and `ns/name.yaml` by hand instead of `vmgen.ManifestPath`. `vmtemplate.Renderer`
  is a one-implementation interface hard-coded in `New`. `history.go` uses literal
  `"create"/"edit"/"delete"` where `view.go` uses `draft.Kind`. `netgen.Spec` /
  `netgen.Manifest` are the only unprefixed pair. `model/ops.go:157-162` is a
  dangling comment block.

## Go app: netstate and netgen

- **Read and write sides decode the same shapes independently.** `netgen` has typed
  doc structs for everything it writes (`decode.go:95-164`); `netstate` re-walks the
  same fields with `unstructured.NestedX` (`catalog.go:189-215`). The
  `kubernetes.io/metadata.name In` selector netgen writes is known in four places.
  Convert each unstructured object once into netgen's struct (the pattern already
  exists at `effective.go:155`).
- **Display and evaluation walk each rule shape twice.** `adminPeers`
  (`policies.go:261-295`) ↔ `anpPeerMatch` (`tracematch.go:89-129`); `netpolPeers` ↔
  `netpolPeersMatch`; `portsSummary` ↔ the two `PortsMatch`; EgressFirewall `to`
  parsing in `policies.go:167` and `trace.go:270`. The comment "every producer must
  word it identically" is the symptom. Decode a rule once into a small typed
  `rule{peers, ports}`; `summary()` and `match(target)` become methods on it.
- **`Effective` and `Trace` re-derive project-tier selection and admin ordering**
  (`effective.go:35-63` vs `trace.go:188-207`, `tracematch.go:59-70`). Two
  definitions of "evaluation order".
- **`gatewayWalk` re-implements `directionWalk`'s `decide`/`addStep` inline**
  (`trace.go:290-317`); the "May match: ..." string is built three times.
- **Scope vs kind vocabulary.** Write side: `netgen.Scope{Project,Shared,VLAN}`; read
  side: `Kind: vlan, Scope: shared`. VLAN is a scope when creating and a kind when
  reading; the handler and the SPA translate.
- **Phantom fields.** `model.Uplink.Ports`, `.VLANs`, `.Status` are never written in
  Go yet appear in `model.gen.ts` as real fields.
- **Comments:** `handlers_networks.go:162-168` has two overlapping blocks, the first
  superseded by the second; `handlers_trace.go:18-44` uses `http.Error` literals
  five times where siblings use `fail(invalid())`.

## Operator

- **Failure recording is inconsistent across phases.** `secrets.go:40-42` returns
  raw errors with no condition; `argo.go:36-41` returns raw errors from the token
  mirror and TLS trust while `argo.go:66-68` uses `failPhase`; so the one case the
  code deliberately "fails loudly" on (`argo.go:117-119`, mirror-ownership conflict)
  never reaches a status condition. Funnel every phase error through `failPhase`,
  or have the reconcile loop record `phase.cond` on any error.
- **Three Secret write regimes.** SSA via `applyOwned`, Get+Create
  (`secrets.go:50-71`), and a hand-rolled Get+Update/Create (`argo.go:104-137`), which
  the controller doc admits. One SSA path drops the update branches and the `update`
  verb on secrets. `ensureSecret` builds a Secret without `TypeMeta`, a trap if it is
  ever switched to `apply`.
- **Ingress CA read three times, trust anchors applied twice per reconcile**
  (`forge.go:53`, `workload.go:23`, `argo.go:224-233` with a literal
  `"ca-bundle.crt"` instead of `install.IngressCAKey`). Read once per reconcile.
- **Effective values recomputed per call.** `argoTarget` four times; `argoServerURL`
  (a Route GET) in both `forge.go:249` and `argo.go:151`. A `reconcileCtx` computed
  after `normalizeSpec` removes the repeats; the code already has that idea for the
  forge spec.
- **`IngressType` is a typed enum with no constants**; `"auto"/"route"/"ingress"`
  are literals in `workload.go` and `forge.go`, and `resolveExposureType` returns a
  bare string.
- **Four in-cluster URL helpers and one bypass**: `svcHost/svcURL/serviceHost/
  ServiceURL` (`dotvirt.go:136-144`, "so the template lives in one place") yet
  `applicationset.go:56` hand-builds the URL with a different suffix.
- **Doc drift.** `platform/detect.go:21` says errors default to Kubernetes; `main.go`
  fails startup. `dotvirt_types.go:133` lists a `Pending` phase that does not exist.
  `operator/README.md`: the phase order is wrong, "Helm" does not exist, the OAuth CA
  note is stale (`install/dotvirt.go:206-209` sets it), "replicas" contradicts
  `replicas: 1`, the creds runbook differs from `status.forgeAdminHint`, and the
  trust-anchor behaviour (the most surprising thing the operator does) is unmentioned.
- **`pkg/forge` sharing is clean.** Nothing forge-related is re-implemented. Nit:
  three factories per bootstrap with an `"unused"` token sentinel.

## SPA

Ranked. The suggested order at the end starts here because these are the
highest-leverage single changes in the tree.

- **Fold `VMDetail`'s six dialogs into `ui.modal`.** Six booleans
  (`VMDetail.svelte:89-110`), a reset effect enumerating them (`:252-262`), an
  `applyAction` switch (`:214-241`), and the `detailIntent`/`requestDetail` hand-off
  (`ui.svelte.ts:127-135`, `+page.svelte:28-35`, the `intent`/`onintentdone` props)
  exist only so those dialogs can be reached from outside. `ui.modal` already carries
  a VM for `staged`; adding the six kinds deletes all of it and lets those modals take
  `onstaged` from the one place `AppModals` already provides it. Adding a VM dialog
  today touches four files.
- **Let `StageModal`/`Wizard` own the staged side effects.** Success →
  `drafts.refresh()` + the standard toast with its "Review & propose" action (the
  literal appears 10 times). `onstaged` (107 sites) becomes optional and mostly
  disappears; `drafts.refresh()` direct calls (25) stop competing with it. Add
  `action({ toast })` so components stop touching `Unauthorized` and `friendlyError`
  (13 sites: `ReleaseProjectModal`, `PlatformAdoptBanner`, `AdoptBanner`,
  `SSOBanner`, three in `VMDetail`, `AppModals.discardStaged`,
  `ContainerWorkspace.runBulk`, `Login`).
- **One source for `inventory.options`.** The layout loads it
  (`+layout.svelte:115-122`); `VMTable`, `NewVMWizard`, `EditSettings`,
  `UploadModal`, `StorageMigrateModal` re-fetch privately with five error policies,
  and `CatalogWorkspace` writes back into the store (two writers). Give the layout
  pull the retry loop `networks`/`policies` already have and delete the five. Sizing
  resolution against the catalog is written four times (`VMTable`, `Inspector`,
  `VMConfigure`, `VMSummary`, the last without the catalog so Summary and grid can
  disagree); one `vmSizing(vm, options)`.
- **Convert the remaining hand-rolled loads to `resource()`.** `MetricsPanel`
  (`:25-59`, exactly a keyed resource with a range-dependent poll), `VMDetail.loadDrift`
  with its own `fresh()` guard, the nodes load written twice (`MigrateModal`,
  `EditSettings`) with the same RBAC degrade, `DeployTemplateModal` fetching templates
  beside `state/catalog.svelte.ts` which exists for that, `authMethods` in both
  `Login` and `SSOBanner`, three load-once caches in `ChangesWorkspace`
  (`:262-353`) with `FileHistory` and `VMChanges` as a fourth and fifth copy. Eight
  files use an effect with no reactive reads as "on mount".
- **Split the two large components.** `VMDetail` (535 lines) = header/toolbar + tab
  router + dialog host; its `vm: VM | null` branch (`:531-535`) is unreachable (the
  one mount site guards on `{#if vm}`) and is why every handler starts with
  `if (!vm) return`; the `<script module>` re-export has no importers; the tab list
  is hardcoded while `nav.ts` holds `VM_TABS` ids only. `ChangesWorkspace` (953
  lines) = lanes + three review panes whose header row and footer bar are copied
  three times + an undo modal + the three caches. Cut: `ChangesLanes`,
  `ReviewItem`, `ReviewProposal`, `ReviewCommit`, `reviewCache.svelte.ts`.
- **State ownership.** `ui.bulkIntercept` and `ui.search` are non-reactive callback
  slots that mounted components register into, so the store depends on component
  lifecycles. `inventory.inventory` forces `inventory.inventory?.projects` at 20 sites
  and an alias import in `GlobalSearch`. `state/nav.svelte.ts SECTIONS` duplicates
  `nav.ts INVENTORY_SECTIONS`. `actions.ts` and `objects.ts` use different verb
  conventions.
- **Local helpers duplicating `lib/`.** `VMTable.memBytes` ≈ `format.quantityBytes`
  with a third regex in `ChangesWorkspace`; `libraryLabel` three times; `parseOwners`
  twice plus two more inline splits; the literal "Name must be lowercase alphanumeric
  with dashes" 8 times while `validate.NAME_HINT` exists; `${vm.namespace}/${vm.name}`
  inline 33 times with a local `vmKey` in `VMTable`; raw inputs with hand-copied
  border classes where `TextInput`/`SelectInput` exist; `Inspector` hand-writes eight
  `dt/dd` rows where `Row` exists; the a11y scroll-region wrapper copied 7 times; the
  skeleton 6 times; the primary pill button class string 16 times and the link-style
  accent button 24 times with no `Button.svelte`; three outside-click idioms.
- **Dead and unused.** `api.ts`: `EgressFirewallPort` and `PolicyPort` are identical
  types; `ScopeQuery` has no external importer. Exports with no importer:
  `lenses.DEFAULT_CLASS`, `lenses.ClassDisk`, `catalog.CatalogRow`,
  `rowlist.RowList`, `tasks.TaskSources`, `textdiff.DiffLine`, `textdiff.DiffRow`,
  `nav.CONTAINER_TABS`. `@sveltejs/adapter-auto` is installed but unused. There is no
  ESLint, which is how the list accumulated; `svelte-check` does not flag unused
  exports.
- **Tests and tooling.** `e2e/helpers.ts` and `e2e/ui/fx.ts` each define `login`
  and an open-VM helper with different selectors; `regressions.spec.ts` re-inlines
  `stageChange`. `playwright.live-ip.config.ts` says "not committed" and is
  committed with a hardcoded IP. `test:ui` needs a build first and no script chains
  it.
- **Comments.** Mostly right. Restating ones: `NewVMWizard:33`, `VMTable:181`,
  `SyncBadge:17`, `MigrateModal:39`, section rulers in `ChangesWorkspace`. The 44
  `svelte-ignore state_referenced_locally` lines each repeat the same "mounts fresh
  per open" sentence; state it once in `StageModal`'s header.

## Comment residue, all areas

Consecutive blocks saying the same thing: `handlers_inventory.go:46-56`,
`handlers_networks.go:162-168`. Describing behaviour the code does not have:
`manifest/edit.go:196-199` and `:238`, `platform/detect.go:21`,
`dotvirt_types.go:133`, `argo/snapshot.go:104-105`. Referring to something that no
longer exists: "the exporter" in `eventbus/bus.go:10,12` and `scope.go:255`. Defending
a duplication: `changeset/drift.go:128-131`, `manifest/edit_devices.go:91`. Broken
mid-sentence: `main.go:1-5`. One-line "what" docs on trivial helpers:
`clusterstate.go:167`, `vmspecstore.go:33`, `inventory.go:117`, `config.go:166`,
`cluster/quotas.go:60`, `argo.go:300`, `cluster.go:393-401`, `cluster/snapshot.go:78`,
`handlers_runtime.go:16`, `handlers_networks.go:27`, `metrics/query.go:85,144,252,394`,
`tasks.go:61`, `eventbus.go:75`, `reflect/store.go:60-63,118-119`,
`changeset/history.go:78`, `git/write.go:176,183`, `forge/url.go:19,25`,
`netgen/policy.go:138`, `draft/draft.go:225`, `vmgen/vmgen.go:197`,
`dotvirt_controller.go:230`, `operator workload.go:92`, `install/forgejo.go:86`,
`install/workload.go:66-67`.

## Suggested order

Each step is independently shippable and pays for itself.

1. Render the VM create at stage time (the defect; small and contained).
2. Fold `VMDetail`'s dialogs into `ui.modal`; delete `detailIntent`.
3. `StageModal`/`Wizard` own refresh + toast; `action({ toast })`; retire `onstaged`.
4. Unify `statusFor`; one body decoder; `unavailable()` everywhere; fix CORS PUT.
5. The kind registry in `model`; derive `draft.Resource`, `api` tables, `netstate`
   GVRs, `netgen` apiVersions from it; collapse `StageCreateX` to one method.
6. Move the reflector loop, indexer and readiness into `reflect`; give `clusterstate`
   health.
7. `inventory.options` single source; `vmSizing()`; remaining loads onto `resource()`.
8. `git`: transform callback instead of the `manifest` import; one commit tail; one
   branch resolver; `LookupOnBranch` at the six existence checks; one `model.File`.
9. netstate: typed rule decode shared by summary and match; shared selection helper
   for `Effective`/`Trace`.
10. `Coordinator` read/write split; PR open-or-recover; the helper dedupes.
11. Operator: `failPhase` everywhere; one Secret path; `reconcileCtx`; `IngressType`
    constants; README refresh.
12. Split `ChangesWorkspace`; `Button.svelte`; `vmKey()`; unused exports; ESLint.
13. The comment residue list, in passing with each of the above.

## Keep as patterns

- Consumer-defined, nil-able interfaces with documented degradation (`Resyncer`,
  `LiveSource`, `PruneSource`, `Deps`).
- `model.Err*` sentinels wrapped with `%w`, mapped at the edge; `fail`'s redaction
  rule; `unavailable()`.
- `scope.go`'s preamble: `resolveProject` + pickers + `platformScopeWith`.
- Version-stamped caches over `bus.Version` with a TTL backstop; the hub's
  reconcile-to-level loop; deterministic `argo.rebuildLocked`.
- `restfactory.Factory[T]` with `clearCredentials` as the named boundary.
- `stageRendered` + generic `stageSpec[S]`; `soleDeclarer`'s whole-file rule;
  `netgen.Decode`'s byte-identical round-trip as the "can the form edit this" test.
- The `manifest` design: read-only `yaml.Node` for positions, byte-stable splicing,
  the post-edit re-parse guard, `TestEditToMatchConverges`.
- `git.RepoSet`: canonical-URL cache, single fetcher, coalescing poke.
- `forge.ensureHook`'s recreate-not-edit reconcile; `TokenSource` resolved per call.
- Operator: the phase pipeline with one condition per phase, `waitPhase`/`failPhase`
  twins, effective spec in memory and status via merge patch, `install.Apply` as the
  single SSA entry, `rbac_markers.go` with rationale beside each verb.
- SPA: `api.ts` with one `req()` and the 401 sink; `resource()`/`action()`/`rowList()`;
  `ui.modal` as a discriminated union; `StageModal` + `FormField` + `ChoiceCards`;
  the VM action registry projected by every menu; URL as state via `nav.ts` and
  `review.ts`; stable-key discipline in `inventory.svelte.ts`; pure-logic modules with
  unit tests; literal tone maps so Tailwind sees static strings.
