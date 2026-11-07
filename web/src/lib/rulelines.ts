// "Rules that apply" as lines: one sentence per rule (the server renders it
// from the typed rule), the tier and policy it comes from, in the evaluator's
// order. The order is the evaluator's, never re-ranked: a cluster rule is met
// before a project rule, and the list must not suggest otherwise.
import type { EffectivePolicy, NetworkRules, Policy, PolicyBinding } from '#lib/api.js';

export interface RuleLine {
	key: string;
	icon: 'allow' | 'deny' | 'out' | 'pass';
	text: string;
	tier: string; // "Cluster rule", "Project rule", ...
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
			policy: p,
			note: note || undefined,
		}));
	if (p.isolation)
		out.push({
			key: `${id}:isolation`,
			icon: 'deny',
			text: p.isolation,
			tier: tierOf(p),
			policy: p,
			note: note || undefined,
		});
	return out;
}

/**
 * The lines for one VM: the east-west chain, then outbound. With no outbound
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
			policy: r.policy,
			note: r.conditional ? 'only the VMs its labels pick' : undefined,
		});
	return out;
}

/**
 * The n lines most worth a glance, kept in the evaluator's order: rules a
 * project wrote to let traffic in, then the first cluster rule that blocks
 * and rules elsewhere naming these VMs, then what reaches outside, then the
 * rest.
 */
export function topLines(lines: RuleLine[], n: number): RuleLine[] {
	const firstDeny = lines.find((l) => l.policy?.kind === 'admin' && l.icon === 'deny');
	const score = (l: RuleLine) => {
		if (l.key.startsWith('peer:')) return 1;
		if (l.policy?.kind === 'dfw' && l.icon !== 'deny') return 0;
		if (l === firstDeny) return 1;
		if (l.icon === 'out') return 2;
		return 3;
	};
	const picked = new Set(
		lines
			.map((l, i) => ({ i, s: score(l) }))
			.sort((a, b) => a.s - b.s || a.i - b.i)
			.slice(0, n)
			.map((x) => x.i),
	);
	return lines.filter((_, i) => picked.has(i));
}
