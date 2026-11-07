// Paging for growable tables: a list that can reach thousands shows one page
// at a time and says where the page sits ("Showing 1-50 of 2,846"). Pages are
// 1-based, as people count them.

export const PAGE_SIZES = [25, 50, 100] as const;
export const DEFAULT_PAGE_SIZE = 50;

export type PageSlice = {
	page: number; // clamped into 1..pages
	pages: number; // at least 1, so an empty list still reads "Page 1 of 1"
	start: number; // index of the first row on the page
	end: number; // index past the last row on the page
	total: number;
};

// pageSlice clamps the page, so a list that shrinks under a live update never
// strands the view on an empty page past the end.
export function pageSlice(total: number, page: number, size: number): PageSlice {
	const per = Math.max(1, Math.floor(size) || DEFAULT_PAGE_SIZE);
	const pages = Math.max(1, Math.ceil(total / per));
	const p = Math.min(Math.max(1, Math.floor(page) || 1), pages);
	const start = (p - 1) * per;
	return { page: p, pages, start, end: Math.min(start + per, total), total };
}

// pageOf is the page that holds row index; a missing row (-1) means page 1.
export function pageOf(index: number, size: number): number {
	return index < 0 ? 1 : Math.floor(index / Math.max(1, size)) + 1;
}

// pageFor keeps a row reachable: the page holding key, else page 1. Used when
// the filter or sort changes, and when a row is picked from outside the table.
export function pageFor(keys: readonly string[], key: string | null, size: number): number {
	return key ? pageOf(keys.indexOf(key), size) : 1;
}

const num = (n: number) => n.toLocaleString('en-US');

// The range a page shows, "1-50"; "0" for an empty list.
export function rangeLabel(s: PageSlice): string {
	return s.total === 0 ? '0' : `${num(s.start + 1)}-${num(s.end)}`;
}

export function totalLabel(s: PageSlice): string {
	return num(s.total);
}
