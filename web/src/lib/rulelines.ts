// "Rules that apply" as lines: one sentence per rule (the server renders it
// from the typed rule), the tier and policy it comes from, in the evaluator's
// order. The order is the evaluator's, never re-ranked: a cluster rule is met
// before a project rule, and the list must not suggest otherwise.
import type { EffectivePolicy, NetworkRules, Policy, PolicyBinding } from '#lib/api.js';

// How a rule relates to what is described: it binds its own VMs, it names
// them from elsewhere, or it merely matches them from elsewhere ("any
// namespace" and the like - on a real cluster, most of the platform's rules).
export type RuleGroup = 'own' | 'names' | 'wildcard';

export interface RuleLine {
	key: string;
	icon: 'allow' | 'deny' | 'out' | 'pass';
	text: string;
	tier: string; // "Cluster rule", "Project rule", ...
	group: RuleGroup;
	policy?: Policy;
	note?: string; // "Priority 10", "For VMs labeled app=db in db-prod"
}

export function tierOf(p: Policy): string {
	switch (p.kind) {
		case 'admin':
			return 'Cluster rule';
		case 'baseline':
			return 'Cluster default rule';
		case 'gateway':
			return 'Outbound rule';
		case 'egressip':
			return 'Fixed outbound IP';
		case 'route':
			return 'External route';
	}
	return 'Project rule';
}

function iconOf(direction: string, action: string): RuleLine['icon'] {
	if (action === 'Deny') return 'deny';
	if (action === 'Pass') return 'pass';
	return direction === 'Egress' ? 'out' : 'allow';
}

function linesOf(b: PolicyBinding): RuleLine[] {
	const p = b.policy;
	const note = [
		p.kind === 'admin' ? `priority ${p.priority ?? 0}` : '',
		b.conditional && p.applies ? `for ${p.applies}` : '',
	]
		.filter(Boolean)
		.join(', ');
	const id = `${p.backing}:${p.namespace ?? ''}:${p.name}`;
	const out: RuleLine[] = (p.rules ?? [])
		.filter((r) => r.text)
		.map((r, i) => ({
			key: `${id}:${i}`,
			icon: iconOf(r.direction, r.action),
			text: r.text!,
			tier: tierOf(p),
			group: 'own' as const,
			policy: p,
			note: note || undefined,
		}));
	if (p.isolation)
		out.push({
			key: `${id}:isolation`,
			icon: 'deny',
			text: p.isolation,
			tier: tierOf(p),
			group: 'own',
			policy: p,
			note: note || undefined,
		});
	return out;
}

/**
 * The lines for one VM: the incoming chain, then outbound. With no outbound
 * rule the VM reaches anything outside, which is worth saying.
 */
export function vmRuleLines(eff: EffectivePolicy, project: string): RuleLine[] {
	const out = (eff.eastWest ?? []).flatMap(linesOf);
	if (eff.gateway?.length) out.push(...eff.gateway.flatMap(linesOf));
	else
		out.push({
			key: 'outbound:none',
			icon: 'out',
			text: 'Reaches anything outside.',
			tier: `No outbound rules in ${project}`,
			group: 'own',
		});
	return out;
}

/** The lines for one network: what binds its VMs, then rules elsewhere naming them. */
export function networkRuleLines(rules: NetworkRules): RuleLine[] {
	const out = rules.applies.flatMap(linesOf);
	for (const [i, r] of rules.reaches.entries())
		out.push({
			key: `peer:${r.policy.backing}:${r.policy.namespace ?? ''}:${r.policy.name}:${i}`,
			icon: iconOf(r.rule.direction === 'Ingress' ? 'Egress' : 'Ingress', r.rule.action),
			text: r.text,
			tier: tierOf(r.policy),
			group: r.wildcard ? 'wildcard' : 'names',
			policy: r.policy,
			note: r.conditional ? 'only the VMs its labels pick' : undefined,
		});
	return out;
}

/** Whether a project rule of its own binds these VMs (else only cluster rules do). */
export const hasOwnRule = (lines: RuleLine[]) =>
	lines.some((l) => l.group === 'own' && l.policy?.kind === 'dfw');

/**
 * The wildcard rules folded into one sentence: never worth listing one by
 * one in a summary, but worth knowing about. '' when there are none.
 */
export function wildcardSummary(lines: RuleLine[]): string {
	const wild = lines.filter((l) => l.group === 'wildcard');
	const n = new Set(wild.map((l) => l.key.slice(0, l.key.lastIndexOf(':')))).size;
	if (!n) return '';
	const where = wild.every((l) => l.policy?.namespace) ? 'in other projects' : 'elsewhere';
	return `${n} rule${n === 1 ? '' : 's'} ${where} ${n === 1 ? 'lets' : 'let'} any namespace reach ${n === 1 ? 'its' : 'their'} own VMs.`;
}

/**
 * The n lines most worth a glance, kept in the evaluator's order: one of each
 * kind first (a project rule letting traffic in, the first cluster rule that
 * blocks, a rule elsewhere naming these VMs, what reaches outside), then the
 * rest in order. Wildcard rules never make the cut (see wildcardSummary).
 */
export function topLines(all: RuleLine[], n: number): RuleLine[] {
	const lines = all.filter((l) => l.group !== 'wildcard');
	const firstDeny = lines.find((l) => l.policy?.kind === 'admin' && l.icon === 'deny');
	const kind = (l: RuleLine) => {
		if (l.key.startsWith('peer:')) return 2;
		if (l.policy?.kind === 'dfw' && l.icon !== 'deny') return 0;
		if (l === firstDeny) return 1;
		if (l.icon === 'out') return 3;
		return 4;
	};
	const firsts = new Map<number, number>();
	lines.forEach((l, i) => {
		const k = kind(l);
		if (k < 4 && !firsts.has(k)) firsts.set(k, i);
	});
	const order = [
		...[...firsts.entries()].sort((a, b) => a[0] - b[0]).map(([, i]) => i),
		...lines.map((_, i) => i).filter((i) => ![...firsts.values()].includes(i)),
	];
	const picked = new Set(order.slice(0, n));
	return lines.filter((_, i) => picked.has(i));
}
