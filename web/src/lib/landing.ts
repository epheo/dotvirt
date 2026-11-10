import type { DiskProtection, Route } from '#lib/api.js';
import { routeKey, routeState } from '#lib/gateways.js';
import { storageHref } from '#lib/nav.js';
import { protectionOf, standingOf, type MapRow } from '#lib/replication.js';

// A landing page's tile for a section that lives on its own tab: its state in
// one line, tinted only on a problem, and a link that lands on the problem row.
export type LandingTile = {
	value?: string;
	label: string;
	tone: 'none' | 'warn' | 'danger';
	body?: string;
	link: { label: string; href: string };
};

const replicationTab = { label: 'Open Replication', href: storageHref('replication') };

const noDisks: LandingTile = {
	label: 'No replicated disks',
	tone: 'none',
	body: 'Disks on a replicated class show here, with how many survive losing a host.',
	link: replicationTab,
};

// An operator's: every replicated disk. The disk to look at first is one in
// trouble, else one a lost host takes offline.
export function replicationTile(rows: MapRow[]): LandingTile {
	const p = protectionOf(rows);
	if (!p.total) return noDisks;
	const owner = rows.flatMap((r) => r.disks.map(() => r));
	const trouble = p.segments.indexOf('trouble');
	const at = trouble >= 0 ? trouble : p.segments.indexOf('thin');
	return {
		value: `${p.survive} of ${p.total}`,
		label: 'disks survive losing a host',
		tone: trouble >= 0 ? 'danger' : at >= 0 ? 'warn' : 'none',
		body: p.notes[0],
		link:
			at >= 0
				? {
						label: `Show ${owner[at].name}`,
						href: storageHref('replication', { focus: owner[at].key }),
					}
				: replicationTab,
	};
}

const RANK: Record<string, number> = { 'needs-operator': 3, exposed: 2, 'catching-up': 1 };

// A disk owner's: their own disks, as the server reads them, naming no host.
export function protectionTile(
	list: DiskProtection[],
	nameOf: (p: DiskProtection) => string,
): LandingTile {
	if (!list.length) return { ...noDisks, body: 'Pick a replicated class when you add a disk.' };
	const safe = list.filter((p) => p.standing === 'protected').length;
	const first = list.reduce<DiskProtection | undefined>(
		(w, p) => ((RANK[p.standing] ?? 0) > (w ? (RANK[w.standing] ?? 0) : 0) ? p : w),
		undefined,
	);
	const rank = first ? RANK[first.standing] : 0;
	return {
		value: `${safe} of ${list.length}`,
		label: 'disks survive losing a host',
		tone: rank === 3 ? 'danger' : rank === 2 ? 'warn' : 'none',
		body: first ? `${nameOf(first)}: ${standingOf(first).detail}` : undefined,
		link:
			first && rank > 1
				? {
						label: `Show ${nameOf(first)}`,
						href: storageHref('replication', { focus: `${first.namespace}/${first.claim}` }),
					}
				: replicationTab,
	};
}

// Routes, by the state each one's Gateway gave it: a refused route names its
// reason only, never the controller's text. operator: the link opens the
// route on its Gateway's map.
export function routesTile(routes: Route[], operator: boolean): LandingTile {
	const tab = { label: 'Open Gateway', href: '/networking?tab=gateway' };
	if (!routes.length)
		return {
			label: 'No routes yet',
			tone: 'none',
			body: operator
				? 'No project publishes a route.'
				: 'None of your projects publish a route yet.',
			link: tab,
		};
	const rows = routes.map((r) => {
		const p = r.parents?.find((x) => !x.attached) ?? r.parents?.[0];
		return { r, p, s: routeState(p) };
	});
	const live = rows.filter((x) => x.s.live).length;
	const refused = rows.find((x) => x.s.tone === 'danger');
	const waiting = rows.filter((x) => !x.s.live && x.s.tone !== 'danger').length;
	if (!refused)
		return {
			value: `${live} of ${routes.length}`,
			label: 'routes live',
			tone: 'none',
			body: waiting
				? `${waiting} ${waiting === 1 ? 'waits' : 'wait'} for the Gateway to pick ${waiting === 1 ? 'it' : 'them'} up.`
				: undefined,
			link: tab,
		};
	const name = refused.r.hostnames?.[0] ?? refused.r.name;
	const q = new URLSearchParams({ tab: 'gateway' });
	if (operator && refused.p) q.set('gateway', `${refused.p.namespace}/${refused.p.name}`);
	q.set('route', routeKey(refused.r));
	return {
		value: `${live} of ${routes.length}`,
		label: 'routes live',
		tone: 'danger',
		body: `${name}: ${refused.s.label}. ${refused.s.detail}`,
		link: { label: `Show ${name}`, href: `/networking?${q}` },
	};
}
