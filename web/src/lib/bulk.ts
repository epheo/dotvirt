import { vmKey } from './api';
import { friendlyError } from './format';

// bulkOutcome tallies a per-VM staging batch. results align with actionable.
// Failed VMs stay picked so a retry targets exactly them; the first error is
// named because a bare count leaves the admin nothing to fix. held, given,
// says whether a stage's answer still holds its VM: staging what git already
// holds unstages it, and that is not a staged change.
export function bulkOutcome(
	verb: string,
	total: number,
	actionable: { namespace: string; name: string }[],
	results: PromiseSettledResult<unknown>[],
	held?: (value: unknown, i: number) => boolean,
): { keep: Set<string>; staged: number; failed: number; msg: string } {
	const keep = new Set<string>();
	let firstError = '';
	results.forEach((r, i) => {
		if (r.status === 'fulfilled') return;
		keep.add(vmKey(actionable[i]));
		firstError ||= `${actionable[i].name}: ${friendlyError(r.reason)}`;
	});
	const failed = keep.size;
	const matched = held
		? results.filter((r, i) => r.status === 'fulfilled' && !held(r.value, i)).length
		: 0;
	const staged = results.length - failed - matched;
	const skipped = total - actionable.length;
	const extra = [
		skipped ? `${skipped} skipped` : '',
		matched ? `${matched} already ${matched === 1 ? 'matches' : 'match'} git` : '',
		failed ? `${failed} failed` : '',
	]
		.filter(Boolean)
		.join(', ');
	let msg = `${verb} ${staged} of ${total}${extra ? ` (${extra})` : ''}.`;
	if (firstError) msg += ` ${firstError}`;
	return { keep, staged, failed, msg };
}
