import type { ProjectSync } from './api';

// The gitOps rollup carries two error planes in one syncError field: comparison
// errors (unreachable or lost repo - no operation ran) and apply failures (the
// merged change was refused by the cluster). Recovery flows key on the first
// only; offering "Recover repo" on an apply failure invites re-creating a
// healthy repo.
// A Failed/Error operation that still stands. Once the app converges (Synced),
// Argo never replaces the operation record - no new operation runs while live
// matches git - so the phase alone is history, not a problem. EVERY consumer of
// the operation phase must go through this gate.
export const opStands = (g?: ProjectSync | null): boolean =>
	(g?.operation === 'Failed' || g?.operation === 'Error') && g?.sync !== 'Synced';

export const opFailed = (g?: ProjectSync | null): boolean => opStands(g) && !!g?.syncError;

// The comparison-plane error, or '' when there is none (including when the
// error is an apply failure).
export const repoError = (g?: ProjectSync | null): string =>
	g?.syncError && !opFailed(g) ? g.syncError : '';
