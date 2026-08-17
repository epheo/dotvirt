// The dual-vocabulary layer. dotvirt presents OVN-K networking under two idioms at
// once: the fabric terms a network admin reaches for (Segment, Provider/Project
// Gateway, Distributed Firewall) and the inventory terms the rest of the UI
// already speaks (Port Group, VM Network, uplink). Every networking label flows
// through here so the two idioms never drift and a reader from either world can
// self-orient. The backend model keeps neutral field names - this is presentation
// only, the networking analog of $lib/networks' port-group helpers.
import type { Network } from '$lib/api';

// A concept named in both idioms, with the OVN-K/Kubernetes object behind it for
// the detail drawers and tooltips.
interface Term {
	net: string; // the fabric-facing name (primary - who the networking views dress for)
	virt: string; // the inventory synonym shown alongside it
	backing?: string; // the OVN-K / Kubernetes kind it renders to
}

// The shared glossary. Keyed by concept, not by API kind, so a component asks for
// `TERMS.tier1` rather than knowing which CRD backs it.
export const TERMS = {
	segment: { net: 'Segment', virt: 'Port Group' },
	tier0: {
		net: 'Provider Gateway',
		virt: 'Cluster edge',
		backing: 'uplink + EgressIP + RouteAdvertisements',
	},
	tier1: {
		net: 'Project Gateway',
		virt: 'Project Router',
		backing: 'primary UserDefinedNetwork',
	},
	uplink: {
		net: 'Transport / Uplink',
		virt: 'Physical uplink',
		backing: 'NodeNetworkConfigurationPolicy',
	},
	gatewayFirewall: { net: 'Gateway Firewall', virt: 'Egress Rules', backing: 'EgressFirewall' },
	snat: { net: 'Source NAT', virt: 'Egress SNAT', backing: 'EgressIP' },
	dhcp: { net: 'DHCP / IP Pool', virt: 'IP Pool', backing: 'UDN subnets (IPAM)' },
	bgp: { net: 'Route Advertisement', virt: 'BGP peering', backing: 'RouteAdvertisements' },
	dfw: {
		net: 'Distributed Firewall',
		virt: 'Security Policy',
		backing: 'NetworkPolicy / AdminNetworkPolicy',
	},
	group: { net: 'Group', virt: 'Selector', backing: 'label selector' },
	// Content-library concepts (both idioms already agree on these names).
	template: {
		net: 'VM Template',
		virt: 'VM Template',
		backing: 'VirtualMachineTemplate (template.kubevirt.io/v1beta1) in git',
	},
	library: {
		net: 'Template Library',
		virt: 'Content Library',
		backing: 'templates/ in the project or platform repo',
	},
	customization: {
		net: 'Customization',
		virt: 'Customization Spec',
		backing: 'template parameters + cloud-init',
	},
	tag: { net: 'Tag', virt: 'Custom Attribute', backing: 'label' },
} satisfies Record<string, Term>;

// Render a term as "Fabric (Inventory)" - the default dual presentation for a heading
// or chip. Components that have room for two lines can read t.net / t.virt directly.
export function dual(t: Term): string {
	return `${t.net} (${t.virt})`;
}

// A segment kind named in both idioms plus its OVN-K backing - the dual-vocabulary
// successor to $lib/networks' kindLabel. The primary "VM Network" is the Project
// Gateway's own segment (a primary UDN, born with its namespace); VLAN segments ride
// the provider edge; everything else is an isolated overlay segment, project- or
// cluster-scoped.
interface SegmentType extends Term {
	backing: string;
}
export function segmentType(n: Network): SegmentType {
	switch (n.kind) {
		case 'default':
			return {
				net: 'Project Segment',
				virt: 'VM Network',
				backing: 'primary UserDefinedNetwork',
			};
		case 'vlan':
			return {
				net: 'VLAN Segment',
				virt: 'VLAN',
				backing: 'localnet ClusterUserDefinedNetwork',
			};
		default:
			return n.scope === 'shared'
				? {
						net: 'Overlay Segment',
						virt: 'Shared Port Group',
						backing: 'ClusterUserDefinedNetwork',
					}
				: { net: 'Overlay Segment', virt: 'Internal Port Group', backing: 'UserDefinedNetwork' };
	}
}
