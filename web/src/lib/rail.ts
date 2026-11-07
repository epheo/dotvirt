// The rail trees at fleet scale: a few thousand VMs never become a few
// thousand rows. The biggest groups show and the rest fold into "N more"; an
// open container lists its first VMs, then "N more" that opens its VMs tab.

export const TOP_GROUPS = 5;
export const LEAF_LIMIT = 10;
// At or below this many VMs nothing folds and every container opens by
// default: a small cluster reads whole, as it always has.
export const SMALL_FLEET = 100;

const collator = new Intl.Collator('en', { numeric: true });

// Most VMs first; ties read in name order, host-2 before host-10.
export function bySize<T>(
	items: readonly T[],
	size: (t: T) => number,
	name: (t: T) => string,
): T[] {
	return [...items].sort((a, b) => size(b) - size(a) || collator.compare(name(a), name(b)));
}

export function byName<T>(items: readonly T[], name: (t: T) => string): T[] {
	return [...items].sort((a, b) => collator.compare(name(a), name(b)));
}

// fold keeps the first `top` ranked items and folds the rest, except the one
// in view, which always shows. Folding a single item would save nothing, so
// up to top + 1 items all show.
export function fold<T>(
	ranked: readonly T[],
	top: number,
	keep: (t: T) => boolean = () => false,
): { shown: T[]; folded: T[] } {
	if (ranked.length <= top + 1) return { shown: [...ranked], folded: [] };
	const shown: T[] = [];
	const folded: T[] = [];
	ranked.forEach((t, i) => (i < top || keep(t) ? shown : folded).push(t));
	return { shown, folded };
}

// leafWindow lists a container's first `limit` children, plus the one in view
// when it sits further down, and counts the rest.
export function leafWindow<T>(
	items: readonly T[],
	limit: number,
	active: (t: T) => boolean = () => false,
): { shown: T[]; more: number } {
	if (items.length <= limit) return { shown: [...items], more: 0 };
	const shown = items.slice(0, limit);
	const extra = items.slice(limit).find(active);
	if (extra) shown.push(extra);
	return { shown, more: items.length - shown.length };
}

// matches is the rail filter: a case-blind substring over any of the names.
export function matches(query: string, ...names: string[]): boolean {
	const q = query.trim().toLowerCase();
	return !q || names.some((n) => n.toLowerCase().includes(q));
}
