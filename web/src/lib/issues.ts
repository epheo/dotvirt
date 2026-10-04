import { vmKey, type Inventory, type ObjectHealth, type VM } from '$lib/api';
import { opStands, repoError } from '$lib/gitops';
import { hrefForScope, vmHref } from '$lib/nav';

type Issue = {
	severity: 'danger' | 'warn';
	// Display identity: the project, "namespace/vm", or an object's name.
	scope: string;
	label: string;
	detail?: string;
	href: string;
	project: string;
};

// The platform tier (tenancy, uplinks, shared segments, cluster policies) has no
// project row; its Application's rollup rides the frame under this name.
const PLATFORM = 'platform';

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
		if (opStands(p.gitOps))
			out.push({
				severity: 'danger',
				scope: p.name,
				label: 'Sync failed',
				detail: p.gitOps?.syncError,
				href: phref,
				project: p.name,
			});
		for (const o of p.gitOps?.unhealthy ?? []) {
			const issue = objectIssue(inv, p.name, o);
			if (issue) out.push(issue);
		}
		for (const ns of p.namespaces)
			for (const vm of ns.vms) {
				const issue = vmIssue(p.name, vm);
				if (issue) out.push(issue);
			}
	}
	// A refused apply or an unreachable repo on the platform tier stands like a
	// project's; the Changes lane is where the merged PR behind it lives.
	const g = inv.platform;
	if (g) {
		if (opStands(g) || repoError(g))
			out.push({
				severity: 'danger',
				scope: PLATFORM,
				label: opStands(g) ? 'Sync failed' : 'Repository problem',
				detail: g.syncError,
				href: '/changes',
				project: PLATFORM,
			});
		for (const o of g.unhealthy ?? []) {
			const issue = objectIssue(inv, PLATFORM, o);
			if (issue) out.push(issue);
		}
	}
	return out.sort((a, b) =>
		a.severity === b.severity ? a.scope.localeCompare(b.scope) : a.severity === 'danger' ? -1 : 1,
	);
}

// One row per object Argo reports degraded or refused, linking to the view that
// shows it. VMs are left to vmIssue, which already folds their reasons.
function objectIssue(inv: Inventory, project: string, o: ObjectHealth): Issue | null {
	if (o.kind === 'VirtualMachine') return null;
	// A refused apply carries the apply error and no health of its own.
	const refused = !o.health;
	return {
		severity: refused || o.health === 'Missing' ? 'danger' : 'warn',
		scope: o.namespace ? `${o.namespace}/${o.name}` : o.name,
		label: `${o.kind} ${refused ? 'apply failed' : o.health!.toLowerCase()}`,
		detail: o.message,
		href: objectHref(inv, o),
		project,
	};
}

// Where each kind is shown: segments have a route of their own (a NAD by its
// attach ref), uplinks sit on the networking root and policies on its security
// tab, tenancy objects on the project that owns the namespace.
function objectHref(inv: Inventory, o: ObjectHealth): string {
	switch (o.kind) {
		case 'UserDefinedNetwork':
		case 'ClusterUserDefinedNetwork':
			return hrefForScope({ kind: 'network', network: o.name });
		case 'NetworkAttachmentDefinition':
			return hrefForScope({ kind: 'network', network: `${o.namespace}/${o.name}` });
		case 'NodeNetworkConfigurationPolicy':
			return '/networking';
		case 'NetworkPolicy':
		case 'EgressFirewall':
		case 'AdminNetworkPolicy':
		case 'BaselineAdminNetworkPolicy':
		case 'EgressIP':
		case 'AdminPolicyBasedExternalRoute':
			return '/networking?tab=security';
		case 'Namespace': {
			const p = projectOf(inv, o.name);
			return p ? hrefForScope({ kind: 'namespace', project: p, namespace: o.name }) : '/compute';
		}
		case 'RoleBinding': {
			const p = projectOf(inv, o.namespace ?? '');
			return p ? `${hrefForScope({ kind: 'project', project: p })}?tab=permissions` : '/compute';
		}
		default:
			return '/changes';
	}
}

function projectOf(inv: Inventory, namespace: string): string | undefined {
	return inv.projects.find((p) => p.namespaces.some((n) => n.namespace === namespace))?.name;
}

// One row per VM, highest severity, every reason folded into the label - a
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
		scope: vmKey(vm),
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
