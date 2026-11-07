import { describe, expect, it } from 'vitest';
import type { EffectivePolicy, NetworkRules, Policy } from '#lib/api.js';
import { hasOwnRule, networkRuleLines, topLines, vmRuleLines, wildcardSummary } from './rulelines';

const denyLateral: Policy = {
	name: 'deny-lateral',
	kind: 'admin',
	backing: 'AdminNetworkPolicy',
	priority: 10,
	applies: 'every namespace',
	rules: [
		{ direction: 'Ingress', action: 'Deny', text: 'Blocks all traffic from VMs in any namespace.' },
	],
};
const allowDB: Policy = {
	name: 'allow-web-to-db',
	kind: 'dfw',
	namespace: 'db-prod',
	backing: 'NetworkPolicy',
	applies: 'VMs labeled app=db in db-prod',
	isolation: 'Blocks other incoming traffic.',
	rules: [
		{
			direction: 'Ingress',
			action: 'Allow',
			text: 'Accepts TCP 5432 from VMs labeled app=web in web-prod.',
		},
	],
};

describe('vmRuleLines', () => {
	it('keeps the evaluator order and says when nothing limits outbound', () => {
		const eff: EffectivePolicy = {
			namespace: 'db-prod',
			eastWest: [{ policy: denyLateral }, { policy: allowDB }],
		};
		const lines = vmRuleLines(eff, 'team-db');
		expect(lines.map((l) => [l.icon, l.text, l.tier, l.note])).toEqual([
			['deny', 'Blocks all traffic from VMs in any namespace.', 'Cluster rule', 'priority 10'],
			[
				'allow',
				'Accepts TCP 5432 from VMs labeled app=web in web-prod.',
				'Project rule',
				undefined,
			],
			['deny', 'Blocks other incoming traffic.', 'Project rule', undefined],
			['out', 'Reaches anything outside.', 'No outbound rules in team-db', undefined],
		]);
	});
});

describe('networkRuleLines', () => {
	it('lists bindings, conditional ones with their subject, then peer rules', () => {
		const rules: NetworkRules = {
			network: 'db-prod/db-net',
			namespaces: ['db-prod'],
			applies: [{ policy: denyLateral }, { policy: allowDB, conditional: true }],
			reaches: [
				{
					policy: { name: 'scrape', kind: 'dfw', namespace: 'mon', backing: 'NetworkPolicy' },
					rule: { direction: 'Egress', action: 'Allow' },
					text: 'Every VM in mon can reach VMs in db-prod.',
				},
			],
		};
		const lines = networkRuleLines(rules);
		expect(lines[1].note).toBe('for VMs labeled app=db in db-prod');
		expect(lines.at(-1)).toMatchObject({ icon: 'allow', tier: 'Project rule' });
		expect(new Set(lines.map((l) => l.key)).size).toBe(lines.length);
	});
});

describe('topLines', () => {
	it('picks what a reader needs first but keeps the evaluation order', () => {
		const allowDNS: Policy = {
			name: 'allow-dns',
			kind: 'admin',
			backing: 'AdminNetworkPolicy',
			priority: 5,
			rules: [{ direction: 'Ingress', action: 'Allow', text: 'Accepts UDP 53 from VMs in dns.' }],
		};
		const lines = vmRuleLines(
			{
				namespace: 'db-prod',
				eastWest: [{ policy: allowDNS }, { policy: denyLateral }, { policy: allowDB }],
			},
			'team-db',
		);
		expect(topLines(lines, 3).map((l) => l.text)).toEqual([
			'Blocks all traffic from VMs in any namespace.',
			'Accepts TCP 5432 from VMs labeled app=web in web-prod.',
			'Reaches anything outside.',
		]);
	});
});

describe('topLines on a network', () => {
	it('shows one of each kind before a second project rule', () => {
		const more: Policy = { ...allowDB, name: 'allow-more', isolation: undefined };
		const lines = networkRuleLines({
			network: 'web-prod/web-net',
			namespaces: ['web-prod'],
			applies: [{ policy: denyLateral }, { policy: allowDB }, { policy: more }],
			reaches: [
				{
					policy: allowDB,
					rule: { direction: 'Ingress', action: 'Allow' },
					text: 'VMs in web-prod can reach db-1.',
				},
			],
		});
		expect(topLines(lines, 3).map((l) => l.text)).toEqual([
			'Blocks all traffic from VMs in any namespace.',
			'Accepts TCP 5432 from VMs labeled app=web in web-prod.',
			'VMs in web-prod can reach db-1.',
		]);
	});
});

describe('wildcard rules', () => {
	const platform = (ns: string): NetworkRules['reaches'][number] => ({
		policy: { name: `allow-${ns}`, kind: 'dfw', namespace: ns, backing: 'NetworkPolicy' },
		rule: { direction: 'Ingress', action: 'Allow', peer: 'ns any', ports: 'TCP/8082' },
		text: `VMs in any namespace can reach every VM in ${ns} on TCP 8082.`,
		wildcard: true,
	});
	const rules: NetworkRules = {
		network: 'default/udn-frontend',
		namespaces: ['default'],
		applies: [{ policy: denyLateral }],
		reaches: [platform('openshift-gitops'), platform('openshift-monitoring')],
	};
	it('never lists them in a summary, folds them into one line', () => {
		const lines = networkRuleLines(rules);
		expect(topLines(lines, 3).map((l) => l.group)).toEqual(['own']);
		expect(wildcardSummary(lines)).toBe(
			'2 rules in other projects let any namespace reach their own VMs.',
		);
	});
	it('says when no project rule of its own binds the VMs', () => {
		expect(hasOwnRule(networkRuleLines(rules))).toBe(false);
		expect(
			hasOwnRule(vmRuleLines({ namespace: 'db-prod', eastWest: [{ policy: allowDB }] }, 'x')),
		).toBe(true);
	});
});
