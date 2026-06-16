// Port-group presentation helpers: turn a VM NIC's raw OVN-K network ref into the
// vCenter object a VMware admin recognizes. Shared by the VM detail's Network
// adapters and the Networks lens panel so they label networks identically.
import type { Network, NIC } from '$lib/api';

/** The vCenter-facing label for a port-group kind. */
export function kindLabel(kind: Network['kind']): string {
	switch (kind) {
		case 'default':
			return 'VM Network';
		case 'vlan':
			return 'VLAN';
		default:
			return 'Internal';
	}
}

/**
 * networkByRef matches a multus networkName ref ("namespace/nad" or a bare name)
 * to a port group. Returns undefined for the pod default / no match.
 */
export function networkByRef(ref: string | undefined, networks: Network[]): Network | undefined {
	if (!ref || ref === 'pod') return undefined;
	const base = ref.includes('/') ? ref.slice(ref.lastIndexOf('/') + 1) : ref;
	return networks.find((n) => n.attachRef === ref) ?? networks.find((n) => n.name === base);
}

/**
 * resolveNIC resolves a VM's NIC to its port group. The default interface
 * (network "" or "pod") is transparently backed by a primary ("VM Network") UDN
 * in the VM's namespace when one exists — so an admin sees "network-a (VM
 * Network)" rather than the bare "pod".
 */
export function resolveNIC(nic: NIC, vmNamespace: string, networks: Network[]): Network | undefined {
	if (!nic.network || nic.network === 'pod') {
		return networks.find((n) => n.kind === 'default' && n.namespace === vmNamespace);
	}
	return networkByRef(nic.network, networks);
}

/**
 * attachableNetworks are the secondary port groups a VM in `namespace` may add as
 * a NIC: shared (CUDN) networks published to that namespace plus the namespace's
 * own non-default networks. The primary ("VM Network") backs the default NIC, so
 * it's never an addable adapter. Shared by NewVMWizard + EditSettings so the two
 * attach pickers can't drift apart.
 */
export function attachableNetworks(networks: Network[], namespace: string): Network[] {
	return networks.filter(
		(n) =>
			n.kind !== 'default' &&
			(n.scope === 'shared' ? (n.namespaces ?? []).includes(namespace) : n.namespace === namespace)
	);
}

/** attachRef is how a VM attaches to a port group: its attachRef, else the bare name. */
export function attachRef(n: Network): string {
	return n.attachRef ?? n.name;
}
