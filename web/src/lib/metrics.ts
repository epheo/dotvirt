import type { MetricsReason } from './model.gen.js';

// Every reason but partial carries no numbers: its zeros are not zeros, so a
// view shows this line in their place.
const GAP: Record<string, string> = {
	forbidden: 'Metrics are not available: the metrics service refused your account.',
	unreachable:
		'Metrics are not available: the metrics service is not answering. dotvirt asks again shortly.',
	unconfigured: 'Metrics are not available: no metrics service is set up for your account.',
	scope: 'Too many namespaces to read at once. Pick a project to see its metrics.',
};

/** The line a view shows instead of its numbers, or '' when they stand. */
export function metricsGap(reason?: MetricsReason): string {
	if (!reason || reason === 'partial') return '';
	return GAP[reason] ?? 'Metrics are not available.';
}

export const PARTIAL_NOTE =
	'Incomplete: part of the metrics read failed, so these numbers leave something out.';
