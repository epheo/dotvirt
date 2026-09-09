// Line diff for the Manifest fold: the committed text against the edited one,
// rendered as unified +/- lines with the unchanged middle collapsed. Manifests
// are short and edits touch a few lines, so a common prefix/suffix strip plus
// an LCS over the remainder is exact and cheap.

export type DiffLine = { kind: 'same' | 'add' | 'del'; text: string };
export type DiffRow = DiffLine | { kind: 'skip'; count: number };

export function lineDiff(before: string, after: string): DiffLine[] {
	const a = lines(before);
	const b = lines(after);
	let head = 0;
	while (head < a.length && head < b.length && a[head] === b[head]) head++;
	let tail = 0;
	while (
		tail < a.length - head &&
		tail < b.length - head &&
		a[a.length - 1 - tail] === b[b.length - 1 - tail]
	)
		tail++;
	const out: DiffLine[] = a.slice(0, head).map((text) => ({ kind: 'same', text }));
	out.push(...lcsDiff(a.slice(head, a.length - tail), b.slice(head, b.length - tail)));
	out.push(...a.slice(a.length - tail).map((text): DiffLine => ({ kind: 'same', text })));
	return out;
}

// Unchanged runs longer than 2*context + 1 collapse to one skip row, so a
// one-line edit in a long manifest reads as a hunk, not a wall.
export function hunks(diff: DiffLine[], context = 3): DiffRow[] {
	const out: DiffRow[] = [];
	let i = 0;
	while (i < diff.length) {
		if (diff[i].kind !== 'same') {
			out.push(diff[i++]);
			continue;
		}
		let j = i;
		while (j < diff.length && diff[j].kind === 'same') j++;
		const run = j - i;
		const lead = i === 0 ? 0 : context;
		const trail = j === diff.length ? 0 : context;
		if (run > lead + trail + 1) {
			out.push(...diff.slice(i, i + lead));
			out.push({ kind: 'skip', count: run - lead - trail });
			out.push(...diff.slice(j - trail, j));
		} else {
			out.push(...diff.slice(i, j));
		}
		i = j;
	}
	return out;
}

function lines(s: string): string[] {
	if (s === '') return [];
	return s.replace(/\n$/, '').split('\n');
}

function lcsDiff(a: string[], b: string[]): DiffLine[] {
	const n = a.length;
	const m = b.length;
	if (n === 0) return b.map((text) => ({ kind: 'add', text }));
	if (m === 0) return a.map((text) => ({ kind: 'del', text }));
	// lcs[i][j] = length of the LCS of a[i:] and b[j:].
	const lcs: Uint32Array[] = Array.from({ length: n + 1 }, () => new Uint32Array(m + 1));
	for (let i = n - 1; i >= 0; i--)
		for (let j = m - 1; j >= 0; j--)
			lcs[i][j] = a[i] === b[j] ? lcs[i + 1][j + 1] + 1 : Math.max(lcs[i + 1][j], lcs[i][j + 1]);
	const out: DiffLine[] = [];
	let i = 0;
	let j = 0;
	while (i < n && j < m) {
		if (a[i] === b[j]) (out.push({ kind: 'same', text: a[i++] }), j++);
		else if (lcs[i + 1][j] >= lcs[i][j + 1]) out.push({ kind: 'del', text: a[i++] });
		else out.push({ kind: 'add', text: b[j++] });
	}
	while (i < n) out.push({ kind: 'del', text: a[i++] });
	while (j < m) out.push({ kind: 'add', text: b[j++] });
	return out;
}
