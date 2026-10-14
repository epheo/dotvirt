// rowList owns the editable-row idiom (firewall rules, extra disks): a list
// seeded with one blank row (or the rows an edit starts from), immutable
// add/remove (rows is reassigned so $derived readers see the change). Callers
// keep the length-1 floor via their disabled gate; empty starts a list that
// may hold no rows at all. blank gets the current length, for numbered names.
interface RowList<T> {
	readonly rows: T[];
	add(): void;
	remove(i: number): void;
}

export function rowList<T>(
	blank: (n: number) => T,
	seed?: T[],
	opts: { empty?: boolean } = {},
): RowList<T> {
	let rows = $state<T[]>(seed?.length ? seed : opts.empty ? [] : [blank(0)]);
	return {
		get rows() {
			return rows;
		},
		add() {
			rows = [...rows, blank(rows.length)];
		},
		remove(i: number) {
			rows = rows.filter((_, j) => j !== i);
		},
	};
}
