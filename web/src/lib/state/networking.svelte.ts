import type { Network, Project } from '#lib/api.js';
import { membership, type NetProblem } from '#lib/netmap.js';
import { networkKey } from '#lib/networks.js';
import { inventory } from './inventory.svelte.js';

// What every Networking surface derives from the live inventory and the
// catalog, once per frame for the rail, the header, the map and the pages.
class NetworkingState {
	readonly members = $derived(membership(inventory.allVMs, inventory.networks));
	readonly problems = $derived(inventory.netProblems);
	readonly byKey = $derived(new Map(inventory.networks.map((n) => [networkKey(n), n])));

	readonly projectNets = $derived(
		inventory.networks.filter((n) => n.scope !== 'shared' && n.kind !== 'vlan'),
	);
	readonly sharedNets = $derived(
		inventory.networks.filter((n) => n.scope === 'shared' && n.kind !== 'vlan'),
	);
	readonly vlanNets = $derived(inventory.networks.filter((n) => n.kind === 'vlan'));

	// Firewall objects: cluster rules, project rules (east-west and outbound),
	// and the outbound planes at the cluster edge.
	// In the order traffic meets them: admin rules by priority, the baseline last.
	readonly clusterRules = $derived(
		inventory.policies
			.filter((p) => p.kind === 'admin' || p.kind === 'baseline')
			.sort(
				(a, b) =>
					Number(a.kind === 'baseline') - Number(b.kind === 'baseline') ||
					(a.priority ?? 0) - (b.priority ?? 0) ||
					a.name.localeCompare(b.name),
			),
	);
	readonly projectRules = $derived(
		inventory.policies.filter((p) => p.kind === 'dfw' || p.kind === 'gateway'),
	);
	readonly egress = $derived(
		inventory.policies.filter((p) => p.kind === 'egressip' || p.kind === 'route'),
	);

	vmsOn(key: string) {
		return this.members.byNet.get(key) ?? [];
	}

	problemsOf(key: string): NetProblem[] {
		return this.problems.filter((p) => p.networks?.includes(key));
	}

	// The projects a shared network reaches: where it is published, else where
	// its VMs live.
	projectsOf(n: Network): string[] {
		const nss = n.namespaces?.length
			? n.namespaces
			: this.vmsOn(networkKey(n)).map((v) => v.namespace);
		return [...new Set(nss.map((ns) => inventory.projectOf(ns) || ns))];
	}

	// The project that owns a project network's namespace; '' for shared ones.
	ownerOf(n: Network): string {
		return n.scope === 'shared' || !n.namespace ? '' : inventory.projectOf(n.namespace);
	}

	netsOfProject(p: Project): Network[] {
		const nss = new Set(p.namespaces.map((n) => n.namespace));
		return this.projectNets.filter((n) => n.namespace && nss.has(n.namespace));
	}
}

export const networking = new NetworkingState();
