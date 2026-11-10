import type { DiskProtection, Route } from '#lib/api.js';
import { routeKey, routeState } from '#lib/gateways.js';
import { storageHref } from '#lib/nav.js';
import { diskIfDown, diskNow, protectionOf, standingOf, type MapRow } from '#lib/replication.js';

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

// An operator's: every replicated disk, tinted on the ladder the owners'
// standings use. The disk to look at first is one in trouble (needs an
// operator), else one a lost host takes offline with no copy catching up
// (exposed); a copy catching up mends itself, so it counts short, untinted.
// The body speaks of the disk the link opens, else of the first note.
export function replicationTile(rows: MapRow[]): LandingTile {
	const p = protectionOf(rows);
	if (!p.total) return noDisks;
	const disks = rows.flatMap((r) => r.disks.map((d) => ({ r, d })));
	const catching = (i: number) =>
		disks[i].d.volume.replicas.some((c) => c.role === 'diskful' && c.state === 'catching-up');
	const trouble = p.segments.indexOf('trouble');
	const exposed = p.segments.findIndex((s, i) => s === 'thin' && !catching(i));
	const at = trouble >= 0 ? trouble : exposed;
	if (at < 0)
		return {
			value: `${p.survive} of ${p.total}`,
			label: 'disks survive losing a host',
			tone: 'none',
			body: p.notes[0],
			link: replicationTab,
		};
	const { r, d } = disks[at];
	// What is wrong now, else which host's loss takes it offline: two copies
	// in sync with no witness read fine until then.
	const now = diskNow(d.volume, r.runsOn);
	const lost = d.volume.replicas
		.map((c) => diskIfDown(d.volume, c.node))
		.find((x) => x.fate === 'off');
	const why = now.tone === 'danger' || now.tone === 'warn' || !lost ? now.text : lost.text;
	return {
		value: `${p.survive} of ${p.total}`,
		label: 'disks survive losing a host',
		tone: trouble >= 0 ? 'danger' : 'warn',
		body: `${r.name} ${d.label}: ${why}.`,
		link: { label: `Show ${r.name}`, href: storageHref('replication', { focus: r.key }) },
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
	// Nothing ranks, yet the count is short: say which disk and why.
	const short = list.find((p) => p.standing !== 'protected');
	return {
		value: `${safe} of ${list.length}`,
		label: 'disks survive losing a host',
		tone: rank === 3 ? 'danger' : rank === 2 ? 'warn' : 'none',
		body: first
			? `${nameOf(first)}: ${standingOf(first).detail}`
			: short
				? `${nameOf(short)}: ${standingOf(short).label}.`
				: undefined,
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
	const refusals = rows.filter((x) => x.s.tone === 'danger');
	const refused = refusals[0];
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
		body: `${name}: ${refused.s.label}. ${refused.s.detail}${refusals.length > 1 ? ` ${refusals.length - 1} more refused.` : ''}`,
		link: { label: `Show ${name}`, href: `/networking?${q}` },
	};
}
