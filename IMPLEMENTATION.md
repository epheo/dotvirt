# dotvirt — Multi-tenancy Implementation Spec

> Execute task-by-task in order. Build + verify after each. This file is the
> single source of truth for the multi-tenancy phase; the design rationale is in
> `~/.claude/plans/what-do-you-think-temporal-stroustrup.md` and memory
> (`dotvirt-multitenancy-design`). Work dir: `/home/epheo/dev/dotvirt`.

## Goal

Make dotvirt multi-user + multi-tenant as a **thin lens that owns nothing**,
riding the cluster's own auth + RBAC.

**Core principle: tenant boundary = repo boundary.** Git has no sub-repo read
ACLs, so one shared repo can't be multi-tenant (UI filtering = security theater).
Each project = its own git repo. Isolation holds at TWO layers: cluster RBAC
(namespaces a user can see) + git (a user only ever learns a repo URL from a
namespace they can already see; per-project repos hold no cross-tenant data).

## Settled decisions (do NOT relitigate)

- **Auth = bearer-token login.** User pastes a token (`oc whoami -t` /
  `kubectl create token`). Validated via **TokenReview**. A **signed httpOnly
  cookie holds the raw token** (stateless, no server session store). OIDC +
  kubeconfig-upload are LATER phases (just other ways to obtain the token; the
  authz model below is unchanged).
- **User-token pass-through:** every read/edit/console cluster call uses the
  user's token → cluster RBAC is the SOLE authority. dotvirt enforces nothing.
- **Background work uses dotvirt's own SA** (in-cluster SA or `-kubeconfig`):
  running-branch export, change-signal watches, Argo resync (no user context).
- **Projects = live cluster facts, no dotvirt registry.** Namespace label
  `dotvirt.io/project=<name>` + annotation `dotvirt.io/repo=<url>`, read with the
  user's token. Git **creds** come from dotvirt config (one Forgejo SA token) —
  the only non-cluster input.
- **Per-project git repos.** Per-project `running` branch (SA export scoped to
  that project's namespaces), per-project `dotvirt/proposed`, PRs to that repo.
- **Drafts keyed by (user, project).**
- **dotvirt does NOT create repos/Argo apps** — a platform ApplicationSet does.
- One Forgejo SA for pushes; commit author + PR attributed to the k8s user.
- Cluster-agnostic: only portable primitives (bearer tokens, TokenReview, RBAC,
  labels). Works on OpenShift, RKE2, k3s, microshift.

## Current state (post elegance-refactor — what exists today)

- `internal/manifest`: PURE parse/edit/semantic-diff over `[]byte ↔ model.VM`
  (ParseVMs, ApplyEdit, ChangesForEdit, DiffVMs, UnifiedDiff, VMEdit/DiskAdd/
  NetworkAdd). No go-git. Leaf over `model`.
- `internal/model`: shared DTOs — VM, Project{Namespace,VMs}, Inventory{Branch,
  Projects}, Change, DraftView/DraftItem, ProposeResult, ResyncResult, Options/
  Instancetype/Preference/OSImage/NetworkOption, DriftResult.
- `internal/cluster.Client`: ONE identity. `New(kubeconfig, nsLabel)`. Methods
  LiveState(ctx), ListVMObjects(ctx), ListOptions(ctx)→model.Options,
  VNCConn(ns,name)→net.Conn, Watch(ctx, notify chan) [cluster-wide VM+VMI watch],
  projectNamespaces(ctx). `restConfig(kubeconfig)` helper (in-cluster or
  BuildConfigFromFlags). Fields: kubevirt, kube, dyn, nsLabel.
- `internal/argo.Client`: dynamic client over Application CRs. `New(kubeconfig)`.
  VMDrift(ctx)→map, Watch(ctx,notify), Resync(ctx,ns,name)→model.ResyncResult.
- `internal/git`: `Repo` (in-memory mirror clone; reads NEVER fetch — single
  fetcher via `Refresh()` called by `HeadsSignature()` which the git-poll drives).
  `Provider` (Inventory(branch)→model.Inventory, Branches(), FindVM(branch,ns,
  name)→(model.VM,bool,err); WithEnricher/WithDrift hooks). `WriteRepo`
  (CommitChangeset/CommitVMEdit/CommitNewFile — clone per commit, push).
  `AllowInsecureTLS()`. `OpenWrite(url,user,token,push)`, `Open(url,user,token)`.
- `internal/changeset.Coordinator` implements `api.Draft`. `New(store *draft.Store,
  repo *git.WriteRepo, lookup VMLookup{FindVM}, fc *forge.Client, rs Resyncer,
  baseBranch, proposedBranch)`. Methods StageEdit/StageCreate/Unstage/Get/Discard/
  Propose/VMDrift/Adopt/Resync. helpers.go: editFromRequest, changesForCreate,
  editToMatch.
- `internal/draft.Store`: disk-persisted JSON, SINGLE shared draft.
  `Open(path)`, Stage/Unstage/List/Clear/Len. Entry{Kind,Namespace,Name,
  SourceFile,Edit *manifest.VMEdit,Spec *vmgen.Spec}.
- `internal/vmgen`: Manifest(spec)→(path,content). Spec{Name,Namespace,
  Instancetype,Preference,OSImage,DiskSize,Running,CloudInit,ExtraDisks,Networks,Labels}.
- `internal/stream.Hub`: ONE shared inventory pushed to ALL subscribers.
  `NewHub(inventory func(branch)(model.Inventory,error))`, Run(ctx), Changed()
  chan, Handler (WS, ?branch=). subscriber{branch,send,lastJS}. VNCProxy.
- `internal/forge.Client`: Forgejo (Gitea API). New(Config{BaseURL,Token,Owner,
  Repo,InsecureTLS}), CreatePR, FindOpenPR, CompareURL. Returns nil if unconfigured.
- `internal/config.Config`: flags listed in code. `-kubeconfig`, `-namespace-label`,
  `-base-branch`, `-running-branch`, `-forge-{url,token,owner,repo}`,
  `-insecure-tls`, `-push`, `-export-interval`, `-git-poll-interval`, `-cluster`,
  `-argo`, `-repo`, `-draft-file`, `-ui-origin`, `-addr`.
- `internal/api`: net/http mux, CORS. `Deps{Inventory,Options,Draft,Stream,VNC,
  AllowOrigin}`. Routes: GET /api/healthz, /api/branches, /api/inventory?branch=,
  /api/options, /api/inventory/stream (WS), /api/vms/{ns}/{name}/vnc (WS), POST
  /api/vms/{ns}/{name}/edit, POST /api/vms, GET/DELETE /api/draft, DELETE
  /api/draft/{ns}/{name}, POST /api/draft/propose, GET /api/vms/{ns}/{name}/drift,
  POST /api/vms/{ns}/{name}/{adopt,resync}. Helpers: respond(w,v,err),
  draftReady(w,d), readAll(r), writeJSON, withCORS.
- `internal/export.Exporter`: New(cluster, writeRepo, runningBranch). Run(ctx,
  interval) — exports all-cluster VMs to the running branch.
- `cmd/dotvirt/main.go`: wires it all. provider+hub+pollGit always; cluster/argo
  behind flags; coordinator; deps. enricher()/driftSource() adapters bind cluster/
  argo to the provider's WithEnricher/WithDrift.
- Frontend `web/src/`: api.ts (typed client + streamInventory WS), routes/+page.svelte
  (shell: branch switcher, inventory tree, detail, Changes button), lib/components/
  {InventoryTree,VMDetail,EditSettings,NewVMWizard,ChangesPanel,Console,ChangeList,
  SyncBadge,PowerDot}.svelte. NO auth today.

## Live environment (for verification)

- `oc` authenticated as kube:admin to OpenShift 4.22 (api.hetznet.epheo.eu).
- On-cluster Forgejo: https://forgejo.apps.hetznet.epheo.eu (user dotvirt /
  dotvirt123), token in `/tmp/forgejo-cluster-token.txt`, in-cluster svc
  `forgejo-http.forgejo.svc:3000`. Repo dotvirt/vmrepo exists (will add per-project).
- One Argo app `dotvirt-vms` (openshift-gitops) syncs vmrepo→cluster; selfHeal on.
  Argo SA has kubevirt.io:admin in default/tenant-a/tenant-b/portail-operator-system/
  vm-health-gitops. 6 VMs across those namespaces.
- Run dotvirt locally: `go build -o ./dotvirt ./cmd/dotvirt`; env DOTVIRT_REPO,
  DOTVIRT_FORGE_*, DOTVIRT_DRAFT_FILE; flags -cluster -argo -kubeconfig=$HOME/.kube/config
  -insecure-tls -push=true. Frontend: `cd web && npm run dev -- --port 5173 --host localhost`.
  Vite proxies /api (incl WS) → :8080. Playwright (chromium headless) installed in web/
  for screenshots; output to /tmp, view via Read. /tmp is noexec — build binaries into cwd.

## Conventions to honor

- Match existing style; reuse helpers; keep the elegance from the refactor (pure
  manifest, typed model boundaries, single-fetcher git). gofmt -w after edits.
- Comment only non-obvious "why". Don't over-engineer.
- After each task: `go build ./...`, `go vet ./...`, `go test ./...`, and
  `cd web && npm run check` if frontend touched. All must be green before moving on.
- Editing .svelte: tab indentation; if Edit string-match fails on whitespace, use a
  small python splice (worked reliably this session).

---

# TASKS (in order)

## Task 27 — Per-token client factory (cluster + argo)

**cluster:**
- Add a credential-less **base config**: refactor `restConfig(kubeconfig)` to also
  expose a `baseConfig(kubeconfig) (*rest.Config, error)` that returns the config
  with `BearerToken`, `BearerTokenFile`, `Username`, `Password`, and
  `TLSClientConfig.{CertFile,KeyFile,CertData,KeyData}` CLEARED — keep only `Host`,
  `APIPath`, `TLSClientConfig.{CAData,CAFile,Insecure,ServerName}`. (So per-user
  tokens fully determine identity.)
- New `Factory` struct: `{base *rest.Config; saToken string; cache}`.
  `NewFactory(kubeconfig string) (*Factory, error)` builds base + captures the SA
  token (from the kubeconfig's current user token, or in-cluster
  `/var/run/secrets/.../token`). Methods:
  - `For(token string) (*Client, error)` — clone base, set `BearerToken=token`,
    build kubevirt/kube/dyn clients → `*Client`. Cache by `sha256(token)` with a
    short TTL (e.g. 5 min) + size cap; correctness first.
  - `SA() (*Client, error)` — `For(f.saToken)` (background identity).
- `Client` loses `nsLabel`. Keep its read methods unchanged (LiveState,
  ListVMObjects, ListOptions, VNCConn, Watch). `projectNamespaces` MOVES OUT (see
  Task 29) — but Watch still needs a namespace set: for background watches, keep a
  cluster-wide `NamespaceAll` watch on the SA client (unchanged).
- Add `Client.VisibleNamespaces(ctx) ([]string, error)`: `Namespaces().List` with
  this client's token; **if Forbidden**, fall back to `SelfSubjectRulesReview`
  (authorization.k8s.io/v1) — collect namespaces from rules that grant get/list on
  pods/virtualmachines (resourceRules with non-"*" namespaces), intersect a
  configured candidate set if provided. Return the namespace names the token can use.
- `argo`: same pattern — `Factory{base, saToken, cache}`, `For(token)`, `SA()`.
  Per-user client for VMDrift; SA client for Watch + Resync.

**Verify:** `go build/vet/test`. (Behavioral verify happens once wired.)

## Task 28 — Auth: TokenReview + signed cookie middleware (`internal/auth`, new)

- `Identity{Token, Username string; Groups []string}`. `FromContext(ctx) (Identity, bool)`; `NewContext(ctx, Identity)`.
- `Authenticator{saKube kubernetes.Interface; secret []byte; ttl; cache}` — needs
  the SA kube client (from cluster.Factory.SA().kube — expose a getter, or pass the
  *kubernetes.Interface*) to call TokenReview.
  - `Validate(ctx, token) (Identity, error)`: cached `TokenReview`
    (authentication.k8s.io/v1) create; on `Authenticated==true` return
    Identity{token, status.User.Username, status.User.Groups}; cache ~1 min by token hash.
- Cookie: name `dotvirt_session`; value = `base64(token) + "." + hex(HMAC-SHA256(token, secret))`.
  httpOnly, SameSite=Lax, Path=/, Secure when request is TLS. `setCookie(w, token)`,
  `readCookie(r) (token string, ok bool)` (verify HMAC), `clearCookie(w)`.
- HTTP handlers (mounted in api): `Login(w,r)` POST `{token}` → Validate → setCookie
  → 200 `{username,groups}`; 401 on invalid. `Logout(w,r)` → clearCookie → 204.
  `Me(w,r)` → Identity from ctx → `{username,groups}`.
- `Middleware(next) http.Handler`: skip for `/api/healthz`, `/api/login`; else
  extract token from cookie OR `Authorization: Bearer`; `Validate`; inject Identity
  into ctx; 401 if missing/invalid. (For WS routes, the cookie is present on the
  handshake request — middleware runs there too.)
- Config: `-session-secret` (random default if empty, log a warning that sessions
  won't survive restart). Helper to read in-cluster SA token if `-kubeconfig` empty.

**Verify:** build; curl POST /api/login with `oc whoami -t` → 200 + cookie; bad token → 401.

## Task 29 — Project resolver (`internal/project`, new) + model

- `model`: replace `Project{Namespace,VMs}` with:
  `ProjectNamespace{Namespace string; VMs []VM}` and
  `Project{Name string; Repo string; Namespaces []ProjectNamespace; Error string \`json:",omitempty"\`}`.
  `Inventory{Projects []Project}` (drop `Branch`, or keep optional).
- `project.Resolver{projectLabel, repoAnno string}`. `NewResolver(label, anno)`.
  `Map(ctx, c *cluster.Client) ([]ProjectInfo, error)` where
  `ProjectInfo{Name, Repo string; Namespaces []string; Error string}`:
  - `nss := c.VisibleNamespaces(ctx)`; for each, fetch the Namespace object (with
    the user client) to read `.metadata.labels[projectLabel]` and
    `.metadata.annotations[repoAnno]`.
  - group by project label value; skip namespaces without the label (not managed).
  - per project: if namespaces disagree on repo or none set → set `Error`
    ("no repo configured" / "conflicting repo annotations"); else Repo = the URL.
- Config: `-project-label` (default `dotvirt.io/project`), `-repo-annotation`
  (default `dotvirt.io/repo`). REMOVE/repurpose `-namespace-label`.

**Verify:** build; unit test Map with a fake kube client (labeled/annotated namespaces).

## Task 30 — Per-project git RepoSet (`internal/git/repset.go`, new)

- `RepoSet{user, token string; push bool; mu; cache map[string]*repoPair}` where
  `repoPair{read *Repo; write *WriteRepo}`. `NewRepoSet(forgeUser, forgeToken string, push bool) *RepoSet`.
  - `Get(repoURL string) (*Repo, *WriteRepo, error)`: lazily `Open`/`OpenWrite`
    with the one Forgejo cred; cache by URL. Start the per-repo background poll
    (HeadsSignature) once on first Get (or expose `repoPair.refresh()` for the hub).
- Keep `Repo`/`WriteRepo` as-is. The single-fetcher poll currently lives in main's
  `pollGit(repo,...)`; generalize: RepoSet owns a poll goroutine per open repo that
  signals a shared `changed` chan (passed in).

**Verify:** build/vet.

## Task 31 — Thread identity + project through request path

This is the integration task. Rework `api.Deps` + handlers + coordinator + provider
+ main so every request uses the user identity and resolves project→repo.

- **api.Deps** (new shape): carries the `cluster.Factory`, `argo.Factory`,
  `project.Resolver`, `RepoSet`, `forge.Client`, auth `Authenticator`, config
  (baseBranch, proposed, runningBranch, draftDir), and `Stream`/`VNC`. Drop the old
  single Inventory/Options/Draft interfaces in favor of a request-scoped service
  built per call (or keep thin interfaces but pass Identity).
- **Inventory** (`GET /api/inventory`, no branch param now — or `?branch=running`
  optional per project): handler → Identity → `userCluster := factory.For(token)`
  → `projects := resolver.Map(ctx, userCluster)` → for each project with a Repo:
  `read,_ := repoSet.Get(repo)`; build that project's namespaces' VMs from the repo
  (filter to the project's namespaces), enrich with `userCluster.LiveState`, attach
  drift from `userArgo.VMDrift`. Assemble `model.Inventory{Projects}`. Provider's
  Inventory becomes a helper that takes (repo, namespaces, enricher, drift) → for
  one project; or move this assembly into a new `inventory` builder func.
- **Options**: `userCluster.ListOptions(ctx)`.
- **Edit/stage/create/adopt/drift/resync**: handler resolves the VM's namespace →
  project (via resolver) → repo. `changeset.Coordinator` becomes per-call or
  project-parameterized: methods take `(identity, project ProjectInfo, ...)`.
  Reads use userCluster; commits/PRs use `repoSet.Get(project.Repo).write` +
  project base/proposed branches; resync uses argo SA client.
- **VNC**: `userCluster.VNCConn(ns,name)`.
- **main.go**: build factories, resolver, repoSet, authenticator; mount
  auth.Middleware around the api mux; start SA-identity background watches +
  per-project exporters (see Task 32 note on export). Remove the single
  cluster/argo client + single repo wiring.

**Verify:** build/vet/test; then live: login as kube:admin, GET /api/inventory returns
projects; edit→propose works against a project repo. (Full isolation test in Task 35.)

## Task 32 — Per-user hub + per-(user,project) drafts + per-project export

- **stream.Hub**: `InventoryFunc func(id auth.Identity) (model.Inventory, error)`.
  `subscriber{identity auth.Identity; send; lastJS}`. WS `Handler` resolves Identity
  from the cookie (reuse auth.readCookie + Authenticator.Validate) BEFORE
  registering; close if unauth. `broadcast()` loops subscribers, calls
  inventoryFunc(sub.identity), dedupes per-subscriber. main passes an inventoryFunc
  closure that does the per-user project inventory (same assembly as Task 31).
- **draft.Store**: key by (user, project). Recommend `Store` rooted at a dir:
  `Open(dir)`; methods take `(user, project)`: `Stage(user,project,entry)`,
  `List(user,project)`, `Unstage(user,project,ns,name)`, `Clear(user,project)`,
  and `ListProjects(user)`/`Count(user)` for the badge. Files
  `<dir>/<user>/<project>.json`. Config: `-draft-dir` replaces `-draft-file`.
- **changeset.Coordinator**: pulls user from Identity, project from the VM's
  namespace; draft ops include (user,project); Propose targets project repo.
- **export**: one exporter is no longer right (it wrote ALL cluster VMs to one
  repo). Now: for each KNOWN project (discovered via SA client + resolver.Map with
  SA), export THAT project's namespaces' live VMs to THAT project's repo's running
  branch. Add `export` per-project loop driven by SA identity. (Projects discovered
  periodically with the SA client.)

**Verify:** build/vet/test; live: two sessions get independent inventories;
draft files land per-user/project; export writes per-project running branches.

## Task 33 — Frontend: login + 3-level project tree

- `web/src/lib/api.ts`: add `login(token)`, `logout()`, `me()`; add
  `credentials:'same-origin'` to fetch + ensure WS sends cookie (same-origin, ok).
  Update types: Inventory.projects = Project[]; Project{name,repo,namespaces:
  ProjectNamespace[],error?}; ProjectNamespace{namespace,vms:VM[]}.
- New `Login.svelte`: token textarea + help text (`oc whoami -t` /
  `kubectl create token <sa> -n <ns>`), submit → api.login → on success show app.
- `+page.svelte`: on load call `me()`; if 401 show Login; else show shell with
  "signed in as <user>" + Logout. Any 401 from a call → drop to Login.
- `InventoryTree.svelte`: 3-level Project → Namespace → VM (collapsible each level);
  project node shows repo (or a warning badge if project.error); drift roll-up
  aggregates namespace→project.
- `ChangesPanel.svelte`: group drafts by project; each "Create PR" targets its repo.
- Keep EditSettings/NewVMWizard/Console/drift detail; they operate within the
  visible projects.

**Verify:** `npm run check` (0 errors); `npm run build`.

## Task 34 — Deploy manifests (`deploy/`)

- `deploy/rbac.yaml`: dotvirt ServiceAccount + ClusterRole/Binding:
  `create tokenreviews` (authentication.k8s.io); read namespaces; read
  virtualmachines/virtualmachineinstances (for SA export+watch) in managed ns;
  patch/get applications.argoproj.io (resync). NO Forgejo-admin, NO Argo app create.
- `deploy/applicationset.yaml`: ApplicationSet (cluster/namespace generator over
  `dotvirt.io/project` label) → one Argo Application per project syncing
  `<dotvirt.io/repo>` → that project's namespaces. Document onboarding contract in a
  comment: "label + annotate namespaces; ApplicationSet provisions the Argo app;
  create the Forgejo repo (or a repo-bootstrap job)".
- Update README with the multi-tenant model + onboarding.

## Task 35 — Full multi-tenant verification (live)

Setup: create Forgejo repos `team-a`, `team-b`; seed each with its VMs on main +
running; label+annotate namespaces (`tenant-a`: project=team-a, repo=…/team-a.git;
`tenant-b`: project=team-b, repo=…/team-b.git). Create per-project Argo apps (or via
the ApplicationSet).

Checks (see plan file's Verification section for detail):
1. Token login (kube:admin) + /api/me; garbage token → 401.
2. **Isolation:** SA with VM-read in only tenant-a → sees only project team-a; never
   receives team-b's repo URL in any /api response; edit team-a VM → PR to team-a
   repo; team-b not visible/editable.
3. 3-level tree for admin (team-a, team-b); labeled-but-unannotated ns → project
   shows "no repo" warning.
4. Per-user live: 2 sessions, independent WS inventories; oc patch reflects live.
5. Per-(user,project) drafts isolated; draft files `<dir>/<user>/<project>.json`;
   PR authored as the k8s user.
6. Per-project running + Argo: each repo's running branch updates from SA export;
   merging a team-a PR changes only team-a VMs.
7. Background SA export works with nobody logged in.
8. go build/vet/test + npm check green. Capture UI screenshots (login, 3-level
   tree, isolation as limited user).

Cleanup after: restore any patched VMs; close test PRs; leave services running.

---

## Checklist
- [x] 27 Per-token client factory (cluster + argo)
- [x] 28 Auth: TokenReview + signed cookie middleware
- [x] 29 Project resolver + model Project/ProjectNamespace
- [x] 30 Per-project git RepoSet
- [x] 31 Thread identity + project through request path
- [x] 32 Per-user hub + per-(user,project) drafts + per-project export
- [x] 33 Frontend: login + 3-level project tree
- [x] 34 Deploy manifests: RBAC + ApplicationSet + README
- [x] 35 Full multi-tenant verification (live)
