# dotvirt distribution

A full virtualization solution built from dotvirt: hypervisor OS image,
control plane, storage, and fleet management. The operating model stays
dotvirt's: the entire VM estate lives in git, changes merge as PRs, the
cluster reconciles to git. Positioning in one line: Nutanix-style data
locality and vSphere-style operations on open components, PR-gated in git.

Prior art it deliberately resembles: vCenter appliance (management VM on
the hosts it manages), oVirt hosted-engine (host agents keep that VM
alive), Nutanix (data locality), Proxmox (host autonomy, 1 to N growth).
Harvester is the contrast: 3-node floor, shared-storage-first, no git.

## Decisions

| Area | Decision |
|---|---|
| Topology | One cluster per cell; workers join a single control plane |
| OS | epheo/microshift bootc image, one artifact for hosts and appliance |
| Hypervisor | Upstream KubeVirt + CDI baked into the image (not CNV/HCO) |
| Control plane | VM appliance on host libvirt, never a KubeVirt tenant |
| Storage | Block only; DRBD everywhere; LINSTOR/Piraeus for tenant PVCs |
| Locality | Migrate first, localize in background (locality is an SLO) |
| Scale | Cells of roughly 32 to 64 hosts; dotvirt manages many cells |

## Topology: one cluster, joined workers

A federation of single-node clusters was considered and rejected: it
forfeits the Kubernetes scheduling plane (scheduler, affinity and
anti-affinity, descheduler) and would strand dotvirt's existing DRS,
which already drives in-cluster live migration. One cluster keeps all of
it; every hypervisor is a node.

MicroShift multinode join works mechanically (the upstream contributor
two-node setup) but is officially unsupported. The distribution owns that
surface: join tokens, kubelet cert rotation, node lifecycle. This tooling
lives in the bootc image, not as microshift patches, so the image's
short-and-upstreamable patch policy holds.

Growth path, uniform from 1 to N:

1. Node A installs as a single all-in-one microshift with dotvirt and all
   operators. This is the complete single-box product.
2. To scale: join node B as a worker, move the control plane into the
   appliance VM, switch the VIP, recycle A as a plain worker.
3. Every further host is just a worker join.

Day-1 requirements that are painful to retrofit: a floating apiserver VIP
with its SAN in the serving certs from the very first single-node install
(microshift subjectAltNames), and a decision on control-plane-only mode
(patch microshift, or accept the appliance registering as a tainted
control-plane node; start with the taint, it is patch-free).

## Control plane: a host-level appliance

The control-plane VM is never a KubeVirt tenant of the cluster it
controls. The circularity is not a steady-state problem but a recovery
one: on site-wide cold boot a virt-launcher needs the apiserver that is
inside the VM (deadlock), and on host death nothing exists to reschedule
it while its disk was node-local. Rebuilding a fresh etcd is not an
escape: kubelets reconcile against the empty state and kill every running
pod, meaning every running VM. etcd continuity is what lets workloads
survive management death.

Mechanism: plain qemu-kvm under the modular libvirt daemons (virtqemud),
run as a systemd unit on whichever host holds the disk. The disk is a
hand-managed DRBD resource replicated across two or three workers.
keepalived owns the VIP. A small watchdog agent per host starts the VM
where the replicated disk is primary; DRBD quorum (or a sanlock/virtlockd
lease) prevents two starts. oVirt hosted-engine is the blueprint for the
agent, including its edge cases (maintenance mode, score penalties,
flapping). Planned moves are virsh migrate with a briefly dual-primary
DRBD. Host libvirt coexists cleanly with KubeVirt: virt-launcher ships
its own containerized libvirt; only /dev/kvm is shared.

The appliance is not a self-upgrading bootc system; its OS is derived
content. Two DRBD volumes: a state volume (etcd, /var/lib/microshift,
certs), the only precious data, and an A/B pair of small OS volumes the
host writes from the target image reference (containerized bootc install
to-disk, straight from local container storage, registry-free like the
portail cold-boot trick). Rollback is booting the other slot; a greenboot
health gate inside the VM still applies. One artifact, one version bump:
the appliance flips to the new OS first, because kubelets may lag the
apiserver but never lead it, then hosts roll with bootc switch, draining
VMs through the migrate-first machinery. Keeping the OS volumes on DRBD
preserves virsh migrate; their content stays disposable and regenerable
on any host. The literal sharing alternative (virtiofs root from the
host's own deployment) is rejected: it breaks live migration and couples
the appliance's version to whichever host it happens to run on.

Failure contract, same as vCenter: management down does not touch running
VMs; kubelets and virt-launchers carry on. Failover is a cold start on a
surviving host, minutes of management downtime. MicroShift's
single-member etcd means moving the VM moves etcd with no raft ceremony,
and also means there is no multi-replica control plane; do not attempt
one. The UI surfaces the appliance as a distinct system object (health,
location, last failover), not as a tenant VM.

## Storage: DRBD everywhere, LINSTOR as the Kubernetes face

Block only. One replication engine serves both planes: the appliance uses
a raw DRBD resource outside Kubernetes; tenant PVCs come from
LINSTOR/Piraeus. Ceph is out at this node count (three-monitor floor,
wrong shape for 1 to 3 hosts). Longhorn (migratable RWX block, the
Harvester path) is the documented fallback if the kernel module ever
proves unacceptable; it trades the kmod for a userspace data-path tax.

Live migration uses LINSTOR's documented KubeVirt recipe: volumeMode
Block, accessMode ReadWriteMany, DrbdOptions/Net/allow-two-primaries
"yes". DRBD goes dual-primary only for the migration window.

The scaling model is a fleet of small independent mirrors, not a
distributed pool. Each volume syncs to a fixed set of 2 or 3 peers,
point-to-point; there is no striping, no rebalance storms, and fleet size
is invisible to the data plane. Reads come from the local replica at
native speed; writes pay one RTT (protocol C). Per-volume performance and
size are bounded by a single node; there is no erasure coding or
aggregation. If the LINSTOR controller is down, existing volumes keep
serving and replicating; only provisioning and new attaches wait.

Growth mechanics:

- placeCount 1 on day 1, with the DRBD layer present even for a single
  replica (negligible cost). This is what allows adding replicas to
  existing volumes online later; without it, growth is a migration.
- placeCount 2 at two hosts. Split-brain exposure at exactly two is
  answered by the same tiebreaker as the appliance watchdog.
- placeCount 2 or 3 plus automatic diskless tiebreakers at three and up:
  real quorum.
- Diskless attach lets any node consume any volume over the network, so
  compute placement is never constrained by replica placement.

Costs owned deliberately: DRBD 9 is an out-of-tree kernel module, built
against the pinned kernel in image CI; the vm-test gate catches kernel
bumps. The cost is sunk regardless because the appliance needs DRBD. The
virt flavor drops TopoLVM (already compile-off by default): LINSTOR with
placeCount 1 is the local class, and two storage systems on three nodes
is one too many.

## Data locality: migrate first, localize after

The flagship behavior. Migration is never blocked on storage: if the
target node holds no replica, the CSI driver attaches diskless and the VM
moves immediately, reads temporarily remote. A background repair loop
then restores locality in place: linstor resource toggle-disk converts
the diskless attachment to a diskful replica while the VM keeps running
(DRBD attaches while Primary and fetches cold blocks from peers during
resync), then a far replica is trimmed to restore placeCount. This is the
Nutanix data-locality property on open components.

Consequence: allowRemoteVolumeAccess stays permissive (strict diskful
affinity is incompatible with land-anywhere). Locality is a background
SLO owned by the DRS loop, not a scheduling constraint. For planned DRS
moves the loop may pre-seed instead (create the target replica, wait
UpToDate, migrate, trim the source): same primitives, opposite order.

Guardrails, designed in from the start:

- Hysteresis: localize only after the VM has been stable on its node for
  some minutes; a localization is a full-volume resync, do not waste it.
- Resync is shared: cap DRBD resync rate and queue localizations per
  node, or post-evacuation repair storms degrade tenant I/O.
- Trim failure-domain-aware: never drop the last replica in another rack.
- Never toggle during the dual-primary migration window; one state
  transition per volume at a time.
- The loop is level-triggered and idempotent: observe "VM node holds no
  replica, stable for N minutes", reconcile, restart-safe. Same
  philosophy as dotvirt's eventbus.
- Per-VM locality state in the UI: local, remote-reads, syncing,
  converged.

## Scale: cells

One MicroShift control plane serves tens of hosts, not hundreds; the
single-member etcd in one VM is the honest ceiling. The 400-hypervisor
target is met with cells of roughly 32 to 64 hosts (the vSphere cluster
analog), each cell a complete unit: its own appliance, its own LINSTOR,
its own DRS domain. dotvirt is the manager above all cells; its per-cell
inventory generalizes the existing single-cluster snapshot.

Cross-cell mobility uses KubeVirt decentralized live migration (Tech
Preview): a storage-live-migration variant needing no shared storage,
with no upstream orchestrator yet; the docs enumerate the steps an
orchestrator would perform, and dotvirt becomes that orchestrator.
Known limits to respect: no shareable disks, no virtiofs, no LUN
passthrough, persistent TPM/EFI state needs RWX on both ends, failed
migrations need manual-style cleanup, which the orchestrator owns.

## Install and upgrade experience

One image, three verbs: install, join, merge.

- Install: boot the ISO, unattended install (DHCP, largest disk), the
  console prints the UI URL. First boot is registry-free: KubeVirt,
  LINSTOR, dotvirt and the appliance tooling ship as embedded OCI
  archives, the portail cold-boot trick generalized.
- Join: "Add host" in the UI mints a short-lived token; the new box boots
  the same ISO and picks join at the console prompt (or is discovered on
  the LAN and adopted from the UI). Joining the second host triggers the
  guided appliance move: create the VM, switch the VIP, recycle node A
  as a worker.
- Merge: the fleet's target image digest is a file in the platform repo.
  A new release appears in the UI as an available update; accepting it
  opens a PR, and merging the PR is the upgrade. dotvirt orchestrates:
  appliance OS slot flip first (skew rule), then hosts one at a time:
  cordon, migrate VMs away, bootc switch, reboot, greenboot verdict,
  uncordon. The first host is the canary; a red greenboot halts the
  rollout and bootc rolls that host back. Reverting the PR is the
  fleet-level rollback.

A single host has no spare capacity, so an upgrade there is a scheduled
maintenance window with VM downtime; the UI says so instead of pretending
otherwise. Host-local auto-update timers stay disabled: uncoordinated
reboots break quorum and running VMs; sequencing belongs to the
orchestrator alone.

## Build order

1. OVN-K multinode spike on the microshift image: the highest-risk
   unknown, run it first.
2. Worker join tooling in the bootc image.
3. KubeVirt + CDI opinion in the image (embedded images for cold boot).
4. The appliance: DRBD resource, keepalived, watchdog agent, libvirt
   unit; oVirt hosted-engine source as the reference.
5. The control-plane move as a scripted, rehearsable operation; it
   doubles as the disaster-recovery drill.
6. LINSTOR/Piraeus with the day-1 resource groups (DRBD layer at
   placeCount 1, two-primaries class for VMs).
7. The locality repair loop in dotvirt DRS.
8. Cells: multi-cluster inventory, then decentralized-migration
   orchestration for cross-cell moves.
