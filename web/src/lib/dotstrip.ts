// Layout for the host-balance strips: every worker is a dot on a 0-100% axis,
// stacked into fine bins; when the tallest stack cannot fit the strip height
// at a readable radius, the strip degrades to a density silhouette and only
// outliers keep their dots. The switch is geometric, never a node-count
// threshold — a spread-out fleet keeps its dots longer than a piled-up one.

export interface Dot {
	i: number; // index into the input array
	x: number;
	y: number;
	r: number;
}

export interface StripLayout {
	dots: Dot[] | null; // null: density mode — draw the silhouette instead
	bins: number[]; // per-bin counts (density path input; always filled)
	binPct: number;
}

export const BIN_PCT = 2;
const R_MAX = 7;
const R_MIN = 2.4;

const clamp = (p: number) => Math.min(100, Math.max(0, p));

// Values land in [0,100]; the last bin absorbs the closed upper edge.
const binOf = (p: number, bins: number) => Math.min(bins - 1, Math.floor(clamp(p) / BIN_PCT));

export function layoutStrip(pcts: number[], w: number, h: number): StripLayout {
	const nBins = Math.round(100 / BIN_PCT);
	const bins = new Array<number>(nBins).fill(0);
	const members: number[][] = Array.from({ length: nBins }, () => []);
	pcts.forEach((p, i) => {
		const b = binOf(p, nBins);
		bins[b]++;
		members[b].push(i);
	});
	const maxStack = Math.max(1, ...bins);
	const binW = w / nBins;
	const r = Math.min(R_MAX, h / (2 * maxStack), binW / 2 - 0.5);
	if (r < R_MIN) return { dots: null, bins, binPct: BIN_PCT };

	const dots: Dot[] = [];
	for (let b = 0; b < nBins; b++) {
		// Coldest at the bottom of each stack, so color bands read continuously.
		const stack = [...members[b]].sort((i, j) => pcts[i] - pcts[j]);
		for (let k = 0; k < stack.length; k++) {
			// A lone dot sits at its exact percent; stacked dots align on the
			// bin center so the column reads as one column.
			const x = stack.length === 1 ? (clamp(pcts[stack[k]]) / 100) * w : (b + 0.5) * binW;
			dots.push({ i: stack[k], x, y: h - r - k * 2 * r, r });
		}
	}
	return { dots, bins, binPct: BIN_PCT };
}

// The silhouette: a smoothed area over the bin counts, closed to the baseline.
// Quadratic segments through bin-center midpoints — enough smoothing to read
// as a distribution, cheap enough to rebuild every poll.
export function densityPath(bins: number[], w: number, h: number, baseY: number): string {
	const max = Math.max(1, ...bins);
	const binW = w / bins.length;
	const pts = bins.map((n, b) => ({
		x: (b + 0.5) * binW,
		y: baseY - (n / max) * h,
	}));
	let d = `M 0 ${baseY}`;
	d += ` L ${pts[0].x.toFixed(1)} ${pts[0].y.toFixed(1)}`;
	for (let i = 1; i < pts.length; i++) {
		const mx = (pts[i - 1].x + pts[i].x) / 2;
		const my = (pts[i - 1].y + pts[i].y) / 2;
		d += ` Q ${pts[i - 1].x.toFixed(1)} ${pts[i - 1].y.toFixed(1)} ${mx.toFixed(1)} ${my.toFixed(1)}`;
	}
	const last = pts[pts.length - 1];
	d += ` L ${last.x.toFixed(1)} ${last.y.toFixed(1)} L ${w} ${baseY} Z`;
	return d;
}
