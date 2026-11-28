// The Desktops section's read model over GET /api/desktops: desktops joined to
// the claims holding them, what the caller may list, the tree's grouping, and
// how a pool or desktop stands.
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

// What the caller may list. In each namespace it covers, the answer names
// every kind the caller may not list there, and the classes at "".
export interface Rights {
	lists(namespace: string, resource: string): boolean;
	// Whether some namespace among namespaces (all those covered by default)
	// lists resource.
	anywhere(resource: string, namespaces?: Iterable<string>): boolean;
	// Whether every namespace covered lists resource: only then does none
	// shown mean none in the caller's projects.
	everywhere(resource: string): boolean;
	classes: boolean;
}

export function rights(d: Desktops, covered: ReadonlySet<string>): Rights {
	const not = new Set(d.unlisted.map((u) => key(u.namespace, u.resource)));
	const lists = (ns: string, r: string) => covered.has(ns) && !not.has(key(ns, r));
	return {
		lists,
		anywhere: (r, nss = covered) => [...nss].some((ns) => lists(ns, r)),
		everywhere: (r) => covered.size > 0 && [...covered].every((ns) => lists(ns, r)),
		classes: !not.has(key('', 'vdiclasses')),
	};
}

// DeskuBe leaves a count of 0 out of a pool's status.
export const count = (p: DesktopPool, k: keyof DesktopPoolCounts): number => p.counts?.[k] ?? 0;

// An unset assignment is DeskuBe's default.
export const assignment = (p: DesktopPool): string => p.assignment || 'Personal';

// The pool's seats in parts that never overlap: Held counts the stopped and
// those in session, Ready the warm. They add up to held + warm + free.
export function seatParts(p: DesktopPool) {
	const held = count(p, 'held');
	const inSession = count(p, 'inSession');
	const stopped = count(p, 'stopped');
	return {
		inSession,
		idle: Math.max(0, held - inSession - stopped),
		stopped,
		warm: count(p, 'warm'),
		free: count(p, 'free'),
	};
}

// A Go duration for people: 30m0s reads 30 min, 720h0m0s 30 d. One it cannot
// read stays as written.
export function duration(d: string): string {
	const m = /^(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?$/.exec(d);
	if (!m || !d) return d;
	const [h, min, s] = m.slice(1).map((v) => Number(v ?? 0));
	if (h >= 24 && h % 24 === 0 && !min && !s) return `${h / 24} d`;
	const parts = [h && `${h} h`, min && `${min} min`, s && `${s} s`].filter(Boolean);
	return parts.length ? parts.join(' ') : '0 s';
}

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

// Why a condition is False, in words.
export const why = (c: DesktopCondition, cls?: string): string =>
	c.reason === 'ClassNotFound' && cls ? `class ${cls} does not exist` : c.message || c.reason || '';

// poolState reads the pool's Ready condition: its class is the only thing
// DeskuBe waits on today. Closed is a choice, said apart.
export function poolState(p: DesktopPool): State {
	const ready = readyOf(p.conditions);
	if (ready?.status === 'False' && ready.reason === 'ClassNotFound')
		return { tone: 'danger', label: 'Class missing', missingClass: p.class };
	if (ready?.status === 'False') return { tone: 'warn', label: 'Not ready', message: why(ready) };
	if (ready?.status !== 'True') return { tone: 'neutral', label: 'Not read yet' };
	return { tone: 'ok', label: 'Ready' };
}

// desktopState is the desktop's phase, unless its class is missing: then a
// stopped one does not start, and a running one keeps its pod until stopped.
// A restart asked for shows until DeskuBe rolled it out.
export function desktopState(d: Desktop): State {
	const ready = readyOf(d.conditions);
	if (ready?.status === 'False' && ready.reason === 'ClassNotFound')
		return { tone: 'danger', label: 'Class missing', missingClass: d.class };
	if (d.restarting) return { tone: 'info', label: 'Restart requested' };
	const failing =
		d.phase === 'Failed' ? d.conditions?.find((c) => c.status === 'False') : undefined;
	return {
		tone: desktopTone(d.phase),
		label: d.phase || 'Not read yet',
		message: failing && why(failing),
	};
}

function desktopTone(phase?: string): Tone {
	if (phase === 'Ready') return 'ok';
	if (phase === 'Failed') return 'danger';
	if (!phase || phase === 'Stopped') return 'neutral';
	return 'info'; // Provisioning, Starting, Stopping
}

// restartHeld is why the desktop is not restarted, '' when it may be: only a
// phase that shows its pod has a session a restart ends, and a restart never
// starts a stopped desktop.
export function restartHeld(d: Pick<Desktop, 'phase' | 'restarting'>): string {
	if (d.restarting) return 'A restart is on its way: DeskuBe has not rolled it out yet.';
	if (d.phase === 'Ready' || d.phase === 'Starting') return '';
	const now = d.phase
		? `this one is ${d.phase.toLowerCase()}`
		: 'DeskuBe has not reported this one yet';
	return `Only a starting or ready desktop restarts: ${now}.`;
}

export const sessionLabel = (s?: string): string =>
	s === 'busy' ? 'In session' : s === 'free' ? 'No session' : '-';

// Each namespaced kind, and who may let the caller read it: desktops carry a
// pairing PIN, so the platform grants them, not the project.
const KINDS: Record<string, { name: string; who: string }> = {
	vdipools: { name: 'Pools', who: 'a project admin' },
	vdiinstances: { name: 'Desktops', who: 'a platform admin' },
	vdiclaims: { name: 'Claims', who: 'a project admin' },
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
		if (!u.namespace || (namespaces && !namespaces.has(u.namespace))) continue;
		by.set(u.resource, [...(by.get(u.resource) ?? []), u.namespace]);
	}
	const where = (nss: string[]) =>
		here ? 'here' : nss.length > 3 ? `in ${nss.length} namespaces` : `in ${nss.join(', ')}`;
	return Object.keys(KINDS)
		.filter((r) => by.has(r))
		.map(
			(r) =>
				`${KINDS[r].name} ${where(by.get(r)!)} are not shown to you: ${KINDS[r].who} can let you read them.`,
		);
}

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

// byProject groups what the caller may see under the projects, in their order,
// keeping only the namespaces that hold a pool or a desktop: a kind the caller
// may not list says nothing of what a namespace holds.
export function byProject(
	d: Desktops,
	projects: readonly Project[],
	may: Rights,
): DesktopsProject[] {
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
			namespaces.push({
				namespace,
				pools,
				desktops: loose.filter((x) => !x.pool),
				unlisted: unlistedIn(namespace).map((u) => u.resource),
				count: may.lists(namespace, 'vdiinstances')
					? pools.reduce((n, x) => n + x.desktops.length, 0) + loose.length
					: null,
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
