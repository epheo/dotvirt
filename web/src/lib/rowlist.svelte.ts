// rowList owns the firewall dialogs' editable-rule idiom: a list seeded with
// one blank row, immutable add/remove (rows is reassigned so $derived readers
// see the change). Callers keep the length-1 floor via their disabled gate.
export interface RowList<T> {
	readonly rows: T[];
	add(): void;
	remove(i: number): void;
}

export function rowList<T>(blank: () => T): RowList<T> {
	let rows = $state<T[]>([blank()]);
	return {
		get rows() {
			return rows;
		},
		add() {
			rows = [...rows, blank()];
		},
		remove(i: number) {
			rows = rows.filter((_, j) => j !== i);
		},
	};
}
