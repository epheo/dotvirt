import type { Inventory, NetworkInventory, VM } from '$lib/api';

// The live cluster read layer: the WS inventory snapshot plus the once-per-
// session networking inventory (GET /api/networks). Everything else the UI
// shows about cluster state derives from these two.
class InventoryStore {
	inventory = $state<Inventory | null>(null);
	error = $state('');
	// The networking read layer: the port-group catalog (so raw OVN-K refs render
	// as vCenter port groups) plus the physical fabric (uplinks + node NICs).
	// Changes rarely; backend caches 60s.
	netInv = $state<NetworkInventory | null>(null);

	readonly networks = $derived(this.netInv?.networks ?? []);
	readonly uplinks = $derived(this.netInv?.uplinks ?? []);
	readonly physicalAdapters = $derived(this.netInv?.physicalAdapters ?? []);
	readonly nmstatePresent = $derived(this.netInv?.nmstatePresent ?? false);
	// May the caller author platform-tier networking (cluster-scoped CUDN/uplink +
	// namespaces)? Matches the backend platformScope SSAR gate.
	readonly canManage = $derived(this.netInv?.canManage ?? false);
	// Per-action authoring authority (each the same SSAR the backend create
	// enforces). Undefined caps (older backend) read as false.
	readonly caps = $derived(this.netInv?.caps);

	// All VMs across the 3-level tree (project → namespace → vm).
	readonly allVMs = $derived(
		this.inventory
			? this.inventory.projects.flatMap((p) => p.namespaces.flatMap((n) => n.vms))
			: [],
	);
	readonly vmCount = $derived(this.allVMs.length);
	// Open PRs across the user's projects ride the live inventory stream, so a PR
	// merged anywhere repaints the dock + Changes pane with no manual refresh.
	readonly proposals = $derived(this.inventory?.proposals ?? []);
	readonly projectNames = $derived(
		this.inventory ? this.inventory.projects.map((p) => p.name) : [],
	);
	// A stable primitive key for the SET of project names: $derived arrays are a
	// new reference every inventory frame, which would re-fire effects on every VM
	// state change. Keying on this string fires them only when the set changes.
	readonly projectKey = $derived([...this.projectNames].sort().join('\0'));
	// The same trick for the SET of open PR lanes: a propose moves staged items
	// into a PR and a merge/close clears the lane — possibly from another tab.
	readonly proposalsKey = $derived(
		this.proposals
			.map((p) => `${p.project}#${p.prNumber}`)
			.sort()
			.join('\0'),
	);
	// Namespaces a VM can be created in: those in projects that have a repo (no
	// point staging into a project with no backing repo).
	readonly namespaces = $derived(
		this.inventory
			? this.inventory.projects
					.filter((p) => p.repo)
					.flatMap((p) => p.namespaces.map((n) => n.namespace))
			: [],
	);
	// Projects with a backing repo — the ones with commit history to browse +
	// revert from (the Changes panel's History section).
	readonly repoProjects = $derived(
		this.inventory ? this.inventory.projects.filter((p) => p.repo).map((p) => p.name) : [],
	);

	apply(inv: Inventory) {
		this.inventory = inv;
		this.error = '';
	}

	reset() {
		this.inventory = null;
		this.netInv = null;
	}

	findVM(namespace: string, name: string): VM | null {
		return this.allVMs.find((v) => v.namespace === namespace && v.name === name) ?? null;
	}

	projectOf(namespace: string): string {
		return (
			this.inventory?.projects.find((p) => p.namespaces.some((n) => n.namespace === namespace))
				?.name ?? ''
		);
	}
}

export const inventory = new InventoryStore();
