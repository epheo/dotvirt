import { vmKey, type VM } from '#lib/api.js';
import { plural } from '#lib/format.js';

// The status line a page header says about its scope: how a host stands, which
// hosts need a person, and what a scope's VMs are doing.

export type HeadTone = 'ok' | 'warn' | 'danger' | 'off';
export type Headline = { tone: HeadTone; text: string };

type NodeFlags = { ready?: boolean; unschedulable?: boolean; maintenance?: boolean };
export type HostLabel = 'Ready' | 'Maintenance' | 'Not ready' | 'Cordoned';

// Maintenance is the intent marker and outranks the cordon it implies; a host
// that is not ready says so before a plain cordon.
export function hostState(n: NodeFlags): { tone: 'ok' | 'warn' | 'danger'; label: HostLabel } {
	if (n.maintenance) return { tone: 'warn', label: 'Maintenance' };
	if (n.ready === false) return { tone: 'danger', label: 'Not ready' };
	if (n.unschedulable) return { tone: 'warn', label: 'Cordoned' };
	return { tone: 'ok', label: 'Ready' };
}

// The hosts that need a person, worst first; null when every host is ready.
export function hostsAttention(nodes: NodeFlags[]): Headline | null {
	const n: Record<HostLabel, number> = { 'Not ready': 0, Maintenance: 0, Cordoned: 0, Ready: 0 };
	for (const node of nodes) n[hostState(node).label]++;
	const parts = (
		[
			['Not ready', 'not ready'],
			['Maintenance', 'in maintenance'],
			['Cordoned', 'cordoned'],
		] as const
	)
		.filter(([k]) => n[k] > 0)
		.map(([k, what], i) => `${i ? n[k].toLocaleString('en-US') : plural(n[k], 'host')} ${what}`);
	if (!parts.length) return null;
	return { tone: n['Not ready'] ? 'danger' : 'warn', text: parts.join(', ') };
}

export type VMStats = { issues: number; inChange: number; phases: [string, number][] };

const ORDER = ['Running', 'Paused', 'Stopped'];

// What a scope's VMs are doing: how many carry a standing issue, how many sit
// in a change (staged or in an open PR), and the count per phase, running first.
export function vmStats(
	vms: VM[],
	issueKeys: ReadonlySet<string>,
	inChange: (key: string) => boolean,
): VMStats {
	const phases = new Map<string, number>();
	let issues = 0;
	let changed = 0;
	for (const vm of vms) {
		const k = vmKey(vm);
		if (issueKeys.has(k)) issues++;
		if (inChange(k)) changed++;
		const p = vm.paused ? 'Paused' : (vm.phase ?? (vm.power === 'Off' ? 'Stopped' : ''));
		if (p) phases.set(p, (phases.get(p) ?? 0) + 1);
	}
	return {
		issues,
		inChange: changed,
		phases: [...phases].sort(
			([a], [b]) => (ORDER.indexOf(a) + 1 || 9) - (ORDER.indexOf(b) + 1 || 9),
		),
	};
}

// Issues first, then each phase: "2 with issues, 391 running, 19 stopped".
export function vmHeadline(s: VMStats): Headline | null {
	const n = (v: number) => v.toLocaleString('en-US');
	const parts = [
		...(s.issues ? [`${n(s.issues)} with issues`] : []),
		...s.phases.map(([p, c]) => `${n(c)} ${p.toLowerCase()}`),
	];
	if (!parts.length) return null;
	const tone = s.issues ? 'danger' : s.phases.some(([p]) => p === 'Running') ? 'ok' : 'off';
	return { tone, text: parts.join(', ') };
}
