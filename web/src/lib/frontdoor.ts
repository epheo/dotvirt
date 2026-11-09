// What the Front door tab derives from the Gateways and routes it is handed.
// Pure, so the map can be tested without a browser.
import type { GatewayInfo, GatewayListener, Route, RouteBackend, RouteParent } from '#lib/api.js';
import type { Tone } from '#lib/status.js';

export const routeKey = (r: Route) => `${r.kind}/${r.namespace}/${r.name}`;
export const gatewayKey = (g: GatewayInfo) => `${g.namespace}/${g.name}`;

const SERVED_ON: Record<string, string[]> = {
	HTTPRoute: ['HTTP', 'HTTPS'],
	TLSRoute: ['TLS'],
	TCPRoute: ['TCP'],
	UDPRoute: ['UDP'],
};

// A route can name several sections of one front door: a refused one wins, so
// the map never reads Live over a refusal.
export function parentOn(r: Route, g: GatewayInfo): RouteParent | undefined {
	const on = r.parents?.filter((p) => p.namespace === g.namespace && p.name === g.name) ?? [];
	return on.find((p) => !p.attached) ?? on[0];
}

export const routesOn = (routes: Route[], g: GatewayInfo): Route[] =>
	routes.filter((r) => parentOn(r, g));

const wildcard = (host: string) => (host.startsWith('*.') ? host.slice(1) : '');

// Either side may be the wildcard: a route for *.example.com does attach to a
// listener for shop.example.com.
function hostsMeet(a: string, b: string): boolean {
	const [wa, wb] = [wildcard(a), wildcard(b)];
	return a === b || (wa !== '' && b.endsWith(wa)) || (wb !== '' && a.endsWith(wb));
}

function hostFits(listener: string | undefined, hosts: string[] | undefined): boolean {
	return !listener || !hosts?.length || hosts.some((h) => hostsMeet(listener, h));
}

// The listeners a route arrives on: the one it names, or every one it could
// bind by protocol and hostname. A listener's allowedRoutes can still refuse a
// route that names no section; the read model does not carry it, so such a
// route may be drawn into a listener that does not take it.
export function listenersOf(r: Route, g: GatewayInfo): GatewayListener[] {
	const parents = r.parents?.filter((p) => p.namespace === g.namespace && p.name === g.name) ?? [];
	if (parents.some((p) => !p.section))
		return g.listeners.filter(
			(l) => (SERVED_ON[r.kind] ?? []).includes(l.protocol) && hostFits(l.hostname, r.hostnames),
		);
	return g.listeners.filter((l) => parents.some((p) => p.section === l.name));
}

export type RouteState = { tone: Tone; label: string; detail: string; live: boolean };

const WHY: Record<string, { label: string; detail: string }> = {
	Pending: { label: 'Waiting', detail: 'The front door has not picked this up yet.' },
	NotAllowedByListeners: {
		label: 'Not allowed',
		detail: 'This front door does not take routes from this project. Ask your platform team.',
	},
	NoMatchingListenerHostname: {
		label: 'Hostname not served',
		detail: 'This hostname is outside what the front door serves. Use one it serves.',
	},
	NoMatchingParent: {
		label: 'No such port',
		detail: 'The port or section it names does not exist on this front door.',
	},
	HostnameConflict: { label: 'Taken', detail: 'Another route holds this hostname. Pick another.' },
	BackendNotFound: {
		label: 'No such service',
		detail: 'A service it sends to does not exist. Create it, or fix the name.',
	},
	RefNotPermitted: {
		label: 'Not permitted',
		detail: 'It sends to a service in another project, which has not allowed it.',
	},
	InvalidKind: { label: 'Unsupported', detail: 'It sends to something that is not a service.' },
	UnsupportedValue: { label: 'Unsupported', detail: 'The front door cannot serve it as written.' },
};

export function routeState(p: RouteParent | undefined): RouteState {
	if (p?.attached)
		return { tone: 'ok', label: 'Live', detail: 'Reachable through the front door.', live: true };
	const why = WHY[p?.reason ?? 'Pending'] ?? {
		label: p?.reason ?? 'Not live',
		detail: 'The front door did not accept it. Ask your platform team.',
	};
	return { tone: p?.reason === 'Pending' || !p ? 'neutral' : 'danger', ...why, live: false };
}

export const urlFor = (r: Route, host: string, listener?: GatewayListener): string => {
	const tls = listener?.protocol === 'HTTPS' || (!listener && r.kind === 'HTTPRoute');
	return `${tls ? 'https' : 'http'}://${host}`;
};

export const backendLabel = (b: RouteBackend) => `${b.name}${b.port ? `:${b.port}` : ''}`;

// Each backend's share of its route's traffic.
export function shares(r: Route): { backend: RouteBackend; percent: number }[] {
	const total = (r.backends ?? []).reduce((n, b) => n + b.weight, 0);
	return (r.backends ?? []).map((backend) => ({
		backend,
		percent: total ? Math.round((100 * backend.weight) / total) : 0,
	}));
}

export type MapNode = { key: string; x: number; y: number };
export type ListenerNode = MapNode & { listener: GatewayListener };
export type RouteNode = MapNode & { route: Route; state: RouteState };
export type BackendNode = MapNode & { backend: RouteBackend };
export type MapLink = { d: string; route: string; live: boolean };
export type MapLayout = {
	width: number;
	height: number;
	listeners: ListenerNode[];
	routes: RouteNode[];
	backends: BackendNode[];
	links: MapLink[];
};

export const NODE_W = 180;
export const NODE_H = 40;
const ROW = 52;
const COLUMNS = [0, 270, 540];

const curve = (x1: number, y1: number, x2: number, y2: number) => {
	const mid = (x1 + x2) / 2;
	return `M${x1} ${y1} C${mid} ${y1} ${mid} ${y2} ${x2} ${y2}`;
};

// Three columns, each spread over the tallest one, so a column of two sits
// centred against a column of ten.
export function layoutMap(g: GatewayInfo, routes: Route[]): MapLayout {
	const on = routesOn(routes, g);
	const seen = new Map<string, RouteBackend>();
	for (const r of on)
		for (const b of r.backends ?? []) seen.set(`${b.namespace}/${backendLabel(b)}`, b);
	const backendKeys = [...seen.keys()];
	const rows = Math.max(g.listeners.length, on.length, backendKeys.length, 1);
	const height = rows * ROW;
	const yOf = (i: number, n: number) => ((i + 0.5) * height) / n - NODE_H / 2;

	const listeners = g.listeners.map((listener, i) => ({
		key: listener.name,
		x: COLUMNS[0],
		y: yOf(i, g.listeners.length),
		listener,
	}));
	const routeNodes = on.map((route, i) => ({
		key: routeKey(route),
		x: COLUMNS[1],
		y: yOf(i, on.length),
		route,
		state: routeState(parentOn(route, g)),
	}));
	const backends = backendKeys.map((key, i) => ({
		key,
		x: COLUMNS[2],
		y: yOf(i, backendKeys.length),
		backend: seen.get(key)!,
	}));

	const links: MapLink[] = [];
	for (const n of routeNodes) {
		const midY = n.y + NODE_H / 2;
		for (const l of listenersOf(n.route, g)) {
			const from = listeners.find((x) => x.key === l.name)!;
			links.push({
				d: curve(from.x + NODE_W, from.y + NODE_H / 2, n.x, midY),
				route: n.key,
				live: n.state.live,
			});
		}
		for (const b of n.route.backends ?? []) {
			const to = backends.find((x) => x.key === `${b.namespace}/${backendLabel(b)}`)!;
			links.push({
				d: curve(n.x + NODE_W, midY, to.x, to.y + NODE_H / 2),
				route: n.key,
				live: n.state.live,
			});
		}
	}
	return {
		width: COLUMNS[2] + NODE_W,
		height,
		listeners,
		routes: routeNodes,
		backends,
		links,
	};
}
