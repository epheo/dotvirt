// Humanizers shared by the capacity/usage widgets.

export function bytes(v: number): string {
	const u = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
	let i = 0;
	while (v >= 1024 && i < u.length - 1) {
		v /= 1024;
		i++;
	}
	return v.toFixed(i === 0 ? 0 : 1) + ' ' + u[i];
}

export function cores(v: number): string {
	return v < 10 ? v.toFixed(2) : v.toFixed(1);
}

// relativeAge renders a compact "X ago" from an ISO timestamp or unix seconds.
export function relativeAge(t: string | number | undefined): string {
	if (t == null || t === '') return '';
	const ms = typeof t === 'number' ? t * 1000 : new Date(t).getTime();
	if (Number.isNaN(ms)) return '';
	const s = Math.max(0, Math.floor((Date.now() - ms) / 1000));
	const d = Math.floor(s / 86400);
	const h = Math.floor((s % 86400) / 3600);
	const m = Math.floor((s % 3600) / 60);
	if (d > 0) return `${d}d ${h}h ago`;
	if (h > 0) return `${h}h ${m}m ago`;
	if (m > 0) return `${m}m ago`;
	return `${s}s ago`;
}

// fmtUsage formats a value by a unit hint used across the usage widgets.
export function fmtUsage(unit: 'pct' | 'bytes' | 'cores', v: number): string {
	if (unit === 'pct') return v.toFixed(1) + '%';
	if (unit === 'cores') return cores(v);
	return bytes(v);
}
