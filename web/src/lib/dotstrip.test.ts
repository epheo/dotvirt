import { describe, expect, it } from 'vitest';
import { densityPath, layoutStrip } from './dotstrip';

const W = 640;
const H = 100;

describe('layoutStrip', () => {
	it('renders a tiny fleet as large dots at their exact percent', () => {
		const l = layoutStrip([34, 71], W, H);
		expect(l.dots).not.toBeNull();
		const dots = l.dots!;
		expect(dots).toHaveLength(2);
		const byI = [...dots].sort((a, b) => a.i - b.i);
		expect(byI[0].x).toBeCloseTo((34 / 100) * W, 5);
		expect(byI[1].x).toBeCloseTo((71 / 100) * W, 5);
		// Radius capped, dots resting on the baseline.
		for (const d of dots) {
			expect(d.r).toBeGreaterThan(2);
			expect(d.y + d.r).toBeLessThanOrEqual(H);
		}
	});

	it('stacks same-bin workers into one column on the bin center', () => {
		const l = layoutStrip([50, 50.5, 51], W, H);
		const dots = l.dots!;
		expect(new Set(dots.map((d) => d.x)).size).toBe(1);
		expect(new Set(dots.map((d) => d.y)).size).toBe(3);
		// Coldest at the bottom of the stack.
		const bottom = dots.reduce((a, b) => (a.y > b.y ? a : b));
		expect(bottom.i).toBe(0);
	});

	it('keeps dots for a large but spread-out fleet', () => {
		const pcts = Array.from({ length: 80 }, (_, i) => (i * 97.3) % 100);
		const l = layoutStrip(pcts, W, H);
		expect(l.dots).not.toBeNull();
		expect(l.dots!).toHaveLength(80);
	});

	it('degrades to density when the tallest stack cannot fit readable dots', () => {
		const pcts = Array.from({ length: 500 }, (_, i) => 48 + (i % 5));
		const l = layoutStrip(pcts, W, H);
		expect(l.dots).toBeNull();
		expect(l.bins.reduce((a, b) => a + b, 0)).toBe(500);
	});

	it('clamps out-of-range percents into the edge bins', () => {
		const l = layoutStrip([-5, 105], W, H);
		expect(l.bins[0]).toBe(1);
		expect(l.bins[l.bins.length - 1]).toBe(1);
		for (const d of l.dots!) {
			expect(d.x).toBeGreaterThanOrEqual(0);
			expect(d.x).toBeLessThanOrEqual(W);
		}
	});
});

describe('densityPath', () => {
	it('closes an area from and to the baseline spanning the full width', () => {
		const bins = new Array(50).fill(0);
		bins[24] = 10;
		const d = densityPath(bins, W, 60, 80);
		expect(d.startsWith('M 0 80')).toBe(true);
		expect(d.endsWith(`L ${W} 80 Z`)).toBe(true);
		// The peak bin reaches the full silhouette height.
		expect(d).toContain(' 20');
	});
});
