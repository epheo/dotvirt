// The Storage section's model: one row per VM disk, joined from the live
// inventory (the manifest's disks), the class sheet, the cluster-wide snapshot
// and claim listings and the host list. Every tile, count, filter and bulk
// plan on the Storage tabs derives from these rows, so they cannot disagree.
import type {
	Claim,
	Disk,
	DraftItem,
	EditRequest,
	Node,
	OSImage,
	Project,
	Snapshot,
	StorageClassInfo,
	Template,
	VM,
	VolumeMigration,
} from '#lib/api.js';
import { vmKey } from '#lib/api.js';
import { bytes, quantityBytes } from '#lib/format.js';
import { bySize } from '#lib/rail.js';

// A disk counts as protected by a snapshot this recent.
export const FRESH_DAYS = 7;
const FRESH_MS = FRESH_DAYS * 86_400_000;
// The share of an image's disks without a fresh snapshot worth flagging.
export const STALE_FLAG = 0.25;
// Free space below this share of (free + what VMs hold) reads as close to full.
const NEAR_FULL = 0.1;

const RWX = 'ReadWriteMany';
const PERSISTENT = new Set(['dataVolume', 'pvc']);

const isPersistent = (d: Disk) => PERSISTENT.has(d.type ?? '');

export interface DiskRow {
	key: string; // "namespace/vm/disk": selection, the side panel, deep links
	vm: VM;
	vmKey: string;
	project: string;
	disk: Disk;
	size: number; // bytes; NaN when unknown
	storageClass: string; // '' when unknown (a pvc disk before its claim loads)
	shared: boolean | null; // ReadWriteMany: the VM can live-migrate with it; null = class facts missing
	host: string; // where the disk lives (local) or its VM runs; '' when unknown
	maintenance: boolean; // host is in maintenance
	image: string; // "namespace/name" of the boot image, '' when none
	snapshots: Snapshot[]; // ready snapshots that captured this disk, newest first
	fresh: boolean; // a ready snapshot within FRESH_DAYS
	running: boolean;
	ephemeral: boolean; // the boot disk of a VM that keeps no data
}

export interface StorageInputs {
	projects: Project[];
	classes: StorageClassInfo[];
	defaultClass: string;
	snapshots: Snapshot[] | null; // null until the listing lands
	claims: Claim[] | null;
	nodes: Node[] | null;
	now?: number;
}

// Whether a disk gets ReadWriteMany: its own access modes when the manifest
// names them, else what CDI applies from the class profile's first set.
export function diskShared(disk: Disk, info: StorageClassInfo | undefined): boolean | null {
	if (disk.accessModes?.length) return disk.accessModes.includes(RWX);
	if (!info?.profile) return null;
	return info.profile.accessModes.includes(RWX);
}

// Whether a class offers shared disks at all (the tree's local/shared tag).
const classShared = (info: StorageClassInfo | undefined): boolean | null =>
	info?.profile ? !!info.profile.shared : null;

// Ready snapshots per VM, newest first.
function snapshotsByVM(snapshots: Snapshot[]): Map<string, Snapshot[]> {
	const m = new Map<string, Snapshot[]>();
	for (const s of snapshots) {
		if (!s.readyToUse || !s.vm || !s.namespace) continue;
		const k = `${s.namespace}/${s.vm}`;
		if (!m.has(k)) m.set(k, []);
		m.get(k)!.push(s);
	}
	for (const list of m.values())
		list.sort((a, b) => (b.created ?? '').localeCompare(a.created ?? ''));
	return m;
}

// A snapshot without a volume list (an older KubeVirt, or one still being
// taken) is read as covering every disk of its VM.
const captured = (s: Snapshot, disk: string) => !s.volumes?.length || s.volumes.includes(disk);

// How long ago a snapshot was taken, in the words the tables use: "2 h ago",
// "1 day ago", "9 days ago".
export function ago(iso: string | undefined, now = Date.now()): string {
	const t = iso ? new Date(iso).getTime() : NaN;
	if (Number.isNaN(t)) return '';
	const mins = Math.max(0, Math.floor((now - t) / 60_000));
	if (mins < 60) return mins < 1 ? 'just now' : `${mins} min ago`;
	const hours = Math.floor(mins / 60);
	if (hours < 24) return `${hours} h ago`;
	const days = Math.floor(hours / 24);
	return days === 1 ? '1 day ago' : `${days} days ago`;
}

function isFresh(s: Snapshot | undefined, now = Date.now()): boolean {
	if (!s?.created) return false;
	const t = new Date(s.created).getTime();
	return !Number.isNaN(t) && now - t <= FRESH_MS;
}

// One row per persistent disk, plus one ephemeral row per VM that keeps no
// data (its boot disk), so the "keeps no data" filter lists those VMs.
export function diskRows(inp: StorageInputs): DiskRow[] {
	const now = inp.now ?? Date.now();
	const classes = new Map(inp.classes.map((c) => [c.name, c]));
	const snaps = snapshotsByVM(inp.snapshots ?? []);
	const claims = new Map((inp.claims ?? []).map((c) => [`${c.namespace}/${c.name}`, c]));
	const maint = new Set((inp.nodes ?? []).filter((n) => n.maintenance).map((n) => n.name));
	const out: DiskRow[] = [];
	for (const p of inp.projects)
		for (const ns of p.namespaces)
			for (const vm of ns.vms) {
				const key = vmKey(vm);
				const running = vm.phase === 'Running';
				const vmSnaps = snaps.get(key) ?? [];
				const persistent = (vm.disks ?? []).filter(isPersistent);
				for (const disk of persistent) {
					const claim = disk.claim ? claims.get(`${vm.namespace}/${disk.claim}`) : undefined;
					const storageClass =
						disk.type === 'dataVolume'
							? disk.storageClass || inp.defaultClass
							: (claim?.storageClass ?? '');
					const shared = diskShared(disk, classes.get(storageClass));
					const host = (running && vm.nodeName) || (shared ? '' : (claim?.node ?? ''));
					const mine = vmSnaps.filter((s) => captured(s, disk.name));
					out.push({
						key: `${key}/${disk.name}`,
						vm,
						vmKey: key,
						project: p.name,
						disk,
						size: quantityBytes(disk.size || claim?.capacity || ''),
						storageClass,
						shared,
						host,
						maintenance: maint.has(host),
						image: disk.image ?? '',
						snapshots: mine,
						fresh: isFresh(mine[0], now),
						running,
						ephemeral: false,
					});
				}
				if (persistent.length === 0) {
					const boot: Disk = (vm.disks ?? []).find((d) => d.type !== 'cloudInitNoCloud') ?? {
						name: '',
					};
					out.push({
						key: `${key}/${boot.name}`,
						vm,
						vmKey: key,
						project: p.name,
						disk: boot,
						size: quantityBytes(boot.size ?? ''),
						storageClass: '',
						shared: null,
						host: running ? (vm.nodeName ?? '') : '',
						maintenance: maint.has(vm.nodeName ?? ''),
						image: '',
						snapshots: [],
						fresh: false,
						running,
						ephemeral: true,
					});
				}
			}
	return out;
}

// --- filters ---

export type FlagFilter = 'nolm' | 'nosnap' | 'nodata' | 'stopped';
export const FLAG_LABEL: Record<FlagFilter, string> = {
	nolm: 'Cannot live-migrate',
	nosnap: `No snapshot in ${FRESH_DAYS} days`,
	nodata: 'Keeps no data',
	stopped: 'VM stopped',
};
export const VALUE_FILTERS = ['project', 'class', 'host', 'image'] as const;
export type ValueFilter = (typeof VALUE_FILTERS)[number];
export const VALUE_LABEL: Record<ValueFilter, string> = {
	project: 'Project',
	class: 'Class',
	host: 'Host',
	image: 'Image',
};

export interface DiskFilters {
	flags: FlagFilter[];
	project?: string;
	class?: string;
	host?: string;
	image?: string;
	q?: string;
}

const FLAGS = Object.keys(FLAG_LABEL) as FlagFilter[];

export function filtersFromQuery(q: URLSearchParams): DiskFilters {
	const flags = (q.get('f') ?? '')
		.split(',')
		.filter((f): f is FlagFilter => FLAGS.includes(f as FlagFilter));
	const f: DiskFilters = { flags: [...new Set(flags)] };
	for (const k of VALUE_FILTERS) {
		const v = q.get(k);
		if (v) f[k] = v;
	}
	const text = q.get('q')?.trim();
	if (text) f.q = text;
	return f;
}

// Writes the filters onto q, leaving every other parameter alone.
export function filtersToQuery(f: DiskFilters, q: URLSearchParams): URLSearchParams {
	const out = new URLSearchParams(q);
	if (f.flags.length) out.set('f', f.flags.join(','));
	else out.delete('f');
	for (const k of [...VALUE_FILTERS, 'q'] as const) {
		const v = f[k];
		if (v) out.set(k, v);
		else out.delete(k);
	}
	return out;
}

export const hasFilters = (f: DiskFilters) =>
	f.flags.length > 0 || VALUE_FILTERS.some((k) => !!f[k]) || !!f.q;

const matchText = (r: DiskRow, q: string) => {
	const t = q.toLowerCase();
	return [r.vm.name, r.vm.namespace, r.disk.name, r.storageClass, r.host, r.image].some((s) =>
		s.toLowerCase().includes(t),
	);
};

function matchFlag(r: DiskRow, f: FlagFilter): boolean {
	switch (f) {
		case 'nolm':
			return r.shared === false;
		case 'nosnap':
			return !r.fresh;
		case 'nodata':
			return r.ephemeral;
		case 'stopped':
			return !r.running;
	}
}

// Rows passing every filter. Ephemeral rows only ever show under "keeps no
// data", and nothing else lists them.
export function applyFilters(rows: DiskRow[], f: DiskFilters): DiskRow[] {
	const nodata = f.flags.includes('nodata');
	return rows.filter(
		(r) =>
			r.ephemeral === nodata &&
			f.flags.every((x) => matchFlag(r, x)) &&
			(!f.project || r.project === f.project) &&
			(!f.class || r.storageClass === f.class) &&
			(!f.host || r.host === f.host) &&
			(!f.image || r.image === f.image) &&
			(!f.q || matchText(r, f.q)),
	);
}

// --- sorting ---

export const num = (n: number) => n.toLocaleString('en-US');

// A byte size without a trailing ".0": "150 GiB", "2.9 TiB".
export const size = (n: number) => bytes(n).replace('.0 ', ' ');

export type DiskSort = 'vm' | 'size' | 'snapshot';

// web-2 before web-10, the way people number VMs.
const collator = new Intl.Collator('en', { numeric: true });
const naturally = (a: string, b: string) => collator.compare(a, b);

export function sortRows(rows: DiskRow[], by: DiskSort, desc = false): DiskRow[] {
	const dir = desc ? -1 : 1;
	const last = (r: DiskRow) => r.snapshots[0]?.created ?? '';
	const cmp = (a: DiskRow, b: DiskRow): number => {
		if (by === 'size') return ((a.size || 0) - (b.size || 0)) * dir;
		if (by === 'snapshot') return last(a).localeCompare(last(b)) * dir;
		return 0;
	};
	return [...rows].sort(
		(a, b) =>
			cmp(a, b) ||
			naturally(a.vm.name, b.vm.name) * (by === 'vm' ? dir : 1) ||
			a.vm.namespace.localeCompare(b.vm.namespace) ||
			a.disk.name.localeCompare(b.disk.name),
	);
}

// --- the Library tab ---

export const shortName = (image: string) => image.slice(image.indexOf('/') + 1);

export interface ImageUse {
	image: string; // "namespace/name"; '' gathers disks made from no image
	disks: number;
	local: number;
	shared: number;
	fresh: number;
	templates: string[];
	catalog?: OSImage;
}

// Disks per boot image, the catalog's unused images included, biggest first.
export function imageUsage(
	rows: DiskRow[],
	images: OSImage[] = [],
	templates: Template[] = [],
): ImageUse[] {
	const m = new Map<string, ImageUse>();
	const use = (image: string) => {
		if (!m.has(image))
			m.set(image, { image, disks: 0, local: 0, shared: 0, fresh: 0, templates: [] });
		return m.get(image)!;
	};
	for (const img of images) use(`${img.namespace}/${img.name}`).catalog = img;
	for (const r of rows) {
		if (r.ephemeral) continue;
		const u = use(r.image);
		u.disks++;
		if (r.shared === true) u.shared++;
		else if (r.shared === false) u.local++;
		if (r.fresh) u.fresh++;
	}
	for (const t of templates)
		if (t.image && m.has(t.image)) {
			const u = m.get(t.image)!;
			if (!u.templates.includes(t.name)) u.templates.push(t.name);
		}
	return [...m.values()]
		.filter((u) => u.image || u.disks)
		.sort((a, b) => b.disks - a.disks || a.image.localeCompare(b.image));
}

export interface ImageSummary {
	top: ImageUse[];
	more: (ImageUse & { count: number }) | null; // the images past the top, summed
	none: ImageUse | null; // disks made from no image
	total: number; // boot images
}

export function topImages(uses: ImageUse[], n = 4): ImageSummary {
	const images = uses.filter((u) => u.image);
	// Only images something was made from lead; unused ones fold into the rest.
	const lead = Math.min(n, images.filter((u) => u.disks > 0).length);
	const rest = images.slice(lead);
	const more = rest.length
		? rest.reduce<ImageUse & { count: number }>(
				(acc, u) => ({
					...acc,
					disks: acc.disks + u.disks,
					local: acc.local + u.local,
					shared: acc.shared + u.shared,
					fresh: acc.fresh + u.fresh,
				}),
				{ image: '', disks: 0, local: 0, shared: 0, fresh: 0, templates: [], count: rest.length },
			)
		: null;
	return {
		top: images.slice(0, lead),
		more,
		none: uses.find((u) => !u.image) ?? null,
		total: images.length,
	};
}

// The without-a-fresh-snapshot share is worth a flag when it is large.
export const staleShare = (u: { disks: number; fresh: number }) =>
	u.disks ? (u.disks - u.fresh) / u.disks : 0;

export interface ClassFree {
	name: string;
	free: number | null; // null: the driver reports none
	held: number; // bytes VM disks request on it
	nearFull: boolean;
}

export interface LibraryTiles {
	noLiveMigrate: number;
	stale: number | null; // null until snapshots land
	noData: number; // VMs
	free: ClassFree[];
}

// snapshotsLoaded: the stale count needs the snapshot listing; without it
// there is no answer, not zero.
export function libraryTiles(
	rows: DiskRow[],
	classes: StorageClassInfo[],
	snapshotsLoaded: boolean,
): LibraryTiles {
	const persistent = rows.filter((r) => !r.ephemeral);
	const held = new Map<string, number>();
	for (const r of persistent)
		if (!Number.isNaN(r.size)) held.set(r.storageClass, (held.get(r.storageClass) ?? 0) + r.size);
	return {
		noLiveMigrate: persistent.filter((r) => r.shared === false).length,
		stale: snapshotsLoaded ? persistent.filter((r) => !r.fresh).length : null,
		noData: rows.filter((r) => r.ephemeral).length,
		free: classes.map((c) => {
			const h = held.get(c.name) ?? 0;
			const free = c.free ?? null;
			return {
				name: c.name,
				free,
				held: h,
				nearFull: free !== null && free < NEAR_FULL * (free + h),
			};
		}),
	};
}

// "3,412 disks on 3 classes, 48 hosts"
export function storageContext(rows: DiskRow[], classes: number, hosts: number): string {
	const disks = rows.filter((r) => !r.ephemeral).length;
	const parts = [
		`${num(disks)} ${disks === 1 ? 'disk' : 'disks'} on ${classes} ${classes === 1 ? 'class' : 'classes'}`,
	];
	if (hosts) parts.push(`${num(hosts)} ${hosts === 1 ? 'host' : 'hosts'}`);
	return parts.join(', ');
}

// --- the rail tree ---

export interface ProjectCount {
	name: string;
	vms: number;
}

export function projectsBySize(projects: Project[]): ProjectCount[] {
	return bySize(
		projects.map((p) => ({
			name: p.name,
			vms: p.namespaces.reduce((n, ns) => n + ns.vms.length, 0),
		})),
		(p) => p.vms,
		(p) => p.name,
	);
}

// "local, default" / "shared" / "" while the class facts are missing.
export function classTag(info: StorageClassInfo): string {
	const shared = classShared(info);
	return [shared === null ? '' : shared ? 'shared' : 'local', info.default ? 'default' : '']
		.filter(Boolean)
		.join(', ');
}

// --- what a class allows (the side panel) ---

export interface ClassFacts {
	liveMigration: boolean | null;
	shared: boolean | null;
	snapshots: boolean | null;
	free: string;
}

export function classFacts(info: StorageClassInfo | undefined): ClassFacts {
	const shared = info?.profile ? info.profile.accessModes.includes(RWX) : null;
	return {
		liveMigration: shared,
		shared,
		snapshots: info ? !!info.snapshotClass : null,
		free: info?.free != null ? `${size(info.free)} free` : 'Not reported',
	};
}

// The class a local disk moves to by default: a shared one, the default class
// first, then by name.
export function suggestTarget(classes: StorageClassInfo[]): string {
	const shared = classes.filter((c) => c.profile?.accessModes.includes(RWX));
	shared.sort((a, b) => Number(!!b.default) - Number(!!a.default) || a.name.localeCompare(b.name));
	return shared[0]?.name ?? '';
}

// --- bulk move ---

type Edit = Omit<EditRequest, 'sourceFile'>;

export interface MoveEdit {
	vm: VM;
	project: string;
	edit: Edit;
	disks: DiskRow[]; // the disks this edit moves
}

export interface MovePlan {
	target: string;
	edits: MoveEdit[];
	already: DiskRow[]; // staged to the target before
	stopped: DiskRow[]; // left out: a disk copies only while its VM runs
	blocked: { row: DiskRow; reason: string }[];
	moving: number; // disks the edits move
	vms: number;
	liveMigrateAfter: number; // VMs whose every persistent disk is shared once it merges
	// Disks leaving a class other than the default: a class picked on purpose
	// (faster, closer), which the move gives up.
	leaving: { storageClass: string; count: number; description: string }[];
	added: number; // bytes the moves add to the target
	targetFree: number | null;
	projects: string[];
}

// Why a disk cannot be moved, or '' when it can.
function moveBlock(r: DiskRow, target: string, staged: DraftItem | undefined): string {
	if (r.ephemeral) return 'keeps no data';
	if (!r.vm.sourceFile) return 'not in git';
	if (r.disk.type !== 'dataVolume') return 'not provisioned by its manifest';
	if (r.storageClass === target) return `already on ${target}`;
	if (staged && (staged.kind !== 'edit' || !staged.edit))
		return `a staged ${staged.kind} of ${r.vm.name} is waiting`;
	return '';
}

// One staged edit per VM: what is already staged for it, plus every selected
// disk's move. Staging replaces a VM's whole entry, so the existing edit is
// carried over rather than dropped.
export function planBulkMove(
	selected: DiskRow[],
	target: string,
	ctx: {
		classes: StorageClassInfo[];
		staged: Map<string, DraftItem>;
		rows: DiskRow[]; // every row, to judge a VM's other disks
	},
): MovePlan {
	const classes = new Map(ctx.classes.map((c) => [c.name, c]));
	const targetInfo = classes.get(target);
	const plan: MovePlan = {
		target,
		edits: [],
		already: [],
		stopped: [],
		blocked: [],
		moving: 0,
		vms: 0,
		liveMigrateAfter: 0,
		leaving: [],
		added: 0,
		targetFree: targetInfo?.free ?? null,
		projects: [],
	};
	const byVM = new Map<string, DiskRow[]>();
	for (const r of selected) {
		const staged = ctx.staged.get(r.vmKey);
		const moves = staged?.edit?.migrateVolumes ?? [];
		if (moves.some((m) => m.name === r.disk.name && m.storageClass === target)) {
			plan.already.push(r);
			continue;
		}
		const reason = moveBlock(r, target, staged);
		if (reason) {
			plan.blocked.push({ row: r, reason });
			continue;
		}
		if (!r.running) {
			plan.stopped.push(r);
			continue;
		}
		if (!byVM.has(r.vmKey)) byVM.set(r.vmKey, []);
		byVM.get(r.vmKey)!.push(r);
	}

	const rowsOfVM = new Map<string, DiskRow[]>();
	for (const r of ctx.rows) {
		if (r.ephemeral || !byVM.has(r.vmKey)) continue;
		if (!rowsOfVM.has(r.vmKey)) rowsOfVM.set(r.vmKey, []);
		rowsOfVM.get(r.vmKey)!.push(r);
	}
	const leaving = new Map<string, number>();
	const projects = new Set<string>();
	for (const [key, disks] of byVM) {
		const vm = disks[0].vm;
		// The staged edit is the wire VMEdit; it round-trips unchanged.
		const staged = ctx.staged.get(key)?.edit as Edit | undefined;
		const names = new Set(disks.map((d) => d.disk.name));
		const moves: VolumeMigration[] = [
			...(staged?.migrateVolumes ?? []).filter((m) => !names.has(m.name)),
			...disks.map((d) => ({ name: d.disk.name, storageClass: target })),
		];
		plan.edits.push({
			vm,
			project: disks[0].project,
			edit: { ...staged, migrateVolumes: moves },
			disks,
		});
		projects.add(disks[0].project);
		for (const d of disks) {
			leaving.set(d.storageClass, (leaving.get(d.storageClass) ?? 0) + 1);
			if (!Number.isNaN(d.size)) plan.added += d.size;
		}
		// After the merge each moved disk carries the target's access, the rest
		// keep their own: the VM live-migrates only when all are shared.
		const after = moves.map((m) => m.storageClass);
		const others = (rowsOfVM.get(key) ?? []).filter(
			(r) => !moves.some((m) => m.name === r.disk.name),
		);
		const targetShared = (cls: string) =>
			classes.get(cls)?.profile?.accessModes.includes(RWX) ?? false;
		if (after.every(targetShared) && others.every((r) => r.shared === true))
			plan.liveMigrateAfter++;
	}
	plan.moving = plan.edits.reduce((n, e) => n + e.disks.length, 0);
	plan.vms = plan.edits.length;
	plan.projects = [...projects].sort();
	plan.leaving = [...leaving.entries()]
		.filter(([storageClass]) => !classes.get(storageClass)?.default)
		.map(([storageClass, count]) => ({
			storageClass,
			count,
			description: classes.get(storageClass)?.description ?? '',
		}))
		.sort((a, b) => b.count - a.count || a.storageClass.localeCompare(b.storageClass));
	return plan;
}

const lower = (s: string) => s.charAt(0).toLowerCase() + s.slice(1);
const plural = (n: number, one: string, many = `${one}s`) => `${num(n)} ${n === 1 ? one : many}`;

// "Adds 49 moves to the team-web draft. web-1 rootdisk is already in it."
export function moveNote(plan: MovePlan): string {
	if (!plan.moving && !plan.already.length) return '';
	const drafts =
		plan.projects.length === 1
			? `the ${plan.projects[0]} draft`
			: `${plan.projects.length} project drafts`;
	const parts: string[] = [];
	if (plan.moving) parts.push(`Adds ${plural(plan.moving, 'move')} to ${drafts}.`);
	if (plan.already.length === 1) {
		const r = plan.already[0];
		parts.push(`${r.vm.name} ${r.disk.name} is already in it.`);
	} else if (plan.already.length > 1)
		parts.push(`${plural(plan.already.length, 'disk')} are already in it.`);
	return parts.join(' ');
}

export type Tone = 'ok' | 'warn' | 'danger' | 'info' | 'none';

// What staging the move means, one sentence each, before it is staged.
export function moveConsequences(plan: MovePlan): { tone: Tone; text: string }[] {
	const out: { tone: Tone; text: string }[] = [];
	if (plan.vms) {
		if (plan.liveMigrateAfter === plan.vms)
			out.push({
				tone: 'ok',
				text: `${plan.vms === 1 ? 'The VM' : `The ${num(plan.vms)} VMs`} can live-migrate once it merges.`,
			});
		else
			out.push({
				tone: 'warn',
				text: `${num(plan.liveMigrateAfter)} of ${plural(plan.vms, 'VM')} can live-migrate once it merges; the rest keep a local disk.`,
			});
		out.push({ tone: 'info', text: 'Running VMs keep running while their disk copies.' });
	}
	for (const l of plan.leaving) {
		const what = l.description ? `${lower(l.description)} on ${l.storageClass}` : l.storageClass;
		out.push({
			tone: 'warn',
			text: `${num(l.count)} ${l.count === 1 ? 'leaves' : 'leave'} ${what}.`,
		});
	}
	if (plan.moving) {
		const added = size(plan.added);
		if (plan.targetFree === null)
			out.push({
				tone: 'warn',
				text: `${plan.target} does not report free space. This adds ${added}.`,
			});
		else if (plan.added > plan.targetFree)
			out.push({
				tone: 'danger',
				text: `${plan.target} has ${size(plan.targetFree)} free. This adds ${added}.`,
			});
		else
			out.push({
				tone: 'ok',
				text: `Adds ${added} to ${plan.target}, ${size(plan.targetFree)} free.`,
			});
	}
	if (plan.stopped.length)
		out.push({
			tone: 'danger',
			text: `${plural(plan.stopped.length, 'disk')} of stopped VMs ${plan.stopped.length === 1 ? 'is' : 'are'} left out: a disk copies only while its VM runs.`,
		});
	if (plan.blocked.length) {
		const first = plan.blocked[0];
		out.push({
			tone: 'warn',
			text: `${plural(plan.blocked.length, 'disk')} ${plan.blocked.length === 1 ? 'is' : 'are'} left out: ${first.row.vm.name} ${first.row.disk.name} is ${first.reason}.`,
		});
	}
	return out;
}

// --- bulk snapshot ---

export interface SnapshotPlan {
	vms: VM[]; // one snapshot per VM covers its disks
	disks: number;
	unsupported: DiskRow[]; // on a class without snapshots: the snapshot leaves them out
}

export function planBulkSnapshot(selected: DiskRow[], classes: StorageClassInfo[]): SnapshotPlan {
	const snap = new Map(classes.map((c) => [c.name, !!c.snapshotClass]));
	const vms = new Map<string, VM>();
	const unsupported: DiskRow[] = [];
	let disks = 0;
	for (const r of selected) {
		if (r.ephemeral) continue;
		if (snap.get(r.storageClass) === false) unsupported.push(r);
		else disks++;
		vms.set(r.vmKey, r.vm);
	}
	return { vms: [...vms.values()], disks, unsupported };
}

// Runs call once per item, at most limit at a time, settling like
// Promise.allSettled in item order: a bulk action over thousands of disks
// must not open thousands of requests at once.
export async function settleEach<T>(
	items: T[],
	call: (item: T) => Promise<unknown>,
	limit = 8,
): Promise<PromiseSettledResult<unknown>[]> {
	const out: PromiseSettledResult<unknown>[] = new Array(items.length);
	let next = 0;
	async function worker() {
		while (next < items.length) {
			const i = next++;
			try {
				out[i] = { status: 'fulfilled', value: await call(items[i]) };
			} catch (reason) {
				out[i] = { status: 'rejected', reason };
			}
		}
	}
	await Promise.all(Array.from({ length: Math.min(limit, items.length) }, worker));
	return out;
}
