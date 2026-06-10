# dotvirt

A vCenter-like WebUI that closes the gap between point-and-click VM operation and
GitOps. Dotvirt **edits a git repo** of KubeVirt manifests and **works alongside
ArgoCD** — Argo stays the only thing that applies state to the cluster; dotvirt is
the friendly inventory + editor on top of git and Argo's status.

## Architecture

Two independent services:

- **Backend** (`cmd/dotvirt`, Go): serves a JSON API under `/api`, a WebSocket
  (`/api/inventory/stream`) that pushes live inventory, and a VNC console
  WebSocket (`/api/vms/{ns}/{name}/vnc`) bridged to KubeVirt's VNC subresource.
  Three read sources — git (manifests), cluster (live VM/VMI state via watches),
  ArgoCD (drift via watches). One write target — git feature branches. Never
  applies to the cluster.
- **Frontend** (`web/`, SvelteKit + Tailwind): a standalone static SPA. In dev it
  proxies `/api` to the backend; in prod it's served from any static host or a
  small nginx container that reverse-proxies `/api`.

## Develop

Run the two services in separate terminals:

```sh
# backend on :8080
go run ./cmd/dotvirt -repo <git-url-or-path> -ui-origin http://localhost:5173

# frontend on :5173 (proxies /api -> :8080)
cd web && npm install && npm run dev
```

Open http://localhost:5173.

## Build

```sh
go build -o dotvirt ./cmd/dotvirt   # backend binary
cd web && npm run build             # static SPA -> web/build
```

## Configuration (backend flags / env)

| Flag | Env | Default | Purpose |
|------|-----|---------|---------|
| `-addr` | `DOTVIRT_ADDR` | `:8080` | HTTP listen address |
| `-ui-origin` | `DOTVIRT_UI_ORIGIN` | `http://localhost:5173` | CORS origin for the frontend (empty disables) |
| `-repo` | `DOTVIRT_REPO` | — (required) | git repo URL or local path |
| `-running-branch` | `DOTVIRT_RUNNING_BRANCH` | `running` | branch reflecting live cluster state (dotvirt-owned) |
| `-push` | `DOTVIRT_PUSH` | `true` | push commits to the remote (disable for local/offline testing) |
| `-glob` | `DOTVIRT_GLOB` | `**/*.yaml` | glob selecting VM manifests |
| `-namespace-label` | `DOTVIRT_NAMESPACE_LABEL` | `dotvirt.io/project` | label selector for project namespaces (empty = all namespaces with VMs) |
| `-kubeconfig` | `KUBECONFIG` | in-cluster | cluster access (read-only) |
| `-cluster` | `DOTVIRT_CLUSTER` | `false` | enable live cluster reads + running-branch export |
| `-export-interval` | — | `30s` | how often to export live state to the running branch |
| `-git-poll-interval` | — | `10s` | how often to poll git for branch changes (drives live push; git has no watch) |
| `-argo` | `DOTVIRT_ARGO` | `false` | enable ArgoCD drift reads |
