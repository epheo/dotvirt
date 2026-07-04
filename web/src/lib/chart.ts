// The chart palette lives in app.css as --chart-* custom properties — one
// source of truth for SVG and canvas, and the surface a dark theme overrides.
// SVG components consume 'var(--chart-N)' strings directly in inline styles;
// canvas (uPlot) cannot, so it resolves the current values here at draw time.
const SERIES = ['--chart-1', '--chart-2', '--chart-3', '--chart-4', '--chart-5', '--chart-6'];

function resolve(name: string): string {
	return getComputedStyle(document.documentElement).getPropertyValue(name).trim();
}

// Resolved hex strings, in series order. Hex by construction (app.css authors
// them so) — UPlotChart derives its stacked-fill alpha by appending a byte.
export function chartSeries(): string[] {
	return SERIES.map(resolve);
}

export function chartUI(): { grid: string; axis: string; ticks: string } {
	return {
		grid: resolve('--chart-grid'),
		axis: resolve('--chart-axis'),
		ticks: resolve('--chart-ticks'),
	};
}
