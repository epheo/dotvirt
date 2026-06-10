// Typed client for the dotvirt backend API. Mirrors internal/model.

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
	guestIP?: string;
	nodeName?: string;
	sync: SyncStatus;
	health?: string;
}

export interface Project {
	namespace: string;
	vms: VM[];
}

export interface Inventory {
	branch: string;
	projects: Project[];
}

async function get<T>(path: string): Promise<T> {
	const res = await fetch(path);
	if (!res.ok) {
		throw new Error(`${path}: ${res.status} ${await res.text()}`);
	}
	return res.json() as Promise<T>;
}

export interface EditRequest {
	sourceBranch: string;
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

export interface EditResult {
	branch: string;
	file: string;
	hash: string;
	diff: string;
	pushed: boolean;
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
	sourceBranch: string;
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

async function post<T>(path: string, body: unknown): Promise<T> {
	const res = await fetch(path, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify(body)
	});
	if (!res.ok) {
		throw new Error(`${path}: ${res.status} ${await res.text()}`);
	}
	return res.json() as Promise<T>;
}

export const api = {
	branches: () => get<string[]>('/api/branches'),
	inventory: (branch?: string) =>
		get<Inventory>(`/api/inventory${branch ? `?branch=${encodeURIComponent(branch)}` : ''}`),
	editVM: (namespace: string, name: string, req: EditRequest) =>
		post<EditResult>(
			`/api/vms/${encodeURIComponent(namespace)}/${encodeURIComponent(name)}/edit`,
			req
		),
	options: () => get<Options>('/api/options'),
	createVM: (req: CreateVMRequest) => post<EditResult>('/api/vms', req)
};

/**
 * streamInventory subscribes to live inventory for a branch over WebSocket.
 * Calls onInventory on each push and onStatus on connect/disconnect, and
 * auto-reconnects with backoff. Returns a function to close the subscription.
 */
export function streamInventory(
	branch: string,
	onInventory: (inv: Inventory) => void,
	onStatus?: (connected: boolean) => void
): () => void {
	let ws: WebSocket | null = null;
	let closed = false;
	let retry = 0;
	let reconnectTimer: ReturnType<typeof setTimeout> | undefined;

	const url = () => {
		const proto = location.protocol === 'https:' ? 'wss' : 'ws';
		return `${proto}://${location.host}/api/inventory/stream?branch=${encodeURIComponent(branch)}`;
	};

	const connect = () => {
		if (closed) return;
		ws = new WebSocket(url());
		ws.onopen = () => {
			retry = 0;
			onStatus?.(true);
		};
		ws.onmessage = (e) => {
			try {
				onInventory(JSON.parse(e.data) as Inventory);
			} catch {
				/* ignore malformed frame */
			}
		};
		ws.onclose = () => {
			onStatus?.(false);
			if (closed) return;
			retry = Math.min(retry + 1, 6);
			reconnectTimer = setTimeout(connect, 500 * 2 ** (retry - 1)); // 0.5s..16s backoff
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
