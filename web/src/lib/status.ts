// The one status vocabulary: five tones, two shapes (StatusDot / StatusPill),
// and the domain mappers that translate VM power, VMI phase, alert severity,
// and task-dock rows onto them. Class maps are literal records — Tailwind only
// sees static strings.
import type { Power, ProjectSync } from '$lib/api';

export type Tone = 'ok' | 'warn' | 'danger' | 'info' | 'neutral';

export const TONE_DOT: Record<Tone, string> = {
	ok: 'bg-ok',
	warn: 'bg-warn',
	danger: 'bg-danger',
	info: 'bg-accent',
	neutral: 'bg-ink-faint',
};

export const TONE_PILL: Record<Tone, string> = {
	ok: 'bg-ok-soft text-ok-ink',
	warn: 'bg-warn-soft text-warn-ink',
	danger: 'bg-danger-soft text-danger-ink',
	info: 'bg-accent-soft text-accent-ink',
	neutral: 'bg-inset-strong text-ink-muted',
};

export const TONE_TEXT: Record<Tone, string> = {
	ok: 'text-ok-ink',
	warn: 'text-warn-ink',
	danger: 'text-danger-ink',
	info: 'text-accent-ink',
	neutral: 'text-ink-soft',
};

// A paused VMI stays phase Running, so call it out (warn) rather than ok.
export function powerTone(power: Power, paused = false): Tone {
	if (paused) return 'warn';
	return power === 'On' ? 'ok' : power === 'Off' ? 'neutral' : 'warn';
}

// KubeVirt printable status → tone. Pattern-matched rather than enumerated:
// the error family keeps growing, transitional states all read as activity.
export function phaseTone(phase?: string, paused = false): Tone {
	if (paused) return 'warn';
	if (!phase || phase === 'Stopped') return 'neutral';
	if (phase === 'Running') return 'ok';
	if (/Err|CrashLoop|Unschedulable/.test(phase)) return 'danger';
	if (phase === 'Unknown') return 'warn';
	return 'info'; // Provisioning, Starting, Stopping, Migrating, WaitingFor…
}

// Task-dock rows: anything still moving is info (hosts pulse the dot),
// success lands ok, failures danger, standing drift warn.
export function taskTone(t: { kind: string; ok?: boolean; active?: boolean }): Tone {
	switch (t.kind) {
		case 'drift':
			return 'warn';
		case 'migration':
			return t.active ? 'info' : t.ok ? 'ok' : 'danger';
		case 'sync':
			return t.active ? 'info' : 'ok';
		case 'action':
			return t.ok ? 'ok' : 'danger';
		case 'pr':
			return 'ok';
		default:
			return 'info'; // staged
	}
}

export function severityTone(severity?: string): Tone {
	return severity === 'critical' ? 'danger' : severity === 'warning' ? 'warn' : 'neutral';
}

// A project's ArgoCD rollup → one tone + label, most-alarming state first. Returns
// null for a clean (Synced + Healthy) or not-yet-known project, so a dense tree shows a
// badge only when something needs attention or is in flight — green stays implicit.
// pulse marks the actively-applying state (the "pending apply" feedback a merged
// segment/policy PR produces before it settles).
export function projectSyncView(
	g?: ProjectSync,
): { tone: Tone; label: string; pulse: boolean } | null {
	if (!g) return null;
	const op = g.operation;
	if (op === 'Failed' || op === 'Error')
		return { tone: 'danger', label: 'Sync failed', pulse: false };
	if (g.health === 'Degraded' || g.health === 'Missing')
		return { tone: 'danger', label: g.health, pulse: false };
	if (op === 'Running' || g.health === 'Progressing')
		return { tone: 'info', label: 'Applying…', pulse: true };
	if (g.sync === 'OutOfSync') return { tone: 'warn', label: 'Out of sync', pulse: false };
	return null; // Synced + Healthy, or unknown — no badge
}
