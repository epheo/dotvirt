import { describe, expect, it } from 'vitest';
import { hunks, lineDiff } from './textdiff';

const vm = (cores: number, extra = '') =>
	['kind: VirtualMachine', 'metadata:', '  name: web', 'spec:', `  cores: ${cores}`, extra]
		.filter(Boolean)
		.join('\n') + '\n';

describe('lineDiff', () => {
	it('marks a changed line as one removal and one addition', () => {
		const d = lineDiff(vm(2), vm(4));
		expect(d.filter((l) => l.kind !== 'same')).toEqual([
			{ kind: 'del', text: '  cores: 2' },
			{ kind: 'add', text: '  cores: 4' },
		]);
		expect(d).toHaveLength(6);
	});

	it('keeps an appended line as a pure addition', () => {
		const d = lineDiff(vm(2), vm(2, '  memory: 4Gi'));
		expect(d.filter((l) => l.kind !== 'same')).toEqual([{ kind: 'add', text: '  memory: 4Gi' }]);
	});

	it('treats an empty side as all added or all removed', () => {
		expect(lineDiff('', 'a\nb\n').map((l) => l.kind)).toEqual(['add', 'add']);
		expect(lineDiff('a\n', '').map((l) => l.kind)).toEqual(['del']);
	});

	it('is empty for identical text', () => {
		expect(lineDiff(vm(2), vm(2)).every((l) => l.kind === 'same')).toBe(true);
	});
});

describe('hunks', () => {
	it('collapses a long unchanged run to a skip row with context on both sides', () => {
		const same = Array.from({ length: 20 }, (_, i) => `line ${i}`).join('\n');
		const d = lineDiff(`${same}\nold`, `${same}\nnew`);
		const h = hunks(d);
		expect(h[0]).toEqual({ kind: 'skip', count: 17 });
		expect(h.slice(1).map((r) => r.kind)).toEqual(['same', 'same', 'same', 'del', 'add']);
	});

	it('leaves a short run alone', () => {
		const d = lineDiff('a\nb\nc\n', 'a\nB\nc\n');
		expect(hunks(d).map((r) => r.kind)).toEqual(['same', 'del', 'add', 'same']);
	});
});
