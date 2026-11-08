// Edit and delete for the git-declared network-family objects (segments,
// firewall rules, Tier-0 services). One entry point for every surface that
// lists them: the object read back from git feeds the same modal that created
// it, and a delete stages the file's removal like a VM delete.
import {
	api,
	Unauthorized,
	type AdminNetworkPolicyCreate,
	type EgressFirewallCreate,
	type EgressIPCreate,
	type ExternalRouteCreate,
	type Network,
	type NetworkCreate,
	type NetworkPolicyCreate,
	type ObjectSpec,
	type Policy,
	type Uplink,
	type UplinkCreate,
} from '#lib/api.js';
import { friendlyError } from '#lib/format.js';
import { action } from '#lib/resource.svelte.js';
import { objectAdoptGate, objectGate } from '#lib/gates.js';
import { inventory } from '#lib/state/inventory.svelte.js';
import { ui } from '#lib/state/ui.svelte.js';

/** The draft identity of an object: resource, namespace ('cluster' when cluster-scoped), name. */
export interface ObjectRef {
	resource: string;
	namespace: string;
	name: string;
}

const CLUSTER = 'cluster';

const policyResource: Record<Policy['kind'], string> = {
	dfw: 'networkpolicy',
	gateway: 'egressfirewall',
	admin: 'adminnetworkpolicy',
	baseline: 'baselineadminnetworkpolicy',
	egressip: 'egressip',
	route: 'externalroute',
};

export function networkRef(n: Network): ObjectRef {
	return {
		resource: 'network',
		namespace: n.scope === 'shared' ? CLUSTER : (n.namespace ?? ''),
		name: n.name,
	};
}

// A project network git still declares as a NetworkAttachmentDefinition rides
// the network routes but is its own kind to their gate.
const networkGate = (n: Network, verb: 'create' | 'update' | 'delete') =>
	objectGate(
		n.scope !== 'shared' && n.backing === 'NetworkAttachmentDefinition'
			? { ...networkRef(n), resource: 'networkattachment' }
			: networkRef(n),
		verb,
	);

export function policyRef(p: Policy): ObjectRef {
	return { resource: policyResource[p.kind], namespace: p.namespace || CLUSTER, name: p.name };
}

// An uplink's identity is its NodeNetworkConfigurationPolicy; the builtin one
// and an uplink folded from several policies carry none and have no actions.
export function uplinkRef(u: Uplink): ObjectRef {
	return { resource: 'uplink', namespace: CLUSTER, name: u.policy ?? '' };
}
export const canAdoptUplink = (u: Uplink) =>
	!!u.policy && !u.sourceFile && objectAdoptGate(uplinkRef(u)).ok;
export const canEditUplink = (u: Uplink) =>
	!!u.policy && !!u.sourceFile && objectGate(uplinkRef(u), 'update').ok;
export const canDeleteUplink = (u: Uplink) =>
	!!u.policy && !!u.sourceFile && objectGate(uplinkRef(u), 'delete').ok;

// Adoption is offered where git declares nothing and the caller may author the
// object there: a project object also needs its namespace in a repo-backed
// project, and a kind the tenant repo holds.
export function canAdoptNetwork(n: Network): boolean {
	if (n.sourceFile) return false;
	if (
		n.scope !== 'shared' &&
		(platformTier(n) || !inventory.repoNamespaces.includes(n.namespace ?? ''))
	)
		return false;
	return objectAdoptGate(networkRef(n)).ok;
}

// The kinds a tenant repo may hold (pkg/tenancy TenantKinds, which its
// AppProject whitelists). Anything else in a project's namespace, a raw
// network attachment say, is platform tier: the tenant repo refuses it, and
// no adopt route takes a namespaced object into the platform repo.
const TENANT_KINDS = new Set([
	'VirtualMachine',
	'DataVolume',
	'UserDefinedNetwork',
	'EgressFirewall',
	'NetworkPolicy',
]);
export const platformTier = (n: Network): boolean =>
	n.scope !== 'shared' && !TENANT_KINDS.has(n.backing);
export function canAdoptPolicy(p: Policy): boolean {
	if (p.sourceFile) return false;
	if (p.namespace && !inventory.repoNamespaces.includes(p.namespace)) return false;
	return objectAdoptGate(policyRef(p)).ok;
}

/**
 * Stage the running object's manifest into Changes, as a VM adopt does: a
 * create when git has nothing, an edit when the running state drifted from it.
 */
export async function openAdopt(ref: ObjectRef, drifted = false) {
	try {
		await api.adoptObject(ref.resource, ref.namespace, ref.name);
		ui.toastStaged(
			drifted
				? `Running state of ${ref.name} staged into Changes - open a PR to bring git up to date.`
				: `${ref.name} staged into Changes - open a PR to adopt it into git.`,
		);
	} catch (e) {
		if (e instanceof Unauthorized) return;
		ui.showToast(friendlyError(e), { kind: 'error' });
	}
}

// A segment's subnet, topology, VLAN and uplink are frozen by OVN-K once
// created, so only a shared segment's publication list is editable. A primary
// network is born and dies with its namespace (one file), so it has no delete.
export const canEditNetwork = (n: Network) =>
	!!n.sourceFile && n.scope === 'shared' && networkGate(n, 'update').ok;
export const canDeleteNetwork = (n: Network) =>
	!!n.sourceFile && n.kind !== 'default' && networkGate(n, 'delete').ok;
// Restoring a network's earlier version, or adopting its live changes, edits it.
export const canAuthorNetwork = (n: Network) => !!n.sourceFile && networkGate(n, 'update').ok;
export const canEditPolicy = (p: Policy) => !!p.sourceFile && objectGate(policyRef(p), 'update').ok;
export const canDeletePolicy = (p: Policy) =>
	!!p.sourceFile && objectGate(policyRef(p), 'delete').ok;

// Restoring an object's earlier version is an edit of it.
export const canAuthorRef = (ref: ObjectRef) => objectGate(ref, 'update').ok;

/**
 * Read the object back from git and open its form with those values. A manifest
 * the form has no field for (the server says why, or the form's own row model
 * is narrower) opens as the manifest itself.
 */
export async function openEdit(ref: ObjectRef) {
	// The read outlasts a click: a dialog opened or closed meanwhile is the
	// user's newer intent, and this one must not replace it.
	const shown = ui.modal;
	let read: ObjectSpec | undefined;
	const ok = await action({ toast: ui.toastError }).run(async () => {
		read = await api.objectSpec(ref.resource, ref.namespace, ref.name);
	});
	if (!ok || !read || ui.modal !== shown) return;
	const modal = read.spec !== undefined ? modalFor(ref.resource, read.spec) : null;
	ui.modal = modal ?? {
		kind: 'editManifest',
		...ref,
		sourceFile: read.sourceFile,
		yaml: read.manifest,
		baseBlob: read.baseBlob,
		reason: read.reason ?? 'the form holds one selector and one port per rule',
	};
}

export function openDelete(ref: ObjectRef, sourceFile: string) {
	ui.modal = { kind: 'deleteObject', ...ref, sourceFile };
}

// The forms hold one selector and one port per rule; a spec wider than that
// (git allows it) must not be flattened into the form and re-rendered narrower.
const one = (m?: Record<string, string>) => Object.keys(m ?? {}).length <= 1;

function modalFor(resource: string, spec: unknown): typeof ui.modal {
	const namespaces = inventory.namespaces;
	switch (resource) {
		case 'network':
			return { kind: 'newNetwork', initial: spec as NetworkCreate };
		case 'uplink':
			return { kind: 'uplink', initial: spec as UplinkCreate };
		case 'networkpolicy': {
			const s = spec as NetworkPolicyCreate;
			const fits =
				one(s.appliedTo) &&
				(s.ingress ?? []).every(
					(r) => (r.from?.length ?? 0) <= 1 && one(r.from?.[0]) && (r.ports?.length ?? 0) <= 1,
				);
			return fits ? { kind: 'dfw', namespaces, initial: s } : null;
		}
		case 'egressfirewall': {
			const s = spec as EgressFirewallCreate;
			const fits = s.rules.every((r) => (r.ports?.length ?? 0) <= 1);
			return fits ? { kind: 'egressFw', namespaces, initial: s } : null;
		}
		case 'adminnetworkpolicy':
		case 'baselineadminnetworkpolicy': {
			const s = spec as AdminNetworkPolicyCreate;
			const fits =
				one(s.subject) &&
				!s.egress?.length &&
				(s.ingress ?? []).every(
					(r) => r.peers.length === 1 && one(r.peers[0]) && (r.ports?.length ?? 0) <= 1,
				);
			return fits ? { kind: 'adminFw', initial: s } : null;
		}
		case 'egressip':
			return { kind: 'tier0', initial: { kind: 'snat', spec: spec as EgressIPCreate } };
		case 'externalroute':
			return { kind: 'tier0', initial: { kind: 'route', spec: spec as ExternalRouteCreate } };
	}
	return null;
}
