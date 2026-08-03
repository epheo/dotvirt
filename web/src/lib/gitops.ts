import type { ProjectSync } from './api';

// The gitOps rollup carries two error planes in one syncError field: comparison
// errors (unreachable or lost repo - no operation ran) and apply failures (the
// merged change was refused by the cluster). Recovery flows key on the first
// only; offering "Recover repo" on an apply failure invites re-creating a
// healthy repo.
export const opFailed = (g?: ProjectSync | null): boolean =>
	(g?.operation === 'Failed' || g?.operation === 'Error') && !!g?.syncError;

// The comparison-plane error, or '' when there is none (including when the
// error is an apply failure).
export const repoError = (g?: ProjectSync | null): string =>
	g?.syncError && !opFailed(g) ? g.syncError : '';
