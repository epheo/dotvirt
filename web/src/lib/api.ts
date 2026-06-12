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
	size?: string;
}
export interface NIC {
	name: string;
	network?: string;
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
	sync: SyncStatus;
	health?: string;
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

// Unauthorized is thrown when a call returns 401, so the UI can drop to the login
// screen from anywhere.
export class Unauthorized extends Error {
	constructor() {
		super('unauthorized');
		this.name = 'Unauthorized';
	}
}

async function req<T>(path: string, init?: RequestInit): Promise<T> {
	const res = await fetch(path, { credentials: 'same-origin', ...init });
	if (res.status === 401) throw new Unauthorized();
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
export interface Options {
	instancetypes: Instancetype[];
	preferences: Preference[];
	osImages: OSImage[];
	networks: NetworkOption[];
}

export interface CreateVMRequest {
	name: string;
	namespace: string;
	instancetype: string;
	preference: string;
	osImage: { name: string; namespace: string };
	diskSize?: string;
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
	namespace: string;
	name: string;
	changes: Change[];
	yaml?: string;
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

const enc = encodeURIComponent;

export const api = {
	// Auth
	login: (token: string) => post<User>('/api/login', { token }),
	logout: () => post<void>('/api/logout', {}),
	me: () => get<User>('/api/me'),

	inventory: () => get<Inventory>('/api/inventory'),
	options: () => get<Options>('/api/options'),

	// Commit history + per-commit revert (a forward commit opened as a PR).
	history: (project: string) => get<Commit[]>(`/api/projects/${enc(project)}/history`),
	revert: (project: string, hash: string) =>
		post<ProposeResult>(`/api/projects/${enc(project)}/revert`, { hash }),

	// Staging — the backend resolves the project from the VM's namespace, so these
	// per-VM routes need no project param.
	stageEdit: (namespace: string, name: string, req: EditRequest) =>
		post<DraftView>(`/api/vms/${enc(namespace)}/${enc(name)}/edit`, req),
	stageCreate: (req: CreateVMRequest) => post<DraftView>('/api/vms', req),
	stageDelete: (namespace: string, name: string) =>
		post<DraftView>(`/api/vms/${enc(namespace)}/${enc(name)}/delete`, {}),
	unstage: (namespace: string, name: string) =>
		del(`/api/draft/${enc(namespace)}/${enc(name)}`),

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
	adopt: (namespace: string, name: string) =>
		post<DraftView>(`/api/vms/${enc(namespace)}/${enc(name)}/adopt`, {}),
	resync: (namespace: string, name: string) =>
		post<{ application: string; revision: string }>(
			`/api/vms/${enc(namespace)}/${enc(name)}/resync`,
			{}
		),

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
	return results.filter((r): r is { project: string; draft: DraftView } => !!r && r.draft.count > 0);
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
