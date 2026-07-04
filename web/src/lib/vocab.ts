// The dual-vocabulary layer. dotvirt presents OVN-K networking under two idioms at
// once: the NSX-T terms a network admin reaches for (Segment, Tier-0/Tier-1
// Gateway, Distributed Firewall) and the vSphere/vCenter terms the rest of the UI
// already speaks (Port Group, VM Network, uplink). Every networking label flows
// through here so the two idioms never drift and a reader from either world can
// self-orient. The backend model keeps neutral field names — this is presentation
// only, the networking analog of $lib/networks' port-group helpers.
import type { Network } from '$lib/api';

// A concept named in both idioms, with the OVN-K/Kubernetes object behind it for
// the detail drawers and tooltips.
export interface Term {
	nsx: string; // the NSX-T-facing name (primary — this is who we're dressing for)
	vsphere: string; // the vSphere/vCenter synonym shown alongside it
	backing?: string; // the OVN-K / Kubernetes kind it renders to
}

// The shared glossary. Keyed by concept, not by API kind, so a component asks for
// `TERMS.tier1` rather than knowing which CRD backs it.
export const TERMS = {
	segment: { nsx: 'Segment', vsphere: 'Port Group' },
	tier0: {
		nsx: 'Tier-0 Gateway',
		vsphere: 'Provider',
		backing: 'uplink + EgressIP + RouteAdvertisements',
	},
	tier1: {
		nsx: 'Tier-1 Gateway',
		vsphere: 'Project Router',
		backing: 'primary UserDefinedNetwork',
	},
	uplink: {
		nsx: 'Transport / Uplink',
		vsphere: 'Physical uplink',
		backing: 'NodeNetworkConfigurationPolicy',
	},
	gatewayFirewall: { nsx: 'Gateway Firewall', vsphere: 'Egress Rules', backing: 'EgressFirewall' },
	snat: { nsx: 'Source NAT', vsphere: 'Egress SNAT', backing: 'EgressIP' },
	dhcp: { nsx: 'DHCP / IP Pool', vsphere: 'IP Pool', backing: 'UDN subnets (IPAM)' },
	bgp: { nsx: 'Route Advertisement', vsphere: 'BGP peering', backing: 'RouteAdvertisements' },
	dfw: {
		nsx: 'Distributed Firewall',
		vsphere: 'Security Policy',
		backing: 'NetworkPolicy / AdminNetworkPolicy',
	},
	group: { nsx: 'Group', vsphere: 'Selector', backing: 'label selector' },
	// Content-library concepts (both idioms already agree on these names).
	template: {
		nsx: 'VM Template',
		vsphere: 'VM Template',
		backing: 'VirtualMachineTemplate (template.kubevirt.io/v1beta1) in git',
	},
	library: {
		nsx: 'Template Library',
		vsphere: 'Content Library',
		backing: 'templates/ in the project or platform repo',
	},
	customization: {
		nsx: 'Customization',
		vsphere: 'Customization Spec',
		backing: 'template parameters + cloud-init',
	},
	tag: { nsx: 'Tag', vsphere: 'Custom Attribute', backing: 'label' },
} satisfies Record<string, Term>;

// Render a term as "NSX (vSphere)" — the default dual presentation for a heading or
// chip. Components that have room for two lines can read t.nsx / t.vsphere directly.
export function dual(t: Term): string {
	return `${t.nsx} (${t.vsphere})`;
}

// A segment kind named in both idioms plus its OVN-K backing — the dual-vocabulary
// successor to $lib/networks' kindLabel. The primary "VM Network" is the Tier-1's
// own segment (a primary UDN, born with its namespace); VLAN segments ride the
// provider edge; everything else is an isolated overlay segment, project- or
// cluster-scoped.
export interface SegmentType extends Term {
	backing: string;
}
export function segmentType(n: Network): SegmentType {
	switch (n.kind) {
		case 'default':
			return {
				nsx: 'Tier-1 Segment',
				vsphere: 'VM Network',
				backing: 'primary UserDefinedNetwork',
			};
		case 'vlan':
			return {
				nsx: 'VLAN Segment',
				vsphere: 'VLAN',
				backing: 'localnet ClusterUserDefinedNetwork',
			};
		default:
			return n.scope === 'shared'
				? {
						nsx: 'Overlay Segment',
						vsphere: 'Shared Port Group',
						backing: 'ClusterUserDefinedNetwork',
					}
				: { nsx: 'Overlay Segment', vsphere: 'Internal Port Group', backing: 'UserDefinedNetwork' };
	}
}
