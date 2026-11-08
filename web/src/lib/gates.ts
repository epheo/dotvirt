// Every gated control's verdict, one function per action, each reading the
// capability field its server gate asks (GET /api/capabilities), so labels and
// reasons are said one way everywhere. A gate offers the control or names in
// one line why not: still checking, the check failed, or the server's no.
import type { ClusterCaps, NamespaceCaps, ObjectCaps } from '#lib/api.js';
import { NOT_IN_GIT } from '#lib/authoring.js';
import { caps, type Gate } from '#lib/state/caps.svelte.js';

export type { Gate };
export type Verb = keyof ObjectCaps;

const WORD: Record<Verb, string> = { create: 'create', update: 'change', delete: 'delete' };

const ns = (namespace: string, pick: (n: NamespaceCaps) => boolean, what: string) =>
	caps.nsGate(namespace, pick, `You may not ${what} in ${namespace}.`);
const proj = (namespace: string, pick: (n: NamespaceCaps) => boolean, what: string) =>
	caps.nsGate(namespace, pick, `You may not ${what}.`);
const cluster = (pick: (c: ClusterCaps) => boolean, what: string) =>
	caps.clusterGate(pick, `You may not ${what}.`);

// VMs, in their namespace.
export const vmGate = {
	create: (n: string) => ns(n, (c) => c.vm.create, 'create VMs'),
	update: (n: string) => ns(n, (c) => c.vm.update, 'change VMs'),
	delete: (n: string) => ns(n, (c) => c.vm.delete, 'delete VMs'),
	restart: (n: string) => ns(n, (c) => c.restart, 'restart VMs'),
	pause: (n: string) => ns(n, (c) => c.pause, 'pause VMs'),
	unpause: (n: string) => ns(n, (c) => c.unpause, 'unpause VMs'),
	migrate: (n: string) => ns(n, (c) => c.migrate, 'live-migrate VMs'),
	console: (n: string) => ns(n, (c) => c.console, 'open VM consoles'),
	clone: (n: string) => ns(n, (c) => c.clone, 'clone VMs'),
	snapshotTake: (n: string) => ns(n, (c) => c.snapshotTake, 'take snapshots'),
	snapshotRestore: (n: string) => ns(n, (c) => c.snapshotRestore, 'restore snapshots'),
	snapshotDelete: (n: string) => ns(n, (c) => c.snapshotDelete, 'delete snapshots'),
	resync: (n: string) => ns(n, (c) => c.resync, 'resync VMs'),
};

// An edit to a VM needs git to hold it, then the right to change VMs there.
export function vmEditGate(vm: { namespace: string; sourceFile?: string }): Gate {
	return vm.sourceFile ? vmGate.update(vm.namespace) : { ok: false, why: NOT_IN_GIT };
}

// The project a namespace belongs to.
export const projectGate = {
	templateEdit: (n: string) =>
		proj(n, (c) => c.project.templateEdit, "change this project's templates"),
	newNamespace: (n: string) =>
		proj(n, (c) => c.project.newNamespace, 'add a namespace to this project'),
	adopt: (n: string) => proj(n, (c) => c.project.adopt, 'attach a repo to this project'),
	release: (n: string) => proj(n, (c) => c.project.release, 'release this project'),
	grantAdmin: (n: string) => proj(n, (c) => c.project.grantAdmin, 'name owners for this project'),
	syncAccess: (n: string) => proj(n, (c) => c.project.syncAccess, 'grant sync access'),
};

// templateGate is writing one template library: the shared one ('platform'),
// or the project's that namespace belongs to.
export function templateGate(library: string, namespace: string): Gate {
	return library === 'platform' ? clusterGate.sharedLibrary() : projectGate.templateEdit(namespace);
}

// A VM saved as a template lands in its project's library or the shared one.
export function templateSaveGate(namespace: string): Gate {
	const own = projectGate.templateEdit(namespace);
	return own.ok || clusterGate.sharedLibrary().ok ? { ok: true, why: '' } : own;
}

// A project's verdicts ride each of its namespaces' answers; any one speaks for it.
export const projectNS = (p: { namespaces: { namespace: string }[] }) =>
	p.namespaces[0]?.namespace ?? '';

// repoGate is attaching a repo to a repoless project, or re-creating a lost
// one: both read ProjectCaps.Adopt, the route's own check. Naming owners also
// needs ProjectCaps.GrantAdmin (ownersGate).
export function repoGate(p: { repo?: string; namespaces: { namespace: string }[] }): Gate {
	const n = projectNS(p);
	return p.repo
		? proj(n, (c) => c.project.adopt, "recover this project's repo")
		: projectGate.adopt(n);
}

// Namespaced network objects, by draft resource. A network attachment git
// still declares is its own kind on the network routes.
const NS_OBJECT: Record<string, { pick: (c: NamespaceCaps) => ObjectCaps; noun: string }> = {
	network: { pick: (c) => c.network, noun: 'project networks' },
	networkattachment: { pick: (c) => c.networkAttachment, noun: 'network attachments' },
	networkpolicy: { pick: (c) => c.networkPolicy, noun: 'project rules' },
	egressfirewall: { pick: (c) => c.egressFirewall, noun: 'outbound rules' },
};
// Cluster-scoped objects, by draft resource.
const CLUSTER_OBJECT: Record<string, { pick: (c: ClusterCaps) => ObjectCaps; noun: string }> = {
	network: { pick: (c) => c.network, noun: 'shared networks' },
	uplink: { pick: (c) => c.uplink, noun: 'uplinks' },
	egressip: { pick: (c) => c.egressIP, noun: 'fixed outbound IPs' },
	externalroute: { pick: (c) => c.externalRoute, noun: 'external routes' },
	adminnetworkpolicy: { pick: (c) => c.adminNetworkPolicy, noun: 'cluster rules' },
	baselineadminnetworkpolicy: {
		pick: (c) => c.baselineAdminNetworkPolicy,
		noun: 'the baseline cluster rule',
	},
};

export const CLUSTER_NS = 'cluster';

// objectGate is a git-declared object's create, change or delete, by its
// draft identity (namespace 'cluster' when cluster-scoped).
export function objectGate(ref: { resource: string; namespace: string }, verb: Verb): Gate {
	if (ref.namespace === CLUSTER_NS) {
		const k = CLUSTER_OBJECT[ref.resource];
		return k
			? cluster((c) => k.pick(c)[verb], `${WORD[verb]} ${k.noun}`)
			: { ok: false, why: `You may not ${WORD[verb]} this.` };
	}
	const k = NS_OBJECT[ref.resource];
	return k
		? ns(ref.namespace, (c) => k.pick(c)[verb], `${WORD[verb]} ${k.noun}`)
		: { ok: false, why: `You may not ${WORD[verb]} this in ${ref.namespace}.` };
}

export const clusterObjectGate = (resource: string, verb: Verb) =>
	objectGate({ resource, namespace: CLUSTER_NS }, verb);

// Adopting stages the declaring file, a create, or an edit when git already
// describes the object otherwise: either right opens it.
export function objectAdoptGate(ref: { resource: string; namespace: string }): Gate {
	const create = objectGate(ref, 'create');
	return create.ok ? create : objectGate(ref, 'update').ok ? { ok: true, why: '' } : create;
}

// The platform tier and the cluster-wide actions.
export const clusterGate = {
	platform: () => cluster((c) => c.platform, 'change the platform'),
	platformRead: () => cluster((c) => c.platformRead, "read the platform's changes"),
	namespaceCreate: () => cluster((c) => c.namespaceCreate, 'create projects or namespaces'),
	vmNetwork: () => cluster((c) => c.vmNetwork, 'give a namespace a VM network'),
	grantAdmin: () => cluster((c) => c.grantAdmin, 'grant project access'),
	sharedLibrary: () => cluster((c) => c.sharedLibrary, 'change the shared template library'),
	rebalanceOn: () => cluster((c) => c.drsEnable, 'turn on rebalancing'),
	rebalancePSI: () => cluster((c) => c.drsPSI, 'turn on host pressure readings'),
	rebalanceOff: () => cluster((c) => c.drsDisable, 'turn off rebalancing'),
	oauthClient: () => cluster((c) => c.oauthClient, 'finish single sign-on'),
};
