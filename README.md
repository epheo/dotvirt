# dotvirt

A vCenter-like WebUI that closes the gap between point-and-click VM operation and
GitOps. dotvirt **edits git repos** of KubeVirt manifests and **works alongside
ArgoCD** — Argo stays the only thing that applies state to the cluster; dotvirt is
the friendly inventory + editor on top of git and Argo's status.

It is **multi-user and multi-tenant as a thin lens that owns nothing**: it rides
the cluster's own authentication and RBAC.

## How isolation works

**Tenant boundary = repo boundary.** Git has no sub-repo read ACLs, so one shared
repo can't be multi-tenant (UI filtering would be security theater). Each project
is its own git repo. Isolation holds at two layers that reinforce each other:

1. **Cluster RBAC** — every read, edit, and console call is made with the *user's*
   own bearer token (pass-through). dotvirt enforces nothing; the API server is the
   sole authority on which namespaces a user can see.
2. **Git** — a user only ever learns a repo URL from a namespace they can already
   see (the URL lives in a namespace annotation), and per-project repos hold no
   cross-tenant data.

**Projects are live cluster facts, not a dotvirt registry.** A project is a set of
namespaces sharing a label, pointing at one git repo via an annotation:

```sh
oc label    ns <ns> dotvirt.io/project=<project>
oc annotate ns <ns> dotvirt.io/repo=https://forge/dotvirt/<project>.git
```

dotvirt reads those with the user's token and assembles a 3-level tree
(**project → namespace → VM**). The only non-cluster input is one Forgejo
credential used to clone/push every repo.

## Auth

Users sign in by pasting a Kubernetes token (`oc whoami -t`, or
`kubectl create token <sa> -n <ns>`). dotvirt validates it with a **TokenReview**
(as its own ServiceAccount) and stores the raw token in a **signed, httpOnly
cookie** — stateless, no server session store. Every subsequent cluster call
re-presents that token. (OIDC and kubeconfig-upload are later ways to obtain the
token; the authz model is unchanged.)

## Architecture

Two independent services:

- **Backend** (`cmd/dotvirt`, Go): serves a JSON API under `/api`, a per-user
  WebSocket (`/api/inventory/stream`) that pushes each caller's own live inventory,
  and a VNC console WebSocket (`/api/vms/{ns}/{name}/vnc`) bridged to KubeVirt's VNC
  subresource (dialed with the user's token). Read sources — git (per-project
  repos), cluster (live VM/VMI state), ArgoCD (drift). Write target — per-project
  git branches. **Never applies to the cluster.**
- **Frontend** (`web/`, SvelteKit + Tailwind): a standalone static SPA.

**Identities in play:** user requests use the user's token; background work
(per-project running-branch export, the cluster-wide change-signal watch, Argo
re-sync) uses dotvirt's own ServiceAccount.

## Develop

```sh
# backend on :8080 (cluster + argo reads; needs a kubeconfig + Forgejo creds)
go run ./cmd/dotvirt -argo \
  -kubeconfig "$HOME/.kube/config" \
  -ui-origin http://localhost:5173
#   DOTVIRT_FORGE_URL / DOTVIRT_FORGE_TOKEN  → Forgejo endpoint + token
#   DOTVIRT_GIT_USERNAME / DOTVIRT_GIT_TOKEN → git clone/push credential

# frontend on :5173 (proxies /api -> :8080, WebSockets included)
cd web && npm install && npm run dev
```

Open http://localhost:5173 and sign in with `oc whoami -t`.

## Build

```sh
go build -o dotvirt ./cmd/dotvirt   # backend binary
cd web && npm run build             # static SPA -> web/build
```

## Deploy

Build the image (multi-stage: the SvelteKit SPA is built static and the Go binary
serves it + `/api` at the same origin):

```
podman build -f Containerfile -t <registry>/dotvirt:tag .
```

Manifests in `deploy/`:

| File | What |
|------|------|
| `rbac.yaml` | dotvirt ServiceAccount + minimal ClusterRole: `create tokenreviews`, read namespaces, read VMs/VMIs (SA export + watch), `patch` Argo Applications (re-sync). **No** Forgejo-admin, **no** Argo-app-create. |
| `dotvirt.yaml` | the dotvirt Deployment + Service (uses the SA; needs a Forgejo-creds secret + a session-secret). |
| `applicationset.yaml` | one Argo Application per project, syncing `<dotvirt.io/repo>` → that project's namespaces. **The only component that creates Argo apps** — dotvirt does not. Driven by a **plugin generator** that calls dotvirt's `/api/v1/getparams.execute` (auth: a shared token), so the project list comes from the namespace labels, not a hardcoded list. |
| `forgejo.yaml` | a self-hosted Forgejo (for evaluation; bring your own git forge in production). |

### Onboarding a project (once per tenant)

1. Create the project's git repo (VMs on `main`, an empty `running` branch).
2. Label + annotate the project's namespace(s) (see *How isolation works*):

   ```
   oc label    ns <ns> dotvirt.io/project=<project>
   oc annotate ns <ns> dotvirt.io/repo=http://forge/dotvirt/<project>.git
   ```

That's it — no manual ApplicationSet edit. The ApplicationSet's plugin generator
re-polls dotvirt (`requeueAfterSeconds`), which emits the labeled namespaces, and
provisions the per-project Argo Application within a minute. dotvirt supplies the
list under a shared token (`DOTVIRT_APPSET_PLUGIN_TOKEN`, matched by the
`dotvirt-appset-plugin` secret) but still never creates the Application itself.

## Configuration (backend flags / env)

| Flag | Env | Default | Purpose |
|------|-----|---------|---------|
| `-addr` | `DOTVIRT_ADDR` | `:8080` | HTTP listen address |
| `-ui-origin` | `DOTVIRT_UI_ORIGIN` | `http://localhost:5173` | CORS origin for the frontend (empty = same-origin / disabled) |
| `-kubeconfig` | `KUBECONFIG` | in-cluster | cluster access; empty uses the in-cluster SA |
| `-session-secret` | `DOTVIRT_SESSION_SECRET` | random | HMAC key signing the session cookie (set it so sessions survive restarts / span replicas) |
| `-project-label` | `DOTVIRT_PROJECT_LABEL` | `dotvirt.io/project` | namespace label whose value names the project |
| `-repo-annotation` | `DOTVIRT_REPO_ANNOTATION` | `dotvirt.io/repo` | namespace annotation holding the project's git repo URL |
| `-base-branch` | `DOTVIRT_BASE_BRANCH` | `main` | branch the inventory reads + PRs target |
| `-proposed-branch` | `DOTVIRT_PROPOSED_BRANCH` | `dotvirt/proposed` | working branch holding a draft changeset |
| `-running-branch` | `DOTVIRT_RUNNING_BRANCH` | `running` | per-project branch reflecting live cluster state (dotvirt-owned) |
| `-draft-dir` | `DOTVIRT_DRAFT_DIR` | `./.dotvirt-drafts` | root for persisted drafts (`<dir>/<user>/<project>.json`) |
| `-git-username` / `-git-token` | `DOTVIRT_GIT_USERNAME` / `DOTVIRT_GIT_TOKEN` | — | the one credential used to clone/push every project repo |
| `-forge-url` / `-forge-token` | `DOTVIRT_FORGE_URL` / `DOTVIRT_FORGE_TOKEN` | — | Forgejo endpoint + token for PR creation (owner/repo are derived per project from the repo URL) |
| `-push` | `DOTVIRT_PUSH` | `true` | push commits to the remote (disable for local/offline testing) |
| `-argo` | `DOTVIRT_ARGO` | `false` | enable ArgoCD drift reads + re-sync |
| `-export-interval` | — | `30s` | how often the SA exports live state to each project's running branch |
| `-git-poll-interval` | — | `10s` | how often to poll git for branch changes (git has no watch) |
| `-insecure-tls` | `DOTVIRT_INSECURE_TLS` | `false` | skip TLS verification for git + forge (dev; e.g. a self-signed Route) |
