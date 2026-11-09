import { describe, expect, it } from 'vitest';
import type { GatewayInfo, Route, RouteParent } from '#lib/api.js';
import {
	layoutMap,
	listenersOf,
	parentOn,
	routeKey,
	routesOn,
	routeState,
	shares,
	urlFor,
} from './frontdoor';

const gw = (extra: Partial<GatewayInfo> = {}) =>
	({
		namespace: 'edge',
		name: 'public',
		class: 'portail',
		ready: true,
		listeners: [
			{
				name: 'https',
				port: 443,
				protocol: 'HTTPS',
				hostname: '*.apps.example.com',
				attachedRoutes: 2,
				ready: true,
			},
			{ name: 'http', port: 80, protocol: 'HTTP', attachedRoutes: 0, ready: true },
			{ name: 'ssh', port: 2222, protocol: 'TCP', attachedRoutes: 1, ready: true },
		],
		...extra,
	}) as GatewayInfo;

const parent = (extra: Partial<RouteParent> = {}): RouteParent => ({
	namespace: 'edge',
	name: 'public',
	attached: true,
	...extra,
});

const route = (name: string, extra: Partial<Route> = {}): Route => ({
	kind: 'HTTPRoute',
	namespace: 'web',
	name,
	hostnames: [`${name}.apps.example.com`],
	parents: [parent()],
	backends: [{ namespace: 'web', name, port: 8080, weight: 1 }],
	...extra,
});

describe('where a route arrives', () => {
	it('is the section it names', () => {
		const r = route('a', { parents: [parent({ section: 'http' })] });
		expect(listenersOf(r, gw()).map((l) => l.name)).toEqual(['http']);
	});

	it('is every listener its protocol and hostname fit', () => {
		expect(listenersOf(route('a'), gw()).map((l) => l.name)).toEqual(['https', 'http']);
	});

	it('leaves out a listener whose hostname it does not fit', () => {
		const r = route('a', { hostnames: ['a.other.org'] });
		expect(listenersOf(r, gw()).map((l) => l.name)).toEqual(['http']);
	});

	it('meets a listener when the wildcard is on the route', () => {
		const g = gw({
			listeners: [
				{
					name: 'shop',
					port: 443,
					protocol: 'HTTPS',
					hostname: 'shop.example.com',
					attachedRoutes: 0,
					ready: true,
				},
			],
		});
		expect(listenersOf(route('a', { hostnames: ['*.example.com'] }), g)).toHaveLength(1);
		expect(listenersOf(route('a', { hostnames: ['*.other.org'] }), g)).toHaveLength(0);
	});

	it('does not read a bare domain as under its own wildcard', () => {
		expect(
			listenersOf(route('a', { hostnames: ['apps.example.com'] }), gw()).map((l) => l.name),
		).toEqual(['http']);
	});

	it('reads a refused section over an accepted one', () => {
		const r = route('a', {
			parents: [
				parent({ section: 'https' }),
				parent({ section: 'http', attached: false, reason: 'NotAllowedByListeners' }),
			],
		});
		expect(parentOn(r, gw())).toMatchObject({ section: 'http', attached: false });
		expect(layoutMap(gw(), [r]).routes[0].state.live).toBe(false);
	});

	it('goes by kind for a TCP route', () => {
		const r = route('ssh', { kind: 'TCPRoute', hostnames: undefined });
		expect(listenersOf(r, gw()).map((l) => l.name)).toEqual(['ssh']);
	});

	it('finds only the routes asking for this gateway', () => {
		const other = route('b', { parents: [parent({ name: 'internal' })] });
		expect(routesOn([route('a'), other], gw()).map((r) => r.name)).toEqual(['a']);
		expect(parentOn(other, gw())).toBeUndefined();
	});

	it('does not confuse a gateway of the same name in another namespace', () => {
		const other = route('b', { parents: [parent({ namespace: 'other' })] });
		expect(routesOn([other], gw())).toEqual([]);
	});
});

describe('a route state', () => {
	it('is live when attached', () => {
		expect(routeState(parent())).toMatchObject({ tone: 'ok', label: 'Live', live: true });
	});

	it('says taken and names nobody', () => {
		const s = routeState(parent({ attached: false, reason: 'HostnameConflict' }));
		expect(s).toMatchObject({ tone: 'danger', label: 'Taken', live: false });
		expect(s.detail).toBe('Another route holds this hostname. Pick another.');
	});

	it('is a calm wait before any controller answers', () => {
		expect(routeState(parent({ attached: false, reason: 'Pending' }))).toMatchObject({
			tone: 'neutral',
			label: 'Waiting',
		});
		expect(routeState(undefined).live).toBe(false);
	});

	it('still has a next step for a reason it does not know', () => {
		expect(routeState(parent({ attached: false, reason: 'Odd' }))).toMatchObject({
			label: 'Odd',
			detail: 'The front door did not accept it. Ask your platform team.',
		});
	});
});

describe('the map', () => {
	it('draws a link in and a link out for each route', () => {
		const m = layoutMap(gw(), [route('a', { parents: [parent({ section: 'https' })] })]);
		expect(m.routes).toHaveLength(1);
		expect(m.backends).toHaveLength(1);
		expect(m.links).toHaveLength(2);
		expect(m.links.every((l) => l.route === 'HTTPRoute/web/a')).toBe(true);
	});

	it('draws a shared service once', () => {
		const shared = [{ namespace: 'web', name: 's', port: 80, weight: 1 }];
		const m = layoutMap(gw(), [route('a', { backends: shared }), route('b', { backends: shared })]);
		expect(m.backends).toHaveLength(1);
	});

	it('keeps same-named services of two namespaces apart', () => {
		const m = layoutMap(gw(), [
			route('a', { backends: [{ namespace: 'x', name: 's', port: 80, weight: 1 }] }),
			route('b', { backends: [{ namespace: 'y', name: 's', port: 80, weight: 1 }] }),
		]);
		expect(m.backends).toHaveLength(2);
	});

	it('marks a route that is not live', () => {
		const m = layoutMap(gw(), [
			route('a', { parents: [parent({ attached: false, reason: 'HostnameConflict' })] }),
		]);
		expect(m.links.every((l) => !l.live)).toBe(true);
	});

	it('draws nothing but listeners for a gateway with no route', () => {
		const m = layoutMap(gw(), []);
		expect(m).toMatchObject({ routes: [], backends: [], links: [] });
		expect(m.listeners).toHaveLength(3);
	});
});

describe('details', () => {
	it('splits traffic by weight', () => {
		const r = route('a', {
			backends: [
				{ namespace: 'w', name: 'a', weight: 1 },
				{ namespace: 'w', name: 'b', weight: 3 },
			],
		});
		expect(shares(r).map((s) => s.percent)).toEqual([25, 75]);
	});

	it('survives weights that are all zero', () => {
		expect(
			shares(route('a', { backends: [{ namespace: 'w', name: 'a', weight: 0 }] }))[0].percent,
		).toBe(0);
	});

	it('builds the URL from the listener', () => {
		expect(urlFor(route('a'), 'a.example.com', gw().listeners[1])).toBe('http://a.example.com');
		expect(urlFor(route('a'), 'a.example.com', gw().listeners[0])).toBe('https://a.example.com');
	});

	it('keys a route by kind, namespace and name', () => {
		expect(routeKey(route('a'))).toBe('HTTPRoute/web/a');
	});
});
