// The Desktops section's read model over GET /api/desktops: desktops joined to
// the claims holding them, the tree's grouping, and how a pool or desktop
// stands.
import type {
	Desktop,
	DesktopClaim,
	DesktopCondition,
	DesktopKind,
	DesktopPool,
	DesktopPoolCounts,
	Desktops,
	Project,
} from '#lib/api.js';
import type { Tone } from '#lib/status.js';

const key = (namespace: string, name: string) => `${namespace}/${name}`;

// A desktop with the claim holding it, when the caller may list claims.
export interface DesktopRow {
	desktop: Desktop;
	claim?: DesktopClaim;
}

// joinClaims pairs each desktop with the claim the server named on it: only a
// Bound claim names its desktop, so a claim still waiting holds none.
export function joinClaims(desktops: readonly Desktop[], claims: readonly DesktopClaim[]) {
	const byName = new Map(claims.map((c) => [key(c.namespace, c.name), c]));
	return desktops.map((d): DesktopRow => ({
		desktop: d,
		claim: d.claim ? byName.get(key(d.namespace, d.claim)) : undefined,
	}));
}

// Who has the desktop: its claim's person, else its declared owner.
export const person = (r: DesktopRow): string => r.claim?.user || r.desktop.owner || '';

// Every desktop the caller may see: the pools' and the others.
export const allDesktops = (d: Desktops): Desktop[] =>
	[...d.pools.flatMap((p) => p.desktops), ...d.desktops].sort(
		(a, b) => a.namespace.localeCompare(b.namespace) || a.name.localeCompare(b.name),
	);

// DeskuBe leaves a count of 0 out of a pool's status.
export const count = (p: DesktopPool, k: keyof DesktopPoolCounts): number => p.counts?.[k] ?? 0;

// An unset assignment is DeskuBe's default.
export const assignment = (p: DesktopPool): string => p.assignment || 'Personal';

export interface State {
	tone: Tone;
	label: string;
	// The class it names, which does not exist.
	missingClass?: string;
	// What DeskuBe said of one not ready for another reason.
	message?: string;
}

export const readyOf = (conditions?: DesktopCondition[]) =>
	conditions?.find((c) => c.type === 'Ready');
const said = (c: DesktopCondition) => [c.reason, c.message].filter(Boolean).join(': ');

// poolState reads the pool's Ready condition: its class is the only thing
// DeskuBe waits on today. A closed pool is a choice, not a fault.
export function poolState(p: DesktopPool): State {
	const ready = readyOf(p.conditions);
	if (ready?.status === 'False' && ready.reason === 'ClassNotFound')
		return { tone: 'danger', label: 'Class missing', missingClass: p.class };
	if (ready?.status === 'False') return { tone: 'warn', label: 'Not ready', message: said(ready) };
	if (ready?.status !== 'True') return { tone: 'neutral', label: 'Not read yet' };
	if (p.closed) return { tone: 'neutral', label: 'Closed' };
	return { tone: 'ok', label: 'Ready' };
}

// desktopState is the desktop's phase, unless its class is missing: then a
// stopped one does not start, and a running one keeps its pod until stopped.
export function desktopState(d: Desktop): State {
	const ready = readyOf(d.conditions);
	if (ready?.status === 'False' && ready.reason === 'ClassNotFound')
		return { tone: 'danger', label: 'Class missing', missingClass: d.class };
	const failing =
		d.phase === 'Failed' ? d.conditions?.find((c) => c.status === 'False') : undefined;
	return {
		tone: desktopTone(d.phase),
		label: d.phase || 'Not read yet',
		message: failing && said(failing),
	};
}

function desktopTone(phase?: string): Tone {
	if (phase === 'Ready') return 'ok';
	if (phase === 'Failed') return 'danger';
	if (!phase || phase === 'Stopped') return 'neutral';
	return 'info'; // Provisioning, Starting, Stopping
}

export const sessionLabel = (s?: string): string =>
	s === 'busy' ? 'In session' : s === 'free' ? 'No session' : '-';

const KIND_NAME: Record<string, string> = {
	vdipools: 'Pools',
	vdiinstances: 'Desktops',
	vdiclaims: 'Claims',
};

// unlistedLines says which kinds the caller may not list, one line per kind:
// "here" on a page about one namespace, the namespaces elsewhere. A line is
// about the caller's rights: it never says such an object exists.
export function unlistedLines(
	unlisted: readonly DesktopKind[],
	namespaces: ReadonlySet<string> | null,
	here = false,
): string[] {
	const by = new Map<string, string[]>();
	for (const u of unlisted) {
		if (namespaces && !namespaces.has(u.namespace)) continue;
		by.set(u.resource, [...(by.get(u.resource) ?? []), u.namespace]);
	}
	const where = (nss: string[]) =>
		here ? 'here' : nss.length > 3 ? `in ${nss.length} namespaces` : `in ${nss.join(', ')}`;
	return Object.keys(KIND_NAME)
		.filter((r) => by.has(r))
		.map((r) => `${KIND_NAME[r]} ${where(by.get(r)!)} are not shown to you.`);
}

// Whether the caller may list resource in namespace.
export const listed = (d: Desktops, namespace: string, resource: string): boolean =>
	!d.unlisted.some((u) => u.namespace === namespace && u.resource === resource);

export interface DesktopsNamespace {
	namespace: string;
	pools: DesktopPool[];
	// Made one by one: a pool-made desktop is a row of its pool, never its own.
	desktops: Desktop[];
	unlisted: string[];
	// null where the caller may not list desktops: a 0 there would read as none.
	count: number | null;
	missingClass: boolean;
}

export interface DesktopsProject {
	project: string;
	namespaces: DesktopsNamespace[];
	count: number | null;
	missingClass: boolean;
}

// byProject groups what the caller may see under the inventory's projects, in
// their order, keeping only the namespaces that hold a pool or a desktop: a
// kind the caller may not list says nothing of what a namespace holds.
export function byProject(d: Desktops, projects: readonly Project[]): DesktopsProject[] {
	// Indexed once: the inventory's projects arrive fresh with every frame.
	const pick = <T extends { namespace: string }>(rows: readonly T[]) => {
		const m = new Map<string, T[]>();
		for (const r of rows) {
			const list = m.get(r.namespace);
			if (list) list.push(r);
			else m.set(r.namespace, [r]);
		}
		return (ns: string) => m.get(ns) ?? [];
	};
	const poolsIn = pick(d.pools);
	const desktopsIn = pick(d.desktops);
	const unlistedIn = pick(d.unlisted);
	const out: DesktopsProject[] = [];
	for (const p of projects) {
		const namespaces: DesktopsNamespace[] = [];
		for (const { namespace } of p.namespaces) {
			const pools = poolsIn(namespace);
			// Besides those made one by one, the desktops of pools not shown.
			const loose = desktopsIn(namespace);
			if (!pools.length && !loose.length) continue;
			const unlisted = unlistedIn(namespace).map((u) => u.resource);
			namespaces.push({
				namespace,
				pools,
				desktops: loose.filter((x) => !x.pool),
				unlisted,
				count: unlisted.includes('vdiinstances')
					? null
					: pools.reduce((n, x) => n + x.desktops.length, 0) + loose.length,
				missingClass: pools.some((x) => poolState(x).missingClass),
			});
		}
		const counted = namespaces.filter((x) => x.count !== null);
		if (namespaces.length)
			out.push({
				project: p.name,
				namespaces,
				count: counted.length ? counted.reduce((n, x) => n + x.count!, 0) : null,
				missingClass: namespaces.some((x) => x.missingClass),
			});
	}
	return out;
}

// The project holding each namespace.
export const projectOf = (projects: readonly Project[]): Map<string, string> =>
	new Map(projects.flatMap((p) => p.namespaces.map((n) => [n.namespace, p.name] as const)));
