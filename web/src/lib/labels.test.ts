import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import {
	DRAFT_RESOURCE_LABEL,
	POLICY_KIND_LABEL,
	TRACE_STAGE_LABEL,
	draftResourceLabel,
	policyKindLabel,
	traceStageLabel,
} from './labels';

// The label maps must cover every raw value the server sends, read from the
// same sources it is defined in: a value added there without a label here
// fails, rather than reaching the screen as itself.

const read = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8');
const gen = read('./model.gen.ts');

const policyKinds = [...gen.matchAll(/export const Policy\w+: PolicyKind = "(\w+)"/g)].map(
	(m) => m[1],
);
const traceStages = (gen.match(/^\s+stage: string; \/\/ (.+)$/m)?.[1] ?? '')
	.split('|')
	.map((s) => s.trim())
	.filter(Boolean);
// The draft's resource names are Go-side only (kinds.go is not a wire type).
const draftResources = [
	...read('../../../internal/model/kinds.go').matchAll(/^\t\{Name: "(\w+)"/gm),
].map((m) => m[1]);

// Words a person never reads: the raw values themselves.
const raw = /\b(dfw|drs|segment|gateway)\b/i;

describe('labels', () => {
	it('names every PolicyKind', () => {
		expect(policyKinds.length).toBeGreaterThan(4);
		for (const k of policyKinds) expect(POLICY_KIND_LABEL[k], k).toBeTruthy();
	});

	it('names every TraceStep stage', () => {
		expect(traceStages).toContain('segment');
		for (const s of traceStages) expect(TRACE_STAGE_LABEL[s], s).toBeTruthy();
	});

	it('names every draft resource', () => {
		expect(draftResources).toContain('drs');
		for (const r of draftResources) expect(DRAFT_RESOURCE_LABEL[r], r).toBeTruthy();
	});

	it('never says a raw value, known or not', () => {
		const labels = [
			...Object.values(POLICY_KIND_LABEL),
			...Object.values(TRACE_STAGE_LABEL),
			...Object.values(DRAFT_RESOURCE_LABEL),
		];
		for (const l of labels) expect(l).not.toMatch(raw);
		expect(policyKindLabel('dfw')).toBe('Project rule');
		expect(traceStageLabel('segment')).toBe('Network');
		expect(draftResourceLabel('drs')).toBe('Rebalancing');
		expect(policyKindLabel('nsx-something')).toBe('Rule');
		expect(traceStageLabel('tier1')).toBe('Step');
		expect(draftResourceLabel('portgroup')).toBe('Object');
		expect(draftResourceLabel('toString')).toBe('Object');
	});
});
