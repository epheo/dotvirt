// The issues plane: one derivation from the streamed inventory to "what needs
// attention right now" — vCenter's Issues & Alarms, derive-not-persist. Only
// standing problems qualify; transitional states (applying, progressing,
// plain OutOfSync = a pending apply) are deliberately not issues.
import type { Inventory, VM } from '$lib/api';
import { hrefForScope, vmHref } from '$lib/nav';

export type Issue = {
	severity: 'danger' | 'warn';
	// Display identity: the project, or "namespace/vm".
	scope: string;
	label: string;
	detail?: string;
	href: string;
	project: string;
};

// The error family keeps growing, so pattern-match like phaseTone does.
const badPhase = /Err|CrashLoop|Unschedulable|Failed/;

export function deriveIssues(inv: Inventory | null): Issue[] {
	if (!inv) return [];
	const out: Issue[] = [];
	for (const p of inv.projects) {
		const phref = hrefForScope({ kind: 'project', project: p.name });
		if (p.error)
			out.push({
				severity: 'warn',
				scope: p.name,
				label: 'Repository problem',
				detail: p.error,
				href: phref,
				project: p.name,
			});
		const op = p.gitOps?.operation;
		if (op === 'Failed' || op === 'Error')
			out.push({
				severity: 'danger',
				scope: p.name,
				label: 'Sync failed',
				detail: p.gitOps?.syncError,
				href: phref,
				project: p.name,
			});
		for (const ns of p.namespaces)
			for (const vm of ns.vms) {
				const issue = vmIssue(p.name, vm);
				if (issue) out.push(issue);
			}
	}
	return out.sort((a, b) =>
		a.severity === b.severity ? a.scope.localeCompare(b.scope) : a.severity === 'danger' ? -1 : 1,
	);
}

// One row per VM, highest severity, every reason folded into the label — a
// broken VM should read as one problem, not three.
function vmIssue(project: string, vm: VM): Issue | null {
	const danger: string[] = [];
	const warn: string[] = [];
	let detail: string | undefined;
	if (vm.syncError) {
		danger.push('apply failed');
		detail = vm.syncError;
	}
	if (vm.phase && badPhase.test(vm.phase)) danger.push(vm.phase);
	if (vm.health === 'Degraded') warn.push('degraded');
	if (!danger.length && !warn.length) return null;
	return {
		severity: danger.length ? 'danger' : 'warn',
		scope: `${vm.namespace}/${vm.name}`,
		label: [...danger, ...warn].join(', '),
		detail,
		href: vmHref(vm.namespace, vm.name),
		project,
	};
}

// Scope filter for the summary lane (project or namespace focus).
export function issuesInScope(
	issues: Issue[],
	scope: { project?: string; namespace?: string },
): Issue[] {
	if (!scope.project && !scope.namespace) return issues;
	return issues.filter((i) => {
		if (scope.project && i.project !== scope.project) return false;
		// Project-level issues (scope == project) stay visible inside their
		// namespaces; VM issues narrow to the focused namespace.
		if (scope.namespace) return i.scope === i.project || i.scope.startsWith(scope.namespace + '/');
		return true;
	});
}

// Per-project counts for the tree rollup.
export function issueCountByProject(issues: Issue[]): Map<string, number> {
	const m = new Map<string, number>();
	for (const i of issues) m.set(i.project, (m.get(i.project) ?? 0) + 1);
	return m;
}
