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

// quantityBytes parses a Kubernetes memory quantity ("2Gi", "512Mi", "4G") into
// bytes. NaN for shapes it cannot read, so callers skip a comparison rather
// than mis-refuse on it.
export function quantityBytes(q: string): number {
	const m = /^([0-9]+(?:\.[0-9]+)?)(Ki|Mi|Gi|Ti|Pi|k|M|G|T|P)?$/.exec(q.trim());
	if (!m) return NaN;
	const bin: Record<string, number> = {
		Ki: 2 ** 10,
		Mi: 2 ** 20,
		Gi: 2 ** 30,
		Ti: 2 ** 40,
		Pi: 2 ** 50,
	};
	const dec: Record<string, number> = { k: 1e3, M: 1e6, G: 1e9, T: 1e12, P: 1e15 };
	const s = m[2] ?? '';
	return Number(m[1]) * (bin[s] ?? dec[s] ?? 1);
}

// compactSpan renders elapsed seconds at two units of precision ("3d 21h",
// "5m"); sub-minute stays in seconds - events can be seconds old.
function compactSpan(s: number): string {
	const d = Math.floor(s / 86400);
	const h = Math.floor((s % 86400) / 3600);
	const m = Math.floor((s % 3600) / 60);
	if (d > 0) return `${d}d ${h}h`;
	if (h > 0) return `${h}h ${m}m`;
	if (m > 0) return `${m}m`;
	return `${s}s`;
}

// duration renders a compact elapsed time from an ISO timestamp (no "ago"
// suffix - uptimes, event ages).
export function duration(iso: string | undefined): string {
	if (!iso) return '';
	const start = new Date(iso).getTime();
	if (Number.isNaN(start)) return '';
	return compactSpan(Math.max(0, Math.floor((Date.now() - start) / 1000)));
}

// relativeAge renders a compact "X ago" from an ISO timestamp or unix seconds.
export function relativeAge(t: string | number | undefined): string {
	if (t == null || t === '') return '';
	const ms = typeof t === 'number' ? t * 1000 : new Date(t).getTime();
	if (Number.isNaN(ms)) return '';
	return compactSpan(Math.max(0, Math.floor((Date.now() - ms) / 1000))) + ' ago';
}

// fmtUsage formats a value by a unit hint used across the usage widgets.
export function fmtUsage(unit: 'pct' | 'bytes' | 'cores', v: number): string {
	if (unit === 'pct') return v.toFixed(1) + '%';
	if (unit === 'cores') return cores(v);
	return bytes(v);
}

// Thrown errors stringify as "Error: <msg>"; toasts show just the message.
export function friendlyError(e: unknown): string {
	return (e instanceof Error ? e.message : String(e)).replace(/^Error:\s*/, '');
}
