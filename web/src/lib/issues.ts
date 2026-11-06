import { vmKey, type Alert, type Inventory, type ObjectHealth, type VM } from '#lib/api.js';
import { opStands, repoError } from '#lib/gitops.js';
import { hrefForScope, vmHref } from '#lib/nav.js';
import type { AppModal } from '#lib/state/ui.svelte.js';

// What fixes an issue, one click from wherever it is listed: a view to open, a
// dialog to start, or the adopt verb.
export type IssueAction =
	| { label: string; href: string }
	| { label: string; modal: AppModal }
	| { label: string; adopt: { namespace: string; name: string } };

export type Issue = {
	// info marks a suggestion: worth doing, never counted as a problem.
	severity: 'danger' | 'warn' | 'info';
	// Display identity: the project, "namespace/vm", or an object's name.
	scope: string;
	label: string;
	detail?: string;
	href: string;
	project: string;
	action: IssueAction;
};

// The platform tier (tenancy, uplinks, shared segments, cluster policies) has no
// project row; its Application's rollup rides the frame under this name.
const PLATFORM = 'platform';

// The error family keeps growing, so pattern-match like phaseTone does.
const badPhase = /Err|CrashLoop|Unschedulable|Failed/;

// The issues plane: standing problems from the live stream plus firing cluster
// alerts, one list behind the header count, the tree badges and the Summary
// card. canRepair is the namespace-create authority the repo dialogs need.
export function deriveIssues(
	inv: Inventory | null,
	opts: { alerts?: Alert[]; canRepair?: boolean } = {},
): Issue[] {
	if (!inv) return [];
	const out: Issue[] = [];
	for (const p of inv.projects) {
		const phref = hrefForScope({ kind: 'project', project: p.name });
		// A resolver error or a repo GitOps cannot compare against; the same
		// dialog attaches a missing repo or re-creates a lost one.
		const repoProblem = p.error || (p.repo ? repoError(p.gitOps) : '');
		if (repoProblem)
			out.push({
				severity: 'warn',
				scope: p.name,
				label: 'Repository problem',
				detail: repoProblem,
				href: phref,
				project: p.name,
				action: opts.canRepair
					? {
							label: p.repo ? 'Recover repo' : 'Attach repo',
							modal: {
								kind: 'adoptProject',
								project: p.name,
								namespaces: p.namespaces.map((n) => n.namespace),
								recover: !!p.repo,
							},
						}
					: open(phref),
			});
		if (opStands(p.gitOps))
			out.push({
				severity: 'danger',
				scope: p.name,
				label: 'Sync failed',
				detail: p.gitOps?.syncError,
				href: phref,
				project: p.name,
				action: open(phref),
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
				action: open('/changes'),
			});
		for (const o of g.unhealthy ?? []) {
			const issue = objectIssue(inv, PLATFORM, o);
			if (issue) out.push(issue);
		}
	}
	for (const a of opts.alerts ?? []) {
		const issue = alertIssue(inv, a);
		if (issue) out.push(issue);
	}
	return out.sort(bySeverity);
}

// Worth doing, never wrong: running VMs git does not declare yet. Kept out of
// the issue count so a lit count always means something is broken. canStage
// gates the per-VM adopt the way the stage routes do; the adoptable namespaces
// only reach callers allowed to adopt them.
export function deriveSuggestions(
	inv: Inventory | null,
	canStage: (namespace: string) => boolean,
): Issue[] {
	if (!inv) return [];
	const out: Issue[] = [];
	for (const c of inv.adoptable ?? [])
		out.push({
			severity: 'info',
			scope: c.namespace,
			label: `${c.vms} VM${c.vms === 1 ? '' : 's'} outside any project`,
			href: '/compute',
			project: '',
			action: { label: 'Adopt', modal: { kind: 'newProject', adopt: c.namespace } },
		});
	for (const p of inv.projects)
		for (const ns of p.namespaces) {
			if (!canStage(ns.namespace)) continue;
			for (const vm of ns.vms)
				if (vm.sync === 'NotTracked')
					out.push({
						severity: 'info',
						scope: vmKey(vm),
						label: 'running, not in git',
						href: vmHref(vm.namespace, vm.name),
						project: p.name,
						action: { label: 'Adopt into git', adopt: { namespace: vm.namespace, name: vm.name } },
					});
		}
	return out.sort(bySeverity);
}

const RANK = { danger: 0, warn: 1, info: 2 };
function bySeverity(a: Issue, b: Issue): number {
	return RANK[a.severity] - RANK[b.severity] || a.scope.localeCompare(b.scope);
}

// The default next step: open the view that shows the problem, named for it.
function open(href: string): IssueAction {
	const label = href.startsWith('/vm/')
		? 'Open VM'
		: href.startsWith('/networking')
			? 'Open networking'
			: href.startsWith('/changes')
				? 'Open changes'
				: href === '/compute'
					? 'Open inventory'
					: 'Open project';
	return { label, href };
}

// One row per object Argo reports degraded or refused, linking to the view that
// shows it. VMs are left to vmIssue, which already folds their reasons.
function objectIssue(inv: Inventory, project: string, o: ObjectHealth): Issue | null {
	if (o.kind === 'VirtualMachine') return null;
	// A refused apply carries the apply error and no health of its own.
	const refused = !o.health;
	const href = objectHref(inv, o);
	return {
		severity: refused || o.health === 'Missing' ? 'danger' : 'warn',
		scope: o.namespace ? `${o.namespace}/${o.name}` : o.name,
		label: `${o.kind} ${refused ? 'apply failed' : o.health!.toLowerCase()}`,
		detail: o.message,
		href,
		project,
		action: open(href),
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
	const href = vmHref(vm.namespace, vm.name);
	return {
		severity: danger.length ? 'danger' : 'warn',
		scope: vmKey(vm),
		label: [...danger, ...warn].join(', '),
		detail,
		href,
		project,
		action: open(href),
	};
}

// A firing alert joins the plane at its own severity; informational alerts
// stay in Prometheus. It lands on the Monitor tab of what it names.
function alertIssue(inv: Inventory, a: Alert): Issue | null {
	const severity = a.severity === 'critical' ? 'danger' : a.severity === 'warning' ? 'warn' : null;
	if (!severity) return null;
	const ns = a.namespace ?? '';
	const project = (ns && projectOf(inv, ns)) || '';
	const href =
		a.vm && ns
			? vmHref(ns, a.vm, 'monitor')
			: project
				? `${hrefForScope({ kind: 'namespace', project, namespace: ns })}?tab=monitor`
				: '/compute?tab=monitor';
	return {
		severity,
		scope: a.vm && ns ? `${ns}/${a.vm}` : ns || 'cluster',
		label: alertLabel(a.name),
		detail: `Cluster alert ${a.name}${(a.count ?? 0) > 1 ? `, firing ${a.count} times` : ''}`,
		href,
		project,
		action: { label: 'Open monitor', href },
	};
}

// "KubeVirtVMIExcessiveMigrations" reads as "VMI excessive migrations";
// acronyms (two capitals or more) keep their case.
export function alertLabel(name: string): string {
	return name
		.replace(/^KubeVirt/, '')
		.replace(/([a-z0-9])([A-Z])/g, '$1 $2')
		.replace(/([A-Z]+)([A-Z][a-z]{2,})/g, '$1 $2')
		.split(' ')
		.map((w, i) => (i === 0 || /[A-Z].*[A-Z]/.test(w) ? w : w.toLowerCase()))
		.join(' ');
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
		// namespaces; VM and namespace issues narrow to the focused namespace.
		if (scope.namespace)
			return (
				i.scope === i.project ||
				i.scope === scope.namespace ||
				i.scope.startsWith(scope.namespace + '/')
			);
		return true;
	});
}

// Per-project counts for the tree rollup.
export function issueCountByProject(issues: Issue[]): Map<string, number> {
	const m = new Map<string, number>();
	for (const i of issues) m.set(i.project, (m.get(i.project) ?? 0) + 1);
	return m;
}
