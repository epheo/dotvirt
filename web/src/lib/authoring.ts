import type { VM } from '#lib/api.js';

// A namespace's authoring verdict as the frame carries it: the server's yes,
// its no, or unknown when its review failed this frame. Both closed verdicts
// offer nothing, since the stage routes enforce the same check; they differ
// only in what they tell the person: wait, or ask for access.
export type Authoring = 'yes' | 'no' | 'unknown';

export const NOT_IN_GIT = 'Not in git: adopt this VM first';
export const READ_ONLY = 'Read-only: you may not edit VMs in this namespace';
export const ACCESS_UNKNOWN = 'Your access could not be checked. Try again in a moment.';

// Why the caller cannot change things in a namespace, or '' when they can.
export function authorBlock(a: Authoring): string {
	return a === 'yes' ? '' : a === 'unknown' ? ACCESS_UNKNOWN : READ_ONLY;
}

// Why the caller cannot stage an edit to a VM, or '' when they can: git must
// hold the VM, and the caller must author in its namespace. Every Edit entry
// point reads this one rule, so none offers what staging would refuse.
export function editBlock(vm: Pick<VM, 'sourceFile'>, a: Authoring): string {
	if (!vm.sourceFile) return NOT_IN_GIT;
	return authorBlock(a);
}
