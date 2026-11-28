# Supported personas

Kubernetes is the authority.
dotvirt shows a person what the apiserver lets them list, and lets them do what it lets them do.
It works with any RBAC.

We test and support the five personas below, on OpenShift and on epheo/microshift.
Custom roles follow the same read rule, but our tests do not cover them.

## The personas

| Persona | Roles | Bound by |
| --- | --- | --- |
| Project viewer | `view` + `dotvirt-tenant-egress-view` | the platform repo, per project namespace |
| Project author | `edit` + `dotvirt-tenant-egress-view` | the platform repo, per project namespace |
| Project admin | `admin` + `dotvirt-tenant-egress` | the platform repo, per project namespace (New Project stages it for owners) |
| Platform network admin | `dotvirt-platform-network-admin` | the operator, to Group `dotvirt-platform-admins` |
| Platform admin | `cluster-admin` | the cluster |

Nothing dotvirt ships is aggregated into `view`, `edit` or `admin`.
A dotvirt role grants only the subjects it is bound to.

## What each gives in dotvirt

- Project viewer: its projects' VMs, networks, policies, egress firewalls, snapshots, events and routes; the repo's history, specs and templates. No console, no power actions, no authoring.
- Project author: the viewer's reads, plus consoles, start and stop, and proposing VMs, networks, policies, snapshots, restores and templates. Reads egress firewalls; cannot change them.
- Project admin: the author's rights, plus proposing egress firewalls. Reads its namespaces' NetworkAttachmentDefinitions; changing one goes through the platform repo.
- Platform network admin: proposes shared networks, uplinks and cluster policies through the platform repo, and reads those kinds.
- Platform admin: everything.

## Why `dotvirt-tenant-egress-view`

`view` and `edit` cannot list EgressFirewalls.
EgressFirewall is a tenant kind, and reading a project's repo needs list on every kind the repo declares.
Without this role, a viewer or author loses the whole repo read the moment their repo declares one egress firewall.

## Granting a project persona

Commit the bindings to the platform repo, beside the project's admin grant.
Membership lives in your identity provider: bind Groups, not Users, where you can.

A viewer group on namespace `team-a-dev` of project `team-a`, in `rbac/team-a-dev/team-a-dev-viewers.yaml`:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: team-a-dev-viewers
  namespace: team-a-dev
  labels:
    dotvirt.io/project: team-a
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: view
subjects:
  - apiGroup: rbac.authorization.k8s.io
    kind: Group
    name: team-a-viewers
```

And its egress firewall reads, in `rbac/team-a-dev/team-a-dev-viewers-egress.yaml`:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: team-a-dev-viewers-egress
  namespace: team-a-dev
  labels:
    dotvirt.io/project: team-a
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: dotvirt-tenant-egress-view
subjects:
  - apiGroup: rbac.authorization.k8s.io
    kind: Group
    name: team-a-viewers
```

An author is the same pair with `edit` in place of `view`.
Bind every namespace of the project: a person who sees part of a project does not read its repo.

## Desktops

Where the cluster serves DeskuBe, a platform admin turns desktops on per project namespace, from the project's Configure tab.
It stages three RoleBindings in the platform repo, beside the namespace's admin grant, which re-renders them:

| RoleBinding | Role | Subjects |
| --- | --- | --- |
| `<ns>-desktops` | `dotvirt-tenant-desktops` | the admin grant's subjects, the project's sync account among them |
| `<ns>-desktops-admin` | `dotvirt-tenant-desktops-admin` | the admin grant's people, never a sync account |
| `<ns>-desktops-view` | `dotvirt-tenant-desktops-view` | the namespace's authors: the subjects of its `edit` grants |

- `dotvirt-tenant-desktops`: pools, desktops made one by one, and the claim verbs, with `use` on pools. DeskuBe's admission asks these of whoever sets a pool's subjects or a desktop's owner, the sync account included.
- `dotvirt-tenant-desktops-admin`: acting on anyone's claim (`assign` on pools).
- `dotvirt-tenant-desktops-view`: reading the desktops. DeskuBe keeps this out of `view`: a desktop carries a pending pairing PIN, and whoever reads it may pair first.

dotvirt ships these roles with DeskuBe's rules, so no binding depends on the name prefix a DeskuBe install chooses.
None grants `*` or `seat`.
DeskuBe aggregates its pool and claim viewer roles into `view`, so every project persona reads pools and claims.

The gap: viewers get no desktop read.
A viewer of a repo that declares desktops one by one cannot list its VdiInstances, so loses that repo's read.
A repo declaring pools alone stays readable to viewers.

## What the platform sync account may bind

With sync impersonation on, Argo applies the platform repo as `dotvirt-platform-sync`.
That account may bind exactly the project personas' roles: `admin`, `dotvirt-tenant-egress`, `view`, `edit`, `dotvirt-tenant-egress-view`, the publishing roles `dotvirt-tenant-routes` and `dotvirt-tenant-gateway-view`, and the three desktop roles (`pkg/tenancy.ProjectGrantRoles`).
A RoleBinding naming any other role holds the impersonation switch back, and the operator names it.
