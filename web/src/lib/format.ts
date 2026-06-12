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

// fmtUsage formats a value by a unit hint used across the usage widgets.
export function fmtUsage(unit: 'pct' | 'bytes' | 'cores', v: number): string {
	if (unit === 'pct') return v.toFixed(1) + '%';
	if (unit === 'cores') return cores(v);
	return bytes(v);
}
