// Typed client for the dotvirt backend API. Mirrors internal/model.
//
// Every request is identity-scoped: a signed session cookie (set by login) is
// sent with each fetch + on the WebSocket handshake, and the backend resolves the
// caller's projects from the cluster. credentials:'same-origin' ensures the cookie
// rides cross-origin in dev (Vite proxy) and same-origin in production.

export type Power = 'On' | 'Off' | 'Unknown';
export type SyncStatus = 'Synced' | 'OutOfSync' | 'NotTracked' | 'Unknown';

export interface Disk {
	name: string;
	type?: string;
	size?: string; // emptyDisk capacity or dataVolume requested storage
	storageClass?: string; // dataVolume storageClassName (empty = cluster default)
}
export interface NIC {
	name: string;
	network?: string;
	mac?: string; // live, from VMI status
	ip?: string; // live, from VMI status
}

export interface VM {
	namespace: string;
	name: string;
	power: Power;
	cpuCores?: number;
	memory?: string;
	instancetype?: string;
	preference?: string;
	labels?: Record<string, string>;
	disks?: Disk[];
	networks?: NIC[];
	sourceFile: string;
	phase?: string;
	paused?: boolean; // VMI Paused condition (phase stays Running)
	guestIP?: string;
	ips?: string[]; // every guest-reported IP
	nodeName?: string;
	os?: string; // guest-agent OS, e.g. "Fedora Linux 40 (Cloud Edition)"
	memoryActual?: string; // current guest memory (hotplug-aware)
	startedAt?: string; // RFC3339; VMI entered Running (for uptime)
	migration?: Migration; // live (or last) node-to-node move
	sync: SyncStatus;
	health?: string;
	syncError?: string; // ArgoCD apply failure (e.g. a webhook rejection) when OutOfSync
}

export interface ProjectNamespace {
	namespace: string;
	vms: VM[];
}

export interface Project {
	name: string;
	repo?: string;
	namespaces: ProjectNamespace[];
	error?: string;
}

export interface Inventory {
	projects: Project[];
	warnings?: string[]; // non-fatal degradations (e.g. live/sync status unavailable)
	proposals?: Proposal[]; // open PRs across the caller's projects, streamed live
}

export interface User {
	username: string;
	groups: string[];
}

// Unauthorized is thrown when a call returns 401, so a caller can suppress its
// own error rendering; the sign-out itself is handled centrally (below).
export class Unauthorized extends Error {
	constructor() {
		super('unauthorized');
		this.name = 'Unauthorized';
	}
}

// The one signed-out sink: every 401 funnels through req(), so the page
// registers a single handler here instead of each fetching component
// remembering to report it. The WebSocket paths (streamInventory, VNC) don't
// go through req and take their own onUnauthorized callback.
let unauthorizedSink: (() => void) | undefined;
export function onUnauthorized(fn: () => void) {
	unauthorizedSink = fn;
}

async function req<T>(path: string, init?: RequestInit): Promise<T> {
	const res = await fetch(path, { credentials: 'same-origin', ...init });
	if (res.status === 401) {
		unauthorizedSink?.();
		throw new Unauthorized();
	}
	if (!res.ok) throw new Error(`${path}: ${res.status} ${await res.text()}`);
	if (res.status === 204) return undefined as T;
	return res.json() as Promise<T>;
}

function get<T>(path: string): Promise<T> {
	return req<T>(path);
}

function post<T>(path: string, body: unknown): Promise<T> {
	return req<T>(path, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify(body)
	});
}

function del(path: string): Promise<void> {
	return req<void>(path, { method: 'DELETE' });
}

export interface EditRequest {
	sourceFile: string;
	power?: Power;
	cpuCores?: number;
	memory?: string;
	instancetype?: string;
	preference?: string;
	// Which representation owns CPU/memory. The two are mutually exclusive in
	// KubeVirt, so the backend strips the other when this is set.
	sizing?: 'instancetype' | 'custom';
	setLabels?: Record<string, string>;
	removeLabels?: string[];
	addDisks?: { name: string; size: string }[];
	removeDisks?: string[];
	addNetworks?: { name: string }[];
	removeNetworks?: string[];
	message?: string;
}

export interface Instancetype {
	name: string;
	cpu: number;
	memory: string;
}
export interface Preference {
	name: string;
	displayName?: string;
}
export interface OSImage {
	name: string;
	namespace: string;
	ready: boolean;
}
export interface NetworkOption {
	name: string;
	namespace: string;
}
export interface StorageClass {
	name: string;
	default?: boolean; // the cluster's default class
}
export interface Options {
	instancetypes: Instancetype[];
	preferences: Preference[];
	osImages: OSImage[];
	networks: NetworkOption[];
	storageClasses: StorageClass[];
}

// --- Networks (the vCenter "Distributed Port Group" abstraction) ---
export type NetworkKind = 'default' | 'internal' | 'vlan';
export type NetworkScope = 'project' | 'shared';

export interface Network {
	name: string; // the port-group name shown to the user
	kind: NetworkKind; // default ("VM Network") | internal | vlan
	scope: NetworkScope; // project | shared
	namespace?: string; // project-scoped (UDN/NAD)
	vlan?: number;
	subnets?: string[];
	uplink?: string; // physicalNetworkName (vlan kind)
	attachRef?: string; // "namespace/nad" (CUDN: bare name, resolved at attach)
	backing: string; // UserDefinedNetwork | ClusterUserDefinedNetwork | NetworkAttachmentDefinition
	topology?: string; // raw OVN-K topology, for the detail drawer
	namespaces?: string[]; // for shared (CUDN) nets: where it's attachable; empty for project nets
}
export interface Uplink {
	name: string; // physicalNetworkName
	bridge: string; // OVS bridge (br-ex, br-physnet…)
	builtin?: boolean; // the default br-ex uplink
	nodes?: string[];
	nodeCount: number;
	ports?: string[];
	vlans?: number[];
	status?: string;
}
export interface PhysicalAdapter {
	name: string;
	node: string;
	type?: string; // ethernet | bond
	mac?: string;
	state?: string; // up | down
	mtu?: number;
	role?: string; // cluster-uplink | enslaved | available
}
export interface NetworkInventory {
	networks: Network[];
	uplinks: Uplink[];
	physicalAdapters: PhysicalAdapter[];
	nmstatePresent: boolean;
	canManage: boolean; // caller may author platform-tier networking (CUDN/uplink/namespace)
}

export interface CreateVMRequest {
	name: string;
	namespace: string;
	instancetype: string;
	preference: string;
	osImage: { name: string; namespace: string };
	diskSize?: string;
	storageClass?: string; // root disk class; empty = cluster default
	running: boolean;
	cloudInit?: { user?: string; password?: string; sshKey?: string; extraUserData?: string };
	extraDisks?: { name: string; size: string }[];
	networks?: { name: string }[];
	labels?: Record<string, string>;
}

// --- Draft changeset types ---

export interface Change {
	field: string;
	action: 'change' | 'add' | 'remove';
	from?: string;
	to?: string;
}
export interface DraftItem {
	kind: 'edit' | 'create' | 'delete';
	resource?: string; // '' == vm | network — disambiguates unstage
	namespace: string;
	name: string;
	changes: Change[];
	yaml?: string;
}

export interface NetworkCreate {
	name: string;
	scope?: string; // 'project' (namespace UDN, tenant) | 'shared'/'vlan' (CUDN, platform-routed by kind)
	namespace?: string; // project scope
	subnets?: string[];
	vlan?: number; // vlan scope
	physicalNetwork?: string; // vlan scope: the uplink's physical-network name
	namespaces?: string[]; // shared/vlan scope: namespaces the CUDN publishes to
}
export interface UplinkCreate {
	name: string; // physical-network name
	nic: string; // physical port to enslave
	bridge?: string; // OVS bridge; default br-<name>
	nodeSelector?: Record<string, string>; // node labels; omit = all workers, or {kubernetes.io/hostname: <node>}
}
export interface NamespaceCreate {
	name: string;
	project: string; // the project the namespace joins (its repo)
	vmNetwork?: { name: string; subnet?: string }; // optional primary (Layer2) UDN; subnet required server-side (primary = IPAM)
}
export interface ProjectCreate {
	name: string; // project name → tenant repo + dotvirt.io/project label
	namespace?: string; // first namespace; defaults to name
	owners?: string[]; // usernames granted namespace-admin on the first namespace
	vmNetwork?: { name: string; subnet?: string }; // optional primary (Layer2) UDN on that namespace
}
export interface DraftView {
	base: string;
	branch: string;
	count: number;
	items: DraftItem[];
}
export interface ProposeResult {
	branch: string;
	pushed: boolean;
	prURL?: string;
	prNumber?: number;
	compareURL?: string;
	existing?: boolean;
}
export interface DriftResult {
	drift: boolean;
	changes: Change[];
}
export interface Proposal {
	project: string;
	prNumber: number;
	prURL: string;
	title?: string;
}
export interface Commit {
	hash: string;
	shortHash: string;
	message: string;
	author: string;
	when: string; // RFC3339
	merge?: boolean; // a merge commit (not directly revertable)
}
export interface MetricSeries {
	name: string;
	values: (number | null)[]; // aligned to the chart's times; null = gap
}
export interface MetricChart {
	key: string;
	title: string;
	unit: string; // '%' | 'bytes' | 'Bps' | 'iops' | 'ms'
	stacked?: boolean; // series partition a whole; render as stacked area
	times: number[]; // unix seconds, shared x-axis
	series: MetricSeries[];
}
export interface VMMetrics {
	range: string;
	stepSec: number;
	charts: MetricChart[];
}
// The Performance views' range tiers (vCenter's real-time/day/week/month).
export const METRIC_RANGES = [
	{ key: '1h', label: 'Real-time' },
	{ key: '1d', label: 'Day' },
	{ key: '1w', label: 'Week' },
	{ key: '1mo', label: 'Month' }
] as const;
export interface UsageMetric {
	used: number;
	total?: number; // 0/undefined ⇒ no denominator
	spark?: number[];
}
export interface VMUsage {
	updated: number; // unix seconds
	cpu: UsageMetric; // used = % of allocated, total = 100
	memory: UsageMetric; // bytes
	storage: UsageMetric; // bytes
}
export interface ClusterMetric {
	used: number;
	allocated?: number; // committed to VMs
	total: number; // node-allocatable capacity
	spark?: number[];
}
export interface ConsumerVM {
	namespace: string;
	name: string;
	value: number;
}
export interface ClusterSummary {
	updated: number;
	cpu: ClusterMetric; // cores
	memory: ClusterMetric; // bytes
	storage: ClusterMetric; // bytes
	vms: Record<string, number>; // phase → count
	topCpu: ConsumerVM[];
	topMemory: ConsumerVM[];
}
export interface Snapshot {
	name: string;
	created?: string;
	phase?: string; // InProgress | Succeeded | Failed
	readyToUse: boolean;
	indications?: string[]; // Online | GuestAgent | NoGuestAgent
	error?: string;
}
// A VirtualMachineClone sourced from a VM; the target VM lands cluster-only
// (NotTracked) until adopted into git.
export interface Clone {
	name: string;
	target: string;
	phase?: string; // SnapshotInProgress | RestoreInProgress | CreatingTargetVM | Succeeded | Failed
	created?: string;
}
export interface VMEvent {
	namespace?: string;
	name?: string;
	type: string; // Normal | Warning
	reason: string;
	message: string;
	count?: number;
	object: string; // VirtualMachine | VirtualMachineInstance
	lastSeen?: string;
}

// A VM's live (or last) node-to-node move; active while neither flag is set.
export interface Migration {
	sourceNode?: string;
	targetNode?: string;
	startedAt?: string;
	endedAt?: string;
	completed?: boolean;
	failed?: boolean;
}

// One resource row of a ResourceQuota, pre-parsed for the capacity bars.
export interface QuotaItem {
	resource: string; // e.g. requests.cpu, requests.memory
	used: number;
	hard: number;
	unit: 'cores' | 'bytes' | 'count';
}
export interface NamespaceQuota {
	namespace: string;
	name: string;
	items: QuotaItem[];
}

// One firing Prometheus alert (the dock's Alarms tab).
export interface Alert {
	name: string;
	severity?: string;
	namespace?: string;
	vm?: string;
	count?: number; // collapsed identical series
}

// Image-upload flow (the OVF-import analog): dotvirt mints the target + token,
// the browser streams the image straight to cdi-uploadproxy.
export interface UploadTarget {
	namespace: string;
	name: string;
}
export interface UploadStatus {
	phase: string; // Pending | UploadScheduled | UploadReady | Succeeded | Failed | …
	ready: boolean; // UploadReady — the proxy will accept bytes
	progress?: string; // CDI import progress, once bytes flow
}
export interface UploadToken {
	token: string;
	uploadUrl: string; // the cdi-uploadproxy endpoint the browser POSTs to
}

// A node's maintenance state for the By-Node view.
export interface NodeInfo {
	name: string;
	unschedulable: boolean;
	canCordon: boolean; // the caller's token may cordon it
}

// The caller's effective capabilities in one namespace (the Permissions tab).
export interface Capability {
	id: string;
	label: string;
	allowed: boolean;
	detail?: string;
}
export interface Permissions {
	namespace: string;
	capabilities: Capability[];
	incomplete?: boolean;
}

const enc = encodeURIComponent;

// A container-scope read's query params (the project/namespace/node levels).
export type ScopeQuery = { project?: string; namespace?: string; node?: string };

// scopeQS builds the `?project=&namespace=&node=` suffix for a scope read,
// omitting empty levels; returns '' when nothing is set. extra appends
// additional params (e.g. range).
function scopeQS(scope: ScopeQuery, extra?: Record<string, string>): string {
	const q = new URLSearchParams();
	if (scope.project) q.set('project', scope.project);
	if (scope.namespace) q.set('namespace', scope.namespace);
	if (scope.node) q.set('node', scope.node);
	for (const [k, v] of Object.entries(extra ?? {})) q.set(k, v);
	const qs = q.toString();
	return qs ? `?${qs}` : '';
}

export const api = {
	// Auth
	login: (token: string) => post<User>('/api/login', { token }),
	logout: () => post<void>('/api/logout', {}),
	me: () => get<User>('/api/me'),

	inventory: () => get<Inventory>('/api/inventory'),
	options: () => get<Options>('/api/options'),
	networks: () => get<NetworkInventory>('/api/networks'),

	// Commit history + per-commit revert (a forward commit opened as a PR).
	history: (project: string) => get<Commit[]>(`/api/projects/${enc(project)}/history`),
	revert: (project: string, hash: string) =>
		post<ProposeResult>(`/api/projects/${enc(project)}/revert`, { hash }),

	// Staging — the backend resolves the project from the VM's namespace, so these
	// per-VM routes need no project param.
	stageEdit: (namespace: string, name: string, req: EditRequest) =>
		post<DraftView>(`/api/vms/${enc(namespace)}/${enc(name)}/edit`, req),
	stageCreate: (req: CreateVMRequest) => post<DraftView>('/api/vms', req),
	createNetwork: (req: NetworkCreate) => post<DraftView>('/api/networks', req),
	createUplink: (req: UplinkCreate) => post<DraftView>('/api/uplinks', req),
	createNamespace: (req: NamespaceCreate) => post<DraftView>('/api/namespaces', req),
	createProject: (req: ProjectCreate) => post<DraftView>('/api/projects', req),
	stageDelete: (namespace: string, name: string) =>
		post<DraftView>(`/api/vms/${enc(namespace)}/${enc(name)}/delete`, {}),
	unstage: (namespace: string, name: string, resource?: string, project?: string) => {
		const q = new URLSearchParams();
		if (resource) q.set('resource', resource);
		if (project) q.set('project', project); // cluster-scoped entries resolve by project
		const qs = q.toString();
		return del(`/api/draft/${enc(namespace)}/${enc(name)}${qs ? `?${qs}` : ''}`);
	},

	// Whole-draft ops are scoped to a project (?project=), since they aren't tied
	// to one VM namespace.
	getDraft: (project: string) => get<DraftView>(`/api/draft?project=${enc(project)}`),
	discardDraft: (project: string) => del(`/api/draft?project=${enc(project)}`),
	propose: (project: string, title: string, message: string) =>
		post<ProposeResult>(`/api/draft/propose?project=${enc(project)}`, { title, message }),

	// Drift + reconcile for one VM (project resolved from the namespace).
	drift: (namespace: string, name: string) =>
		get<DriftResult>(`/api/vms/${enc(namespace)}/${enc(name)}/drift`),
	events: (namespace: string, name: string) =>
		get<VMEvent[]>(`/api/vms/${enc(namespace)}/${enc(name)}/events`),
	allEvents: () => get<VMEvent[]>('/api/events'),
	permissions: (namespace: string) =>
		get<Permissions>(`/api/permissions?namespace=${enc(namespace)}`),
	metrics: (namespace: string, name: string, range: string) =>
		get<VMMetrics>(`/api/vms/${enc(namespace)}/${enc(name)}/metrics?range=${enc(range)}`),
	vmUsage: (namespace: string, name: string) =>
		get<VMUsage>(`/api/vms/${enc(namespace)}/${enc(name)}/usage`),
	clusterSummary: (scope: ScopeQuery = {}) =>
		get<ClusterSummary>(`/api/metrics/cluster${scopeQS(scope)}`),
	scopeMetrics: (scope: ScopeQuery, range: string) =>
		get<VMMetrics>(`/api/metrics/scope${scopeQS(scope, { range })}`),
	alarms: () => get<Alert[]>('/api/alarms'),
	// Node maintenance (cluster-scoped; the user's token is the gate).
	nodeInfo: (node: string) => get<NodeInfo>(`/api/nodes/${enc(node)}`),
	setNodeCordon: (node: string, unschedulable: boolean) =>
		post<void>(`/api/nodes/${enc(node)}/cordon`, { unschedulable }),

	// Image upload: create the target DataVolume + mint a token; the browser
	// then streams the file straight to the proxy (uploadUrl from uploadToken).
	createUpload: (req: { namespace: string; name: string; size: string; storageClass?: string }) =>
		post<UploadTarget>('/api/uploads', req),
	uploadStatus: (namespace: string, name: string) =>
		get<UploadStatus>(`/api/uploads/${enc(namespace)}/${enc(name)}`),
	uploadToken: (namespace: string, name: string) =>
		post<UploadToken>(`/api/uploads/${enc(namespace)}/${enc(name)}/token`, {}),
	quotas: (scope: ScopeQuery) => get<NamespaceQuota[]>(`/api/quotas${scopeQS(scope)}`),
	adopt: (namespace: string, name: string) =>
		post<DraftView>(`/api/vms/${enc(namespace)}/${enc(name)}/adopt`, {}),
	// Bulk: stage every untracked (NotTracked) VM in a namespace into one draft.
	adoptNamespace: (namespace: string) =>
		post<DraftView>(`/api/namespaces/${enc(namespace)}/adopt`, {}),
	// Wire a repo to an existing labeled-but-repoless project (the "no repo" dead-end).
	adoptProject: (project: string, owners?: string[]) =>
		post<DraftView>(`/api/projects/${enc(project)}/adopt`, owners?.length ? { owners } : {}),
	resync: (namespace: string, name: string) =>
		post<{ application: string; revision: string }>(
			`/api/vms/${enc(namespace)}/${enc(name)}/resync`,
			{}
		),

	// Clone (imperative create; the target VM lands NotTracked until adopted).
	clones: (namespace: string, name: string) =>
		get<Clone[]>(`/api/vms/${enc(namespace)}/${enc(name)}/clones`),
	createClone: (namespace: string, name: string, target: string) =>
		post<{ name: string; target: string }>(`/api/vms/${enc(namespace)}/${enc(name)}/clone`, {
			target
		}),

	// Snapshots (imperative, RBAC-gated; not git-managed).
	snapshots: (namespace: string, name: string) =>
		get<Snapshot[]>(`/api/vms/${enc(namespace)}/${enc(name)}/snapshots`),
	takeSnapshot: (namespace: string, name: string, snapName?: string) =>
		post<{ name: string }>(`/api/vms/${enc(namespace)}/${enc(name)}/snapshots`, {
			name: snapName ?? ''
		}),
	restoreSnapshot: (namespace: string, name: string, snap: string) =>
		post<void>(`/api/vms/${enc(namespace)}/${enc(name)}/snapshots/${enc(snap)}/restore`, {}),
	deleteSnapshot: (namespace: string, name: string, snap: string) =>
		del(`/api/vms/${enc(namespace)}/${enc(name)}/snapshots/${enc(snap)}`),

	// Imperative runtime ops (RBAC-gated; don't touch the git-managed spec).
	restart: (namespace: string, name: string) =>
		post<void>(`/api/vms/${enc(namespace)}/${enc(name)}/restart`, {}),
	migrate: (namespace: string, name: string) =>
		post<void>(`/api/vms/${enc(namespace)}/${enc(name)}/migrate`, {}),
	pause: (namespace: string, name: string) =>
		post<void>(`/api/vms/${enc(namespace)}/${enc(name)}/pause`, {}),
	unpause: (namespace: string, name: string) =>
		post<void>(`/api/vms/${enc(namespace)}/${enc(name)}/unpause`, {})
};

// draftsByProject fetches the draft for each named project and returns the
// non-empty ones, for the Changes panel + header badge. Projects with no repo are
// skipped (they can't hold a draft).
export async function draftsByProject(
	projects: string[]
): Promise<{ project: string; draft: DraftView }[]> {
	const results = await Promise.all(
		projects.map(async (project) => {
			try {
				return { project, draft: await api.getDraft(project) };
			} catch (e) {
				if (e instanceof Unauthorized) throw e;
				return null;
			}
		})
	);
	return results.filter(
		(r): r is { project: string; draft: DraftView } => !!r && r.draft.count > 0
	);
}

/**
 * streamInventory subscribes to the caller's live inventory over WebSocket. The
 * session cookie rides the handshake (same-origin), so the server pushes only the
 * caller's tree. Calls onInventory on each push, auto-reconnects with backoff, and
 * invokes onUnauthorized if the handshake is rejected (expired session). Returns a
 * function to close the subscription.
 */
export function streamInventory(
	onInventory: (inv: Inventory) => void,
	onUnauthorized?: () => void
): () => void {
	let ws: WebSocket | null = null;
	let closed = false;
	let retry = 0;
	let reconnectTimer: ReturnType<typeof setTimeout> | undefined;
	let everOpen = false;

	const url = () => {
		const proto = location.protocol === 'https:' ? 'wss' : 'ws';
		return `${proto}://${location.host}/api/inventory/stream`;
	};

	const connect = () => {
		if (closed) return;
		everOpen = false;
		ws = new WebSocket(url());
		ws.onopen = () => {
			everOpen = true;
			retry = 0;
		};
		ws.onmessage = (e) => {
			try {
				onInventory(JSON.parse(e.data) as Inventory);
			} catch {
				/* ignore malformed frame */
			}
		};
		const scheduleReconnect = () => {
			if (closed) return;
			retry = Math.min(retry + 1, 6);
			reconnectTimer = setTimeout(connect, 500 * 2 ** (retry - 1)); // 0.5s..16s backoff
		};
		ws.onclose = () => {
			if (closed) return;
			if (everOpen) {
				scheduleReconnect();
				return;
			}
			// A close before the socket ever opened can't expose the handshake status
			// (the WS API hides it). It's EITHER an expired session (401 on upgrade) OR
			// a transient failure (backend restart, blip). Don't assume 401 — probe the
			// session: only sign out if it's genuinely gone, otherwise reconnect. This
			// stops every deploy/blip from bouncing valid users to login.
			api
				.me()
				.then(() => {
					if (closed) return; // torn down while probing → do nothing
					scheduleReconnect(); // session still valid → it was transient
				})
				.catch((e) => {
					if (closed) return; // torn down while probing → don't sign out a dead subscription
					if (e instanceof Unauthorized) onUnauthorized?.();
					else scheduleReconnect();
				});
		};
		ws.onerror = () => ws?.close();
	};

	connect();

	return () => {
		closed = true;
		clearTimeout(reconnectTimer);
		ws?.close();
	};
}
