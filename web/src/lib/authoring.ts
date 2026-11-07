import type { VM } from '#lib/api.js';

// Why the caller cannot stage an edit to a VM, or '' when they can: git must
// hold the VM, and the caller must author in its namespace. Every Edit entry
// point reads this one rule, so none offers what staging would refuse.
export const NOT_IN_GIT = 'Not in git: adopt this VM first';
export const READ_ONLY = 'Read-only: you may not edit VMs in this namespace';

export function editBlock(vm: Pick<VM, 'sourceFile'>, canAuthor: boolean): string {
	if (!vm.sourceFile) return NOT_IN_GIT;
	if (!canAuthor) return READ_ONLY;
	return '';
}
