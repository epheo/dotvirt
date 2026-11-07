<script lang="ts">
	import { Plus } from '@lucide/svelte';
	import { page } from '$app/state';
	import { NETWORKING_TABS, networkingTab } from '#lib/nav.js';
	import { attachRef } from '#lib/networks.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { networking } from '#lib/state/networking.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import Breadcrumb from '#lib/components/Breadcrumb.svelte';
	import Button from '#lib/components/Button.svelte';
	import PageHeader from '#lib/components/PageHeader.svelte';
	import PlatformAdoptBanner from '#lib/components/PlatformAdoptBanner.svelte';
	import TabBar from '#lib/components/TabBar.svelte';
	import FirewallTab from './FirewallTab.svelte';
	import MapTab from './MapTab.svelte';
	import NetworksTab from './NetworksTab.svelte';
	import NetworkVMs from './NetworkVMs.svelte';
	import UplinksTab from './UplinksTab.svelte';

	// The Networking section's root: one header, five tabs. The map answers
	// what connects to what; the other tabs list everything at scale.
	const tab = $derived(networkingTab(page.url.searchParams.get('tab')));
	const fmt = (n: number) => n.toLocaleString('en-US');
	const counts: Record<string, number> = $derived({
		networks: inventory.networks.length,
		firewall: networking.clusterRules.length + networking.projectRules.length,
		uplinks: inventory.uplinks.length,
		vms: inventory.vmCount,
	});
	const tabs = $derived(NETWORKING_TABS.map((t) => ({ ...t, count: counts[t.id] })));
	const problems = $derived(networking.problems);
	const context = $derived(
		[
			`${fmt(inventory.vmCount)} VMs`,
			`${fmt(inventory.networks.length)} networks`,
			inventory.uplinks.length ? `${fmt(inventory.uplinks.length)} uplinks` : '',
		]
			.filter(Boolean)
			.join(', '),
	);
	const canCreate = $derived(inventory.namespaces.length > 0 || inventory.canManage);

	// New network fills in from what the map has selected: a network's project
	// and range, or a VM's namespace.
	function newNetwork() {
		const sel = page.url.searchParams.get('sel') ?? '';
		const net = sel.startsWith('net:') ? networking.byKey.get(sel.slice(4)) : undefined;
		if (net) {
			ui.modal = {
				kind: 'newNetwork',
				like: attachRef(net),
				from: `${net.name}, selected on the map`,
			};
			return;
		}
		if (sel.startsWith('vm:')) {
			const [ns, name] = sel.slice(3).split('/');
			ui.modal = { kind: 'newNetwork', namespace: ns, from: `${name}, selected on the map` };
			return;
		}
		ui.modal = { kind: 'newNetwork' };
	}
</script>

<div class="flex min-h-0 flex-1 flex-col bg-canvas">
	<div class="flex flex-col gap-2 px-6 pt-5">
		<Breadcrumb
			trail={[
				{ label: 'Networking', href: '/networking' },
				{ label: NETWORKING_TABS.find((t) => t.id === tab)!.label },
			]}
		/>
		<PageHeader
			title="Networking"
			status={problems.length
				? {
						tone: problems.some((p) => p.severity === 'danger') ? 'danger' : 'warn',
						text: `${problems.length} problem${problems.length === 1 ? '' : 's'}`,
					}
				: { tone: 'ok', text: 'No problems' }}
			{context}
		>
			{#snippet actions()}
				{#if canCreate}
					<Button variant="outline" onclick={newNetwork}><Plus size={16} />New network</Button>
				{/if}
			{/snippet}
		</PageHeader>
		<TabBar {tabs} active={tab} href={(t) => `?tab=${t}`} />
	</div>

	{#if tab === 'vms'}
		<NetworkVMs vms={inventory.allVMs} />
	{:else}
		<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
		<div
			class="min-h-0 flex-1 overflow-y-auto px-6 pt-4 pb-7"
			role="region"
			aria-label="Networking, {tab}"
			tabindex="0"
		>
			{#if tab === 'map'}
				<PlatformAdoptBanner />
				<MapTab />
			{:else if tab === 'networks'}
				<NetworksTab />
			{:else if tab === 'firewall'}
				<FirewallTab />
			{:else}
				<UplinksTab />
			{/if}
		</div>
	{/if}
</div>
