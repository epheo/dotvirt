// What the Replication tab derives from r-drbd's placement: which disks a host
// takes with it, and what is left of each tier. Pure, so a rehearsal never
// asks the cluster anything and adds no authority: it reads the copies the
// caller may already see.
import type {
	ClassReplication,
	DiskProtection,
	ReplicatedVolume,
	ReplicationPool,
	StorageClassInfo,
	VM,
} from '#lib/api.js';
import { size, type DiskRow } from '#lib/storage.js';
import type { Tone } from '#lib/status.js';

export type DiskFate = 'off' | 'weak' | 'ok';
export type RowFate = 'off' | 'restart' | 'weak' | 'ok';

export type MapDisk = { label: string; volume: ReplicatedVolume };
export type MapRow = {
	key: string;
	name: string;
	project: string;
	vm?: { namespace: string; name: string };
	runsOn: string;
	disks: MapDisk[];
};

const copies = (v: ReplicatedVolume) => v.replicas.filter((r) => r.role === 'diskful');
const inSync = (v: ReplicatedVolume) =>
	copies(v)
		.filter((r) => r.state === 'in-sync')
		.map((r) => r.node);

const join = (names: string[]) =>
	names.length <= 1 ? names.join('') : `${names.slice(0, -1).join(', ')} and ${names.at(-1)}`;

// Witnesses vote, a plain diskless attachment does not. Replicas already out
// of reach cannot vote for the survivors.
function keepsQuorum(v: ReplicatedVolume, host: string): boolean {
	const voters = v.replicas.filter((r) => r.role !== 'diskless');
	if (voters.length <= 1) return true;
	const left = voters.filter(
		(r) => r.node !== host && r.state !== 'unreachable' && r.state !== 'unknown',
	);
	return left.length * 2 > voters.length;
}

const ALREADY_OFFLINE = 'Already offline';
const settingUp = (v: ReplicatedVolume) =>
	!v.phase || v.phase === 'Pending' || v.phase === 'Provisioning';

export function diskIfDown(v: ReplicatedVolume, host: string): { fate: DiskFate; text: string } {
	const held = copies(v).some((r) => r.node === host);
	const left = inSync(v).filter((n) => n !== host);
	if (v.quorumLost || v.phase === 'Failed' || (!settingUp(v) && inSync(v).length === 0))
		return { fate: 'off', text: ALREADY_OFFLINE };
	if (settingUp(v)) return { fate: 'ok', text: 'Still being set up' };
	if (held && copies(v).length === 1) return { fate: 'off', text: `Its only copy is on ${host}` };
	// The other side stopped accepting this one's writes: nothing can take over.
	if (v.splitBrain && (v.primary === host || !v.primary))
		return { fate: 'off', text: 'Its copies disagree, none can take over' };
	if (held && left.length === 0)
		return { fate: 'off', text: `Its only in-sync copy is on ${host}` };
	if (!keepsQuorum(v, host)) return { fate: 'off', text: `Loses its majority with ${host}` };
	if (held)
		return left.length === 1
			? { fate: 'weak', text: 'Runs on one in-sync copy' }
			: { fate: 'ok', text: `${left.length} copies left, still protected` };
	if (v.replicas.some((r) => r.node === host && r.role === 'tiebreaker'))
		return { fate: 'ok', text: 'Loses its witness, keeps running' };
	return { fate: 'ok', text: 'Unaffected' };
}

export function rowIfDown(row: MapRow, host: string): { fate: RowFate; text: string } {
	const disks = row.disks.map((d) => diskIfDown(d.volume, host));
	const off = disks.filter((d) => d.fate === 'off');
	if (off.length)
		return {
			fate: 'off',
			text: off.every((d) => d.text === ALREADY_OFFLINE)
				? ALREADY_OFFLINE
				: `Offline until ${host} returns`,
		};
	if (row.runsOn === host) {
		const nearest = homeFor(row, host);
		return { fate: 'restart', text: nearest ? `Restarts, data on ${nearest}` : 'Restarts' };
	}
	if (disks.some((d) => d.fate === 'weak'))
		return { fate: 'weak', text: 'Keeps running, less protected' };
	return { fate: 'ok', text: 'Unaffected' };
}

// The surviving host holding the most of the row's in-sync copies.
function homeFor(row: MapRow, host: string): string {
	const held = new Map<string, number>();
	for (const d of row.disks)
		for (const n of inSync(d.volume)) if (n !== host) held.set(n, (held.get(n) ?? 0) + 1);
	return [...held].sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))[0]?.[0] ?? '';
}

export type Rehearsal = {
	host: string;
	byFate: Record<RowFate, MapRow[]>;
	maintenance: string;
	retire: string;
};

export function rehearse(rows: MapRow[], host: string): Rehearsal {
	const byFate: Record<RowFate, MapRow[]> = { off: [], restart: [], weak: [], ok: [] };
	for (const r of rows) byFate[rowIfDown(r, host).fate].push(r);
	const here = rows.filter((r) => r.runsOn === host);
	const pinned = (r: MapRow) => r.disks.some((d) => copies(d.volume).length === 1);
	const move = here.filter((r) => !pinned(r)).map((r) => r.name);
	const stop = here.filter(pinned).map((r) => r.name);
	const held = rows
		.flatMap((r) => r.disks)
		.filter((d) => copies(d.volume).some((c) => c.node === host));
	const rebuild = held.filter((d) => copies(d.volume).length > 1);
	const stuck = rows
		.filter((r) =>
			r.disks.some((d) => copies(d.volume).length === 1 && copies(d.volume)[0].node === host),
		)
		.map((r) => r.name);
	const bytes = rebuild.reduce((n, d) => n + d.volume.sizeBytes, 0);
	return {
		host,
		byFate,
		maintenance: [
			move.length ? `${join(move)} move away first.` : '',
			stop.length ? `${join(stop)} stop${stop.length === 1 ? 's' : ''}, on a local disk.` : '',
			`Copies catch up on the changed blocks when ${host} returns.`,
		]
			.filter(Boolean)
			.join(' '),
		retire: [
			rebuild.length
				? `Rebuilds ${rebuild.length} ${rebuild.length === 1 ? 'copy' : 'copies'} on other hosts, ${size(bytes)} read from peers.`
				: `No replicated disk has a copy on ${host}.`,
			stuck.length
				? `Local disks cannot move: recreate ${join(stuck)} on replicated storage first.`
				: '',
		]
			.filter(Boolean)
			.join(' '),
	};
}

// The columns: every host that holds a pool, a replica or a report.
export function hostsOf(
	volumes: ReplicatedVolume[],
	pools: ReplicationPool[],
	reported: string[],
): string[] {
	return [
		...new Set([
			...reported,
			...pools.map((p) => p.node),
			...volumes.flatMap((v) => v.replicas.map((r) => r.node)),
		]),
	]
		.filter(Boolean)
		.sort();
}

// Worst first, so what needs a person is on top; a rehearsal leaves the order
// alone, or rows would jump under the pointer.
function trouble(v: ReplicatedVolume): number {
	if (v.splitBrain || v.quorumLost || v.phase === 'Failed') return 3;
	if (v.replicas.some((r) => r.state === 'unreachable' || r.state === 'behind')) return 2;
	if (v.replicas.some((r) => r.state === 'catching-up')) return 1;
	return 0;
}

// Joins each volume to the VM whose disk claims it. A claim no VM uses, and a
// volume with no claim, still get a row: their copies occupy hosts all the same.
export function mapRows(volumes: ReplicatedVolume[], disks: DiskRow[]): MapRow[] {
	const owner = new Map(
		disks.filter((d) => d.disk.claim).map((d) => [`${d.vm.namespace}/${d.disk.claim}`, d] as const),
	);
	const rows = new Map<string, MapRow>();
	for (const volume of volumes) {
		const d = volume.claim ? owner.get(`${volume.namespace}/${volume.claim}`) : undefined;
		const key = d ? d.vmKey : `volume/${volume.name}`;
		let row = rows.get(key);
		if (!row) {
			row = {
				key,
				name: d?.vm.name ?? volume.claim ?? volume.name,
				project: d?.project ?? volume.namespace ?? 'Not claimed',
				vm: d ? { namespace: d.vm.namespace, name: d.vm.name } : undefined,
				runsOn: d?.host || volume.primary || '',
				disks: [],
			};
			rows.set(key, row);
		}
		row.disks.push({ label: d?.disk.name ?? (volume.claim ? 'disk' : volume.name), volume });
	}
	const worst = (r: MapRow) => Math.max(...r.disks.map((d) => trouble(d.volume)));
	return [...rows.values()].sort((a, b) => worst(b) - worst(a) || a.name.localeCompare(b.name));
}

export type TierRoom = {
	tier: string;
	pools: number;
	totalBytes: number;
	usedBytes: number;
	room: number[]; // each host's room for one new copy, largest first
	largest: number; // the largest new two-copy disk
	hosts: HostRoom[];
};

// A host's room in one tier: its largest pool takes one new disk, and all its
// pools together bound several.
export type HostRoom = { node: string; largest: number; total: number };

// Placement wants each copy on its own host, so the room for an N-copy disk
// is the Nth-roomiest host's.
export function tierRooms(pools: ReplicationPool[]): TierRoom[] {
	const tiers = new Map<string, ReplicationPool[]>();
	for (const p of pools) tiers.set(p.tier, [...(tiers.get(p.tier) ?? []), p]);
	return [...tiers]
		.map(([tier, list]) => {
			const byNode = new Map<string, HostRoom>();
			for (const p of list) {
				if (!p.available) continue;
				const h = byNode.get(p.node) ?? { node: p.node, largest: 0, total: 0 };
				h.largest = Math.max(h.largest, p.placeableBytes);
				h.total += p.placeableBytes;
				byNode.set(p.node, h);
			}
			const hosts = [...byNode.values()];
			const room = hosts.map((h) => h.largest).sort((a, b) => b - a);
			const totalBytes = list.reduce((n, p) => n + p.totalBytes, 0);
			return {
				tier,
				pools: list.length,
				totalBytes,
				usedBytes: totalBytes - list.reduce((n, p) => n + p.freeBytes, 0),
				room,
				largest: room[1] ?? 0,
				hosts,
			};
		})
		.sort((a, b) => a.tier.localeCompare(b.tier));
}

// One disk as it stands, in the map's last column.
export function diskNow(v: ReplicatedVolume, runsOn: string): { tone: Tone; text: string } {
	if (v.resolving)
		return { tone: 'info', text: `Keeping the copy on ${v.resolving}, the rest rebuild` };
	if (v.splitBrain) return { tone: 'danger', text: 'Two copies disagree' };
	if (v.quorumLost) return { tone: 'danger', text: 'Lost its majority, writes stopped' };
	if (v.phase === 'Failed') return { tone: 'danger', text: 'Failed' };
	if (settingUp(v)) return { tone: 'neutral', text: 'Being set up' };
	const out = copies(v).find((r) => r.state === 'unreachable');
	if (out) return { tone: 'warn', text: `Copy on ${out.node} is out of reach` };
	const catching = copies(v).find((r) => r.state === 'catching-up');
	if (catching)
		return {
			tone: 'info',
			text: `Copy on ${catching.node} catching up, ${catching.syncPercent ?? 0}%`,
		};
	const behind = copies(v).find((r) => r.state === 'behind');
	if (behind) return { tone: 'warn', text: `Copy on ${behind.node} is behind` };
	if (copies(v).length === 1)
		return { tone: 'neutral', text: `One copy, lost with ${copies(v)[0].node}` };
	const local = copies(v).some((r) => r.node === runsOn);
	return {
		tone: 'neutral',
		text: `${copies(v).length} copies in sync, ${local ? 'reads local' : 'reads over the network'}`,
	};
}

export type CellKind =
	'copy' | 'catching' | 'behind' | 'lost' | 'pending' | 'witness' | 'witness-lost' | 'remote' | '';
export type Cell = { kind: CellKind; percent: number; label: string };

// One disk on one host. A VM reading over the network is its own glyph: it is
// the placement fact an operator most wants to spot.
export function cellFor(v: ReplicatedVolume, host: string, runsOn: string): Cell {
	const r = v.replicas.find((x) => x.node === host);
	const none: Cell = { kind: '', percent: 0, label: '' };
	if (!r || r.role === 'diskless')
		return host === runsOn && copies(v).length > 0
			? { ...none, kind: 'remote', label: 'No copy here, reads over the network' }
			: none;
	if (r.role === 'tiebreaker')
		return r.state === 'unreachable'
			? { ...none, kind: 'witness-lost', label: 'Witness, out of reach' }
			: { ...none, kind: 'witness', label: 'Witness' };
	switch (r.state) {
		case 'in-sync':
			return { ...none, kind: 'copy', label: 'Copy, in sync' };
		case 'catching-up':
			return {
				kind: 'catching',
				percent: r.syncPercent ?? 0,
				label: `Copy, catching up, ${r.syncPercent ?? 0}%`,
			};
		case 'unreachable':
			return { ...none, kind: 'lost', label: 'Copy, out of reach' };
		case 'behind':
			return { ...none, kind: 'behind', label: 'Copy, behind' };
	}
	return { ...none, kind: 'pending', label: 'Copy, no report yet' };
}

export type Protection = {
	survive: number;
	total: number;
	segments: ('safe' | 'thin' | 'local' | 'trouble')[];
	notes: string[];
};

const someOf = (names: string[]) =>
	names.length <= 3 ? join(names) : `${names.slice(0, 3).join(', ')} and ${names.length - 3} more`;

// The calm card: how much survives a host, and only what needs saying.
export function protectionOf(rows: MapRow[]): Protection {
	const disks = rows.flatMap((r) =>
		r.disks.map((d) => ({ row: r.name, disk: d.label, v: d.volume })),
	);
	const notes: string[] = [];
	for (const d of disks) {
		if (d.v.splitBrain)
			notes.push(`${d.row} ${d.disk}: two copies disagree. Pick the one to keep.`);
		else if (d.v.quorumLost)
			notes.push(`${d.row} ${d.disk}: lost its majority, writes are stopped.`);
		const catching = copies(d.v).find((r) => r.state === 'catching-up');
		if (catching && inSync(d.v).length === 1)
			notes.push(
				`${d.row} ${d.disk}: the copy on ${catching.node} is catching up, ${catching.syncPercent ?? 0}%. Until then, losing ${inSync(d.v)[0]} takes it offline.`,
			);
	}
	const locals = disks.filter((d) => copies(d.v).length === 1).map((d) => d.row);
	if (locals.length)
		notes.push(
			`${locals.length} ${locals.length === 1 ? 'disk keeps' : 'disks keep'} one copy by choice: ${someOf([...new Set(locals)])}.`,
		);
	const broken = (v: ReplicatedVolume) => v.splitBrain || v.quorumLost || v.phase === 'Failed';
	// The rehearsal's own answer, asked of every host holding a replica, so the
	// summary can never disagree with what a click on a host shows.
	const survives = (v: ReplicatedVolume) =>
		!broken(v) && v.replicas.every((r) => diskIfDown(v, r.node).fate !== 'off');
	return {
		survive: disks.filter((d) => survives(d.v)).length,
		total: disks.length,
		segments: disks.map((d) =>
			broken(d.v) ? 'trouble' : copies(d.v).length <= 1 ? 'local' : survives(d.v) ? 'safe' : 'thin',
		),
		notes,
	};
}

// One VM's replicated disks as a row of the map, or null when it has none.
export function vmRow(vm: VM, volumes: ReplicatedVolume[]): MapRow | null {
	const disks = (vm.disks ?? []).flatMap((d) => {
		const volume = d.claim
			? volumes.find((v) => v.namespace === vm.namespace && v.claim === d.claim)
			: undefined;
		return volume ? [{ label: d.name, volume }] : [];
	});
	if (disks.length === 0) return null;
	return {
		key: `${vm.namespace}/${vm.name}`,
		name: vm.name,
		project: '',
		vm: { namespace: vm.namespace, name: vm.name },
		runsOn: vm.nodeName || disks.find((d) => d.volume.primary)?.volume.primary || '',
		disks,
	};
}

// What losing each host would do to this VM, leaving out the hosts that would
// not touch it: calm by exception.
export function hostLosses(row: MapRow): { host: string; fate: RowFate; text: string }[] {
	return hostsOf(
		row.disks.map((d) => d.volume),
		[],
		[row.runsOn],
	)
		.map((host) => ({ host, ...rowIfDown(row, host) }))
		.filter((l) => l.fate !== 'ok');
}

// One square per copy, never a place: all a tenant may know of where they are.
export function copySquares(p: DiskProtection): { kind: CellKind; percent: number }[] {
	return Array.from({ length: p.copies }, (_, i) =>
		i < p.inSync
			? { kind: 'copy', percent: 0 }
			: p.syncPercent !== undefined && i === p.inSync
				? { kind: 'catching', percent: p.syncPercent }
				: { kind: 'pending', percent: 0 },
	);
}

export type Standing = { tone: Tone; label: string; detail: string };

// r-drbd suspends IO without a majority of its voters, so a third copy buys
// more ways to stay in sync, not a second host lost.
function survivesText(copies: number): string {
	const hosts = Math.max(1, Math.floor((copies - 1) / 2));
	return `Survives losing ${hosts === 1 ? 'any one host' : `any ${hosts} hosts`}.`;
}

// What each number of copies gets a new disk, as r-drbd places and replicates
// it (docs/concepts.md): one card per count a class may ask, 1 to 3.
const CHOICES: { copies: number; title: string; lines: string[] }[] = [
	{
		copies: 1,
		title: 'One copy',
		lines: [
			'Lost with its host.',
			'Writes stay on its host, with no copy to wait for.',
			'Its VM stays on that host: no live migration.',
		],
	},
	{
		copies: 2,
		title: 'Two copies',
		lines: [
			survivesText(2),
			'A third host keeps a tiebreaker, with no data.',
			'Each write waits for both copies.',
		],
	},
	{
		copies: 3,
		title: 'Three copies',
		lines: [
			survivesText(3),
			'Two copies remain after losing a host.',
			'Each write waits for all three copies.',
		],
	},
];

// hosts: each host's room in the class's tier, for one-copy disks that share one.
export type ProtectionClass = {
	info: StorageClassInfo;
	tier: string;
	room?: number;
	hosts?: HostRoom[];
};
export type ProtectionChoice = {
	copies: number;
	title: string;
	lines: string[];
	classes: ProtectionClass[]; // none: no class the caller may list asks this many
};

// rooms: each tier's room, or null where the caller may not read the pools,
// so a tenant is never shown a number it could not read itself.
export function protectionChoices(
	classes: StorageClassInfo[],
	rooms: TierRoom[] | null,
): ProtectionChoice[] {
	return CHOICES.map((c) => ({
		...c,
		classes: classes
			.filter((x) => x.replication?.copies === c.copies)
			.map((info) => {
				const tier = info.replication!.tier;
				const t = rooms?.find((x) => x.tier === tier);
				return rooms
					? { info, tier, room: t?.room[c.copies - 1] ?? 0, hosts: t?.hosts ?? [] }
					: { info, tier };
			})
			.sort(
				(a, b) =>
					Number(!!b.info.default) - Number(!!a.info.default) ||
					(b.room ?? 0) - (a.room ?? 0) ||
					a.info.name.localeCompare(b.info.name),
			),
	}));
}

// One-copy disks of one VM share its host: each is pinned to its node
// (r-drbd docs/volumes.md), the scheduler weighs each alone against a host's
// largest pool, and placement takes the pool with the most room. So a set
// surely fits a host whose largest pool holds it all, surely not one whose
// pools together cannot, and may not in between.
export type LocalFit = 'fits' | 'maybe' | 'too-big';
function fitOn(h: HostRoom, disks: number[]): LocalFit {
	const sum = disks.reduce((n, b) => n + b, 0);
	if (Math.max(...disks) > h.largest || sum > h.total) return 'too-big';
	return sum <= h.largest ? 'fits' : 'maybe';
}

// host: where a one-copy disk the VM already holds pins it. Without one, the
// verdict holds for every host the scheduler may pick.
export function localFit(hosts: HostRoom[], disks: number[], host?: string): LocalFit {
	if (host) {
		const h = hosts.find((x) => x.node === host);
		return h ? fitOn(h, disks) : 'too-big';
	}
	const big = Math.max(...disks);
	const each = hosts.filter((h) => h.largest >= big).map((h) => fitOn(h, disks));
	if (each.every((v) => v === 'too-big')) return 'too-big';
	return each.every((v) => v === 'fits') ? 'fits' : 'maybe';
}

// The host a one-copy disk of this VM pins it to, if it holds one.
export function pinnedHost(vm: VM, volumes: ReplicatedVolume[]): string | undefined {
	const local = vmRow(vm, volumes)?.disks.find((d) => d.volume.copies === 1);
	return local && (copies(local.volume)[0]?.node ?? local.volume.pinnedNode);
}

// Whether a new disk fits the tier today; null when the room is unknown.
// bytes: NaN or 0 while the size is unreadable, then only the room is said.
// local: for one copy, the sizes of the VM's other new one-copy disks in this
// tier, and the host one it already holds pins it to.
export function roomText(
	c: ProtectionClass,
	copies: number,
	bytes: number,
	local?: { others: number[]; host?: string },
): { ok: boolean; text: string } | null {
	if (c.room === undefined) return null;
	const at = copies === 1 ? 'one copy' : `${copies} copies`;
	if (copies === 1 && c.hosts && local?.host) return pinnedText(c, bytes, local.others, local.host);
	if (!c.room) return { ok: false, text: `No room on ${c.tier} for a new disk at ${at} today.` };
	const room = `Room on ${c.tier}: ${size(c.room)} at ${at}.`;
	if (!(bytes > 0)) return { ok: true, text: room };
	if (bytes > c.room) return { ok: false, text: `Too big. ${room}` };
	if (copies !== 1 || !c.hosts || !local?.others.length) return { ok: true, text: `Fits. ${room}` };
	switch (localFit(c.hosts, [bytes, ...local.others])) {
		case 'fits':
			return { ok: true, text: `Fits with the VM's other one-copy disks, which share its host.` };
		case 'maybe':
			return { ok: false, text: `May not fit on one host with the VM's other one-copy disks.` };
		default:
			return {
				ok: false,
				text: `Too big. No host has room on ${c.tier} for the VM's one-copy disks together.`,
			};
	}
}

function pinnedText(
	c: ProtectionClass,
	bytes: number,
	others: number[],
	host: string,
): { ok: boolean; text: string } {
	const where = `${host}, where the VM's one-copy disk lives`;
	const h = c.hosts!.find((x) => x.node === host);
	if (!h?.largest) return { ok: false, text: `No room on ${c.tier} on ${where}.` };
	const room = `Room on ${c.tier} there: ${size(h.largest)}.`;
	if (!(bytes > 0)) return { ok: true, text: `Goes on ${where}. ${room}` };
	const on = others.length ? `${host} with the other new one-copy disks` : where;
	switch (localFit(c.hosts!, [bytes, ...others], host)) {
		case 'fits':
			return { ok: true, text: `Fits on ${where}. ${room}` };
		case 'maybe':
			return { ok: false, text: `May not fit on ${on}. ${room}` };
		default:
			return { ok: false, text: `Too big for ${on}. ${room}` };
	}
}

// What a class promises a disk, before it exists.
export function classPromise(r: ClassReplication): { ok: boolean; text: string } {
	const tier = r.tier ? `, ${r.tier}` : '';
	return r.copies <= 1
		? { ok: false, text: `One copy${tier}: lost with its host` }
		: {
				ok: true,
				text: `${r.copies} copies${tier}: ${survivesText(r.copies).toLowerCase().replace(/\.$/, '')}`,
			};
}

export function standingOf(p: DiskProtection): Standing {
	switch (p.standing) {
		case 'protected':
			return { tone: 'ok', label: 'Protected', detail: survivesText(p.copies) };
		case 'local':
			return {
				tone: 'neutral',
				label: 'One copy',
				detail: 'Fast, but lost with its host, and the VM cannot live-migrate.',
			};
		case 'catching-up':
			return {
				tone: 'info',
				label: `Catching up, ${p.syncPercent ?? 0}%`,
				detail: `${p.inSync} of ${p.copies} copies in sync. Nothing to do.`,
			};
		case 'exposed':
			return {
				tone: 'warn',
				label: 'Less protected',
				detail: `${
					p.inSync >= p.copies
						? 'It would not survive losing a host right now.'
						: `${p.inSync} of ${p.copies} copies in sync.`
				} Tell your platform team if it lasts.`,
			};
		case 'needs-operator':
			return {
				tone: 'danger',
				label: 'Needs your platform team',
				detail: 'This disk cannot recover by itself. Ask them to look at it.',
			};
		default:
			return { tone: 'neutral', label: 'Being set up', detail: 'Copies are being created.' };
	}
}
