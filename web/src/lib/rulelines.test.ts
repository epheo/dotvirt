import { describe, expect, it } from 'vitest';
import type { EffectivePolicy, NetworkRules, Policy } from '#lib/api.js';
import { networkRuleLines, topLines, vmRuleLines } from './rulelines';

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
