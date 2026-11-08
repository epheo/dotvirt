import {
	vmKey,
	type Alert,
	type Inventory,
	type ObjectHealth,
	type ObjectRef as GitObject,
	type Project,
	type VM,
} from '#lib/api.js';
import { opStands, repoError } from '#lib/gitops.js';
import type { NetProblem } from '#lib/netmap.js';
import { changesHref, hrefForScope, networkHref, vmHref } from '#lib/nav.js';
import type { ObjectRef } from '#lib/objects.js';
import type { AppModal } from '#lib/state/ui.svelte.js';

// What fixes an issue, one click from wherever it is listed: a view to open, a
// dialog to start, the adopt verb, or an object's git file to edit.
export type IssueAction =
	| { label: string; href: string }
	| { label: string; modal: AppModal }
	| { label: string; adopt: { namespace: string; name: string } }
	| { label: string; edit: ObjectRef };

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
	opts: { alerts?: Alert[]; canRepair?: boolean; network?: NetProblem[] } = {},
): Issue[] {
	if (!inv) return [];
	const out: Issue[] = [];
	// A failing uplink reports per host from its own problem; Argo's rollup of
	// the same policy would only repeat it.
	const failingPolicies = new Set((opts.network ?? []).map((p) => p.policy).filter(Boolean));
	const objectIssueOf = (project: string, o: ObjectHealth) =>
		o.kind === 'NodeNetworkConfigurationPolicy' && failingPolicies.has(o.name)
			? null
			: objectIssue(inv, project, o);
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
			const issue = objectIssueOf(p.name, o);
			if (issue) out.push(issue);
		}
		for (const ns of p.namespaces)
			for (const vm of ns.vms) {
				const issue = vmIssue(p.name, vm);
				if (issue) out.push(issue);
			}
		out.push(...placementIssues(inv, p));
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
			const issue = objectIssueOf(PLATFORM, o);
			if (issue) out.push(issue);
		}
	}
	for (const p of opts.network ?? []) out.push(networkIssue(inv, p));
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
// attach ref), uplinks and policies on the networking root's Uplinks and
// Firewall tabs, tenancy objects on the project that owns the namespace.
function objectHref(inv: Inventory, o: ObjectHealth): string {
	switch (o.kind) {
		case 'UserDefinedNetwork':
			return hrefForScope({ kind: 'network', network: `${o.namespace}/${o.name}` });
		case 'ClusterUserDefinedNetwork':
			return hrefForScope({ kind: 'network', network: o.name });
		case 'NetworkAttachmentDefinition':
			return hrefForScope({ kind: 'network', network: `${o.namespace}/${o.name}` });
		case 'NodeNetworkConfigurationPolicy':
			return '/networking?tab=uplinks';
		case 'NetworkPolicy':
		case 'EgressFirewall':
		case 'AdminNetworkPolicy':
		case 'BaselineAdminNetworkPolicy':
		case 'EgressIP':
		case 'AdminPolicyBasedExternalRoute':
			return '/networking?tab=firewall';
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

// A networking problem from the catalog (an uplink failing on hosts, subnets
// that overlap where one VM could attach both), named by its objects.
function networkIssue(inv: Inventory, p: NetProblem): Issue {
	if (p.kind === 'uplink') {
		const href = '/networking?tab=uplinks';
		return {
			severity: p.severity,
			scope: p.uplink ?? '',
			label: p.title.toLowerCase(),
			detail: p.detail,
			href,
			project: PLATFORM,
			action: open(href),
		};
	}
	const keys = p.networks ?? [];
	const href = keys.length ? networkHref(keys[0]) : '/networking?tab=networks';
	const ns = keys[0]?.includes('/') ? keys[0].split('/')[0] : '';
	return {
		severity: p.severity,
		scope: keys.map((k) => k.slice(k.lastIndexOf('/') + 1)).join(' and '),
		label: 'subnets overlap',
		detail: p.detail,
		href,
		project: (ns && projectOf(inv, ns)) || PLATFORM,
		action: { label: 'Open the network', href },
	};
}

// The kinds whose git file dotvirt can open for editing, by its object route.
const EDITABLE: Record<string, string> = {
	UserDefinedNetwork: 'network',
	NetworkPolicy: 'networkpolicy',
	EgressFirewall: 'egressfirewall',
};

// Objects a project's repo declares where its Application cannot keep them:
// without a namespace (Argo applies them to the project's default namespace
// instead), in a namespace outside the project, or of a kind only the
// platform repo may hold (both refuse the project's whole sync).
function placementIssues(inv: Inventory, p: Project): Issue[] {
	const at = (o: GitObject) => (o.namespace ? `${o.namespace}/${o.name}` : o.name);
	const view = (o: GitObject) =>
		objectHref(inv, { kind: o.kind, namespace: o.namespace, name: o.name });
	const fixIn = (o: GitObject, label: string): IssueAction =>
		o.kind === 'VirtualMachine' && o.namespace
			? { label: 'Open the VM', href: vmHref(o.namespace, o.name) }
			: EDITABLE[o.kind] && o.namespace
				? { label, edit: { resource: EDITABLE[o.kind], namespace: o.namespace, name: o.name } }
				: open(view(o));
	const out: Issue[] = [];
	for (const o of p.unnamespaced ?? [])
		out.push({
			severity: 'warn',
			scope: at(o),
			label: `${o.kind} has no namespace in git`,
			detail: `Argo applies it to ${p.name}'s default namespace${o.namespace ? `, not ${o.namespace}` : ''}.`,
			href: view(o),
			project: p.name,
			action: fixIn(o, 'Set its namespace'),
		});
	for (const o of p.outOfProject ?? []) {
		const href = changesHref(p.name);
		out.push({
			severity: 'danger',
			scope: at(o),
			label: `${o.kind} outside ${p.name}`,
			detail: `${o.namespace} is not one of ${p.name}'s namespaces: the project's sync fails until git moves it into one or removes it.`,
			href,
			project: p.name,
			action: { label: "Open the project's changes", href },
		});
	}
	for (const o of p.platformKinds ?? [])
		out.push({
			severity: 'danger',
			scope: at(o),
			label: `${o.kind} only the platform repo may hold`,
			detail: `${p.name}'s sync fails until its repo no longer declares it.`,
			href: view(o),
			project: p.name,
			action:
				o.kind === 'NetworkAttachmentDefinition'
					? { label: 'Open the network', href: view(o) }
					: open(view(o)),
		});
	return out;
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
