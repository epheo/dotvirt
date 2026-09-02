// Typed client for the dotvirt backend API.
//
// Types come from model.gen.ts (tygo output of internal/model - regenerate with
// `make types`). The overlays below shadow the star re-export only where the
// generated shape is too loose (string where the wire carries a closed union)
// or wrong (Go []*float64 marshals null, not undefined). Request types with no
// internal/model counterpart (decoded server-side into netgen/vmgen/drsgen
// specs) stay hand-written.
//
// Every request is identity-scoped: a signed session cookie (set by login) is
// sent with each fetch + on the WebSocket handshake, and the backend resolves the
// caller's projects from the cluster. credentials:'same-origin' ensures the cookie
// rides cross-origin in dev (Vite proxy) and same-origin in production.

import type * as gen from './model.gen';

export type * from './model.gen';
export type { Event as VMEvent, Node as NodeTarget } from './model.gen';

// --- Narrowing overlays (locally declared exports win over the star re-export) ---

export type Power = 'On' | 'Off' | 'Unknown';
export type SyncStatus = 'Synced' | 'OutOfSync' | 'NotTracked' | 'Pending' | 'Unknown';
export type NetworkKind = 'default' | 'internal' | 'vlan';
export type NetworkScope = 'project' | 'shared';
export type PolicyKind = 'dfw' | 'admin' | 'baseline' | 'gateway' | 'egressip' | 'route';
export type DRSMode = 'Predictive' | 'Automatic';

export interface PlacementGroup extends Omit<gen.PlacementGroup, 'mode'> {
	mode: 'together' | 'apart';
}
export interface VMScheduling extends Omit<gen.VMScheduling, 'groups'> {
	groups?: PlacementGroup[];
}
export interface VM extends Omit<gen.VM, 'power' | 'sync' | 'scheduling'> {
	power: Power;
	sync: SyncStatus;
	scheduling?: VMScheduling;
}
export interface ProjectSync extends Omit<gen.ProjectSync, 'sync'> {
	sync?: SyncStatus;
}
export interface ProjectNamespace extends Omit<gen.ProjectNamespace, 'vms'> {
	vms: VM[];
}
export interface Project extends Omit<gen.Project, 'namespaces' | 'gitOps'> {
	namespaces: ProjectNamespace[];
	gitOps?: ProjectSync;
}
export interface Inventory extends Omit<gen.Inventory, 'projects'> {
	projects: Project[];
}

export interface Change extends Omit<gen.Change, 'action'> {
	action: 'change' | 'add' | 'remove';
}
export interface DraftItem extends Omit<gen.DraftItem, 'kind' | 'changes'> {
	kind: 'edit' | 'create' | 'delete';
	changes: Change[];
}
export interface DraftView extends Omit<gen.DraftView, 'items'> {
	items: DraftItem[];
}
export interface TaskEntry extends Omit<gen.TaskEntry, 'kind'> {
	kind: 'op' | 'merge';
}
export interface DriftResult extends Omit<gen.DriftResult, 'changes'> {
	changes: Change[];
}

// Go []*float64 marshals a nil as JSON null; the generated type says undefined.
export interface MetricSeries extends Omit<gen.MetricSeries, 'values'> {
	values: (number | null)[]; // aligned to the chart's times; null = gap
}
export interface MetricChart extends Omit<gen.MetricChart, 'series'> {
	series: MetricSeries[];
}
export interface VMMetrics extends Omit<gen.VMMetrics, 'charts'> {
	charts: MetricChart[];
}

export interface EditRequest extends Omit<gen.VMEdit, 'power' | 'sizing' | 'addGroups'> {
	sourceFile: string;
	power?: Power;
	// Which representation owns CPU/memory. The two are mutually exclusive in
	// KubeVirt, so the backend strips the other when this is set.
	sizing?: 'instancetype' | 'custom';
	addGroups?: PlacementGroup[]; // upsert placement groups
}

// --- Request types with no internal/model counterpart ---

export interface User {
	username: string;
	groups: string[];
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
	extraDisks?: { name: string; size: string; storageClass?: string }[];
	networks?: { name: string }[]; // secondary networks (UDN/localnet)
	primaryNetwork?: boolean; // attach the primary (pod-network) NIC; omitted/true = yes
	labels?: Record<string, string>;
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
// EgressFirewall - a namespace's north-south egress rules (the Tier-1 gateway
// firewall). One per namespace (named "default" server-side); rules are first-match.
export interface EgressFirewallPort {
	protocol: 'TCP' | 'UDP' | 'SCTP';
	port: number;
}
export interface EgressFirewallRule {
	action: 'Allow' | 'Deny';
	cidr?: string; // set exactly one of cidr / dnsName
	dnsName?: string;
	ports?: EgressFirewallPort[];
}
export interface EgressFirewallCreate {
	namespace: string;
	rules: EgressFirewallRule[];
}
// Tier-0 (provider-edge) services - cluster-scoped, platform-routed.
export interface EgressIPCreate {
	name: string;
	egressIPs: string[]; // the source-NAT pool
	namespaces: string[]; // projects it pins egress for
}
export interface ExternalRouteCreate {
	name: string;
	namespaces: string[]; // projects whose egress is steered
	nextHops: string[]; // static external next-hop IPs
}
// Distributed Firewall (east-west) - a NetworkPolicy protecting a Group (a label
// selector) inside one namespace, allowing ingress only from the named peer Groups.
export interface PolicyPort {
	protocol: 'TCP' | 'UDP' | 'SCTP';
	port: number;
}
export interface PolicyRule {
	from?: Record<string, string>[]; // peer Groups (podSelector matchLabels)
	ports?: PolicyPort[];
}
export interface NetworkPolicyCreate {
	name: string;
	namespace: string;
	appliedTo?: Record<string, string>; // the Group this protects; empty = whole namespace
	ingress?: PolicyRule[];
}
// Cluster-wide admin Distributed Firewall - AdminNetworkPolicy (priority + Pass) or
// the BaselineAdminNetworkPolicy default (Allow/Deny only). Platform-tier, admin-only.
export interface AdminPolicyRule {
	action: 'Allow' | 'Deny' | 'Pass';
	peers: Record<string, string>[]; // peer Groups (namespaceSelector matchLabels; {} = all)
	ports?: PolicyPort[];
}
export interface AdminNetworkPolicyCreate {
	name: string;
	baseline?: boolean; // a BaselineAdminNetworkPolicy (the singleton "default")
	priority?: number; // 0..1000, lower = higher precedence (ANP only)
	subject?: Record<string, string>; // namespaceSelector matchLabels; empty = all namespaces
	ingress?: AdminPolicyRule[];
	egress?: AdminPolicyRule[];
}
export interface NamespaceCreate {
	name: string;
	project: string; // the project the namespace joins (its repo)
	vmNetwork?: { name: string; subnet?: string }; // optional primary (Layer2) UDN; subnet required server-side (primary = IPAM)
}
export interface ProjectCreate {
	name: string; // project name -> tenant repo + dotvirt.io/project label
	namespace?: string; // first namespace; defaults to name
	owners?: string[]; // usernames granted namespace-admin on the first namespace
	vmNetwork?: { name: string; subnet?: string }; // optional primary (Layer2) UDN on that namespace
}

export interface DRSEnableRequest {
	mode: DRSMode;
	threshold?: string;
	intervalSeconds?: number;
	softTainter?: boolean;
	evictionNodeLimit?: number;
	evictionTotalLimit?: number;
	installPSI?: boolean; // also stage the worker PSI MachineConfig (reboots workers on merge)
}

// --- DRS vocabulary (mirrored from internal/drsgen, the backend validator) ---

export const DRS_THRESHOLDS = [
	{ value: 'AsymmetricLow', label: 'Conservative', detail: 'move only off clearly hot nodes' },
	{ value: 'Low', label: 'Moderate', detail: '10% deviation from average' },
	{ value: 'Medium', label: 'Eager', detail: '20% deviation from average' },
	{ value: 'High', label: 'Aggressive', detail: '30% deviation from average' },
] as const;
export const DRS_BOUNDS = {
	intervalSeconds: { min: 10, max: 86400 },
	evictionNodeLimit: { min: 1, max: 100 },
	evictionTotalLimit: { min: 1, max: 1000 },
} as const;

export function drsThresholdLabel(value: string): string {
	return DRS_THRESHOLDS.find((t) => t.value === value)?.label ?? value;
}

// The Performance views' range tiers (real-time/day/week/month).
export const METRIC_RANGES = [
	{ key: '1h', label: 'Real-time' },
	{ key: '1d', label: 'Day' },
	{ key: '1w', label: 'Week' },
	{ key: '1mo', label: 'Month' },
] as const;

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

function send<T>(method: 'POST' | 'PUT', path: string, body: unknown): Promise<T> {
	return req<T>(path, {
		method,
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify(body),
	});
}

function post<T>(path: string, body: unknown): Promise<T> {
	return send<T>('POST', path, body);
}

function put<T>(path: string, body: unknown): Promise<T> {
	return send<T>('PUT', path, body);
}

function del(path: string): Promise<void> {
	return req<void>(path, { method: 'DELETE' });
}

const enc = encodeURIComponent;

// vmPath is the one spelling of a VM route's prefix; actions.ts builds its
// manifest/screenshot URLs from it too.
export const vmPath = (namespace: string, name: string) =>
	`/api/vms/${enc(namespace)}/${enc(name)}`;

// qs builds a query-string suffix, omitting empty values; '' when nothing set.
function qs(params: Record<string, string | undefined>): string {
	const q = new URLSearchParams();
	for (const [k, v] of Object.entries(params)) if (v) q.set(k, v);
	const out = q.toString();
	return out ? `?${out}` : '';
}

// A container-scope read's query params (the project/namespace/node levels).
export type ScopeQuery = { project?: string; namespace?: string; node?: string };

// scopeQS is qs over a scope read's levels; extra appends params (e.g. range).
function scopeQS(scope: ScopeQuery, extra?: Record<string, string>): string {
	return qs({ project: scope.project, namespace: scope.namespace, node: scope.node, ...extra });
}

export const api = {
	// Auth
	login: (token: string) => post<User>('/api/login', { token }),
	logout: () => post<void>('/api/logout', {}),
	me: () => get<User>('/api/me'),

	tasks: () => get<TaskEntry[]>('/api/tasks'),
	options: () => get<gen.Options>('/api/options'),
	networks: () => get<gen.NetworkInventory>('/api/networks'),
	policies: () => get<gen.PolicyInventory>('/api/policies'),
	vmPolicy: (namespace: string, name: string) =>
		get<gen.EffectivePolicy>(`${vmPath(namespace, name)}/policy`),
	namespacePolicy: (namespace: string) =>
		get<gen.EffectivePolicy>(`/api/namespaces/${enc(namespace)}/policy`),
	trace: (req: gen.TraceRequest) => post<gen.TraceResult>('/api/networking/trace', req),
	// Which sign-in paths exist (shown on the login screen before any session).
	authMethods: () => get<{ sso: boolean; ssoPending: boolean }>('/api/auth/methods'),
	// Applies the OAuthClient under the CALLER's token; RBAC is the gate.
	finishSSO: () => req<void>('/api/auth/oauthclient', { method: 'POST' }),

	// Commit history + per-commit revert (a forward commit opened as a PR).
	history: (project: string) => get<gen.Commit[]>(`/api/projects/${enc(project)}/history`),
	revert: (project: string, hash: string) =>
		post<gen.ProposeResult>(`/api/projects/${enc(project)}/revert`, { hash }),

	// Staging - the backend resolves the project from the VM's namespace, so these
	// per-VM routes need no project param.
	stageEdit: (namespace: string, name: string, req: EditRequest) =>
		post<DraftView>(`${vmPath(namespace, name)}/edit`, req),
	stageCreate: (req: CreateVMRequest) => post<DraftView>('/api/vms', req),
	createNetwork: (req: NetworkCreate) => post<DraftView>('/api/networks', req),
	createUplink: (req: UplinkCreate) => post<DraftView>('/api/uplinks', req),
	createEgressFirewall: (req: EgressFirewallCreate) => post<DraftView>('/api/egressfirewalls', req),
	createEgressIP: (req: EgressIPCreate) => post<DraftView>('/api/egressips', req),
	createExternalRoute: (req: ExternalRouteCreate) => post<DraftView>('/api/externalroutes', req),
	createNetworkPolicy: (req: NetworkPolicyCreate) => post<DraftView>('/api/networkpolicies', req),
	createAdminNetworkPolicy: (req: AdminNetworkPolicyCreate) =>
		post<DraftView>('/api/adminnetworkpolicies', req),
	createNamespace: (req: NamespaceCreate) => post<DraftView>('/api/namespaces', req),
	createProject: (req: ProjectCreate) => post<DraftView>('/api/projects', req),

	// DRS (platform tier): read the merged git/live view; enable/reconfigure and
	// disable stage into the platform draft like every other cluster-scoped kind.
	drs: () => get<gen.DRSView>('/api/drs'),
	enableDRS: (r: DRSEnableRequest) => post<DraftView>('/api/drs', r),
	disableDRS: () => req<DraftView>('/api/drs', { method: 'DELETE' }),
	stageDelete: (namespace: string, name: string) =>
		post<DraftView>(`${vmPath(namespace, name)}/delete`, {}),
	// project: cluster-scoped entries resolve by project.
	unstage: (namespace: string, name: string, resource?: string, project?: string) =>
		del(`/api/draft/${enc(namespace)}/${enc(name)}${qs({ resource, project })}`),

	// Whole-draft ops are scoped to a project (?project=), since they aren't tied
	// to one VM namespace.
	getDraft: (project: string) => get<DraftView>(`/api/draft?project=${enc(project)}`),
	discardDraft: (project: string) => del(`/api/draft?project=${enc(project)}`),
	propose: (project: string, title: string, message: string) =>
		post<gen.ProposeResult>(`/api/draft/propose?project=${enc(project)}`, { title, message }),

	// Drift + reconcile for one VM (project resolved from the namespace).
	drift: (namespace: string, name: string) => get<DriftResult>(`${vmPath(namespace, name)}/drift`),
	events: (namespace: string, name: string) =>
		get<gen.Event[]>(`${vmPath(namespace, name)}/events`),
	allEvents: () => get<gen.Event[]>('/api/events'),
	permissions: (namespace: string) =>
		get<gen.Permissions>(`/api/permissions?namespace=${enc(namespace)}`),
	metrics: (namespace: string, name: string, range: string) =>
		get<VMMetrics>(`${vmPath(namespace, name)}/metrics?range=${enc(range)}`),
	vmUsage: (namespace: string, name: string) =>
		get<gen.VMUsage>(`${vmPath(namespace, name)}/usage`),
	clusterSummary: (scope: ScopeQuery = {}) =>
		get<gen.ClusterSummary>(`/api/metrics/cluster${scopeQS(scope)}`),
	hostLoad: () => get<gen.HostLoad>('/api/metrics/hosts'),
	scopeMetrics: (scope: ScopeQuery, range: string) =>
		get<VMMetrics>(`/api/metrics/scope${scopeQS(scope, { range })}`),
	alarms: () => get<gen.Alert[]>('/api/alarms'),
	// Node maintenance (cluster-scoped; the user's token is the gate).
	nodes: () => get<gen.Node[]>('/api/nodes'),
	capacity: () => get<gen.HostCapacity>('/api/metrics/capacity'),
	nodeInfo: (node: string) => get<gen.NodeInfo>(`/api/nodes/${enc(node)}`),
	setNodeCordon: (node: string, unschedulable: boolean) =>
		post<void>(`/api/nodes/${enc(node)}/cordon`, { unschedulable }),
	setNodeMaintenance: (node: string, enter: boolean) =>
		post<void>(`/api/nodes/${enc(node)}/maintenance`, { enter }),
	evacuateNode: (node: string) => post<gen.Evacuation>(`/api/nodes/${enc(node)}/evacuate`, {}),

	// Image upload: create the target DataVolume + mint a token; the browser
	// then streams the file straight to the proxy (uploadUrl from uploadToken).
	createUpload: (req: { namespace: string; name: string; size: string; storageClass?: string }) =>
		post<gen.UploadTarget>('/api/uploads', req),
	uploadStatus: (namespace: string, name: string) =>
		get<gen.UploadStatus>(`/api/uploads/${enc(namespace)}/${enc(name)}`),
	uploadToken: (namespace: string, name: string) =>
		post<gen.UploadToken>(`/api/uploads/${enc(namespace)}/${enc(name)}/token`, {}),
	quotas: (scope: ScopeQuery) => get<gen.NamespaceQuota[]>(`/api/quotas${scopeQS(scope)}`),
	adopt: (namespace: string, name: string) =>
		post<DraftView>(`${vmPath(namespace, name)}/adopt`, {}),
	// Bulk: stage every untracked (NotTracked) VM in a namespace into one draft.
	adoptNamespace: (namespace: string) =>
		post<DraftView>(`/api/namespaces/${enc(namespace)}/adopt`, {}),
	// Wire a repo to an existing labeled-but-repoless project (the "no repo" dead-end).
	adoptProject: (project: string, owners?: string[]) =>
		post<DraftView>(`/api/projects/${enc(project)}/adopt`, owners?.length ? { owners } : {}),
	// Dissolve a repoless project: declared tenancy stages a platform rewrite,
	// label residue is stripped imperatively under the caller's token.
	releaseProject: (project: string) =>
		post<gen.ReleaseResult>(`/api/projects/${enc(project)}/release`, {}),

	// The template library. Deploy renders server-side
	// and stages the VM into the draft; save derives a template from a VM's git
	// manifest ("Clone to Template") - both land as PR-gated changes.
	templates: () => get<{ templates: gen.Template[] }>('/api/templates'),
	deployTemplate: (req: gen.DeployTemplateRequest) => post<DraftView>('/api/templates/deploy', req),
	saveTemplate: (req: gen.SaveTemplateRequest) => post<DraftView>('/api/templates', req),
	updateTemplate: (req: gen.UpdateTemplateRequest) => put<DraftView>('/api/templates', req),
	resync: (namespace: string, name: string) =>
		post<{ application: string; revision: string }>(`${vmPath(namespace, name)}/resync`, {}),

	// Clone (imperative create; the target VM lands NotTracked until adopted).
	clones: (namespace: string, name: string) =>
		get<gen.Clone[]>(`${vmPath(namespace, name)}/clones`),
	createClone: (namespace: string, name: string, target: string) =>
		post<{ name: string; target: string }>(`${vmPath(namespace, name)}/clone`, {
			target,
		}),

	// Snapshots (imperative, RBAC-gated; not git-managed).
	snapshots: (namespace: string, name: string) =>
		get<gen.Snapshot[]>(`${vmPath(namespace, name)}/snapshots`),
	takeSnapshot: (namespace: string, name: string, snapName?: string) =>
		post<{ name: string }>(`${vmPath(namespace, name)}/snapshots`, {
			name: snapName ?? '',
		}),
	restoreSnapshot: (namespace: string, name: string, snap: string) =>
		post<void>(`${vmPath(namespace, name)}/snapshots/${enc(snap)}/restore`, {}),
	deleteSnapshot: (namespace: string, name: string, snap: string) =>
		del(`${vmPath(namespace, name)}/snapshots/${enc(snap)}`),

	// Imperative runtime ops (RBAC-gated; don't touch the git-managed spec).
	restart: (namespace: string, name: string) =>
		post<void>(`${vmPath(namespace, name)}/restart`, {}),
	// node pins the migration to that host; omitted = the scheduler's choice.
	migrate: (namespace: string, name: string, node?: string) =>
		post<void>(`${vmPath(namespace, name)}/migrate`, node ? { node } : {}),
	pause: (namespace: string, name: string) => post<void>(`${vmPath(namespace, name)}/pause`, {}),
	unpause: (namespace: string, name: string) =>
		post<void>(`${vmPath(namespace, name)}/unpause`, {}),
};

// draftsByProject fetches the draft for each named project and returns the
// non-empty ones, for the Changes panel + header badge. Projects with no repo are
// skipped (they can't hold a draft).
export async function draftsByProject(
	projects: string[],
): Promise<{ project: string; draft: DraftView }[]> {
	const results = await Promise.all(
		projects.map(async (project) => {
			try {
				return { project, draft: await api.getDraft(project) };
			} catch (e) {
				if (e instanceof Unauthorized) throw e;
				return null;
			}
		}),
	);
	// Warning-only drafts render too: prune risk must warn BEFORE anything merges.
	return results.filter(
		(r): r is { project: string; draft: DraftView } =>
			!!r && (r.draft.count > 0 || !!r.draft.warning),
	);
}

// Reconnect backoff for streamInventory: 0.5s doubling to a 16s cap.
// retry is the 1-based attempt count since the last successful open.
export function retryDelay(retry: number): number {
	return 500 * 2 ** (Math.min(Math.max(retry, 1), 6) - 1);
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
	onUnauthorized?: () => void,
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
			reconnectTimer = setTimeout(connect, retryDelay(retry));
		};
		ws.onclose = () => {
			if (closed) return;
			if (everOpen) {
				scheduleReconnect();
				return;
			}
			// A close before the socket ever opened can't expose the handshake status
			// (the WS API hides it). It's EITHER an expired session (401 on upgrade) OR
			// a transient failure (backend restart, blip). Don't assume 401 - probe the
			// session: only sign out if it's genuinely gone, otherwise reconnect. This
			// stops every deploy/blip from bouncing valid users to login.
			api
				.me()
				.then(() => {
					if (closed) return; // torn down while probing -> do nothing
					scheduleReconnect(); // session still valid -> it was transient
				})
				.catch((e) => {
					if (closed) return; // torn down while probing -> don't sign out a dead subscription
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
