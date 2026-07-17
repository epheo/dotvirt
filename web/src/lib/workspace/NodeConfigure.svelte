<script lang="ts">
	import type { VM } from '$lib/api';
	import { inventory } from '$lib/state/inventory.svelte';
	import { ui } from '$lib/state/ui.svelte';
	import InfoCard from '$lib/components/InfoCard.svelte';
	import NodeActions from '$lib/components/NodeActions.svelte';
	import Row from '$lib/components/Row.svelte';

	// The host's Configure tab: maintenance verbs plus the physical fabric.
	// dotvirt owns nothing here — node configuration is the cluster platform's.
	let { node, vms }: { node: string; vms: VM[] } = $props();

	const nodeUplinks = $derived(inventory.uplinks.filter((u) => !u.nodes || u.nodes.includes(node)));
	const nics = $derived(inventory.physicalAdapters.filter((a) => a.node === node));
</script>

<div class="min-h-0 flex-1 overflow-y-auto p-4">
	<div class="max-w-2xl space-y-4">
		<InfoCard title="Node: {node}">
			<dl class="divide-y divide-line-soft text-[13px]">
				<Row label="VMs placed here" value={String(vms.length)} />
			</dl>
			<p class="border-t border-line-soft px-3 py-2 text-xs text-ink-faint">
				Node configuration is managed by the cluster platform, not dotvirt.
			</p>
		</InfoCard>

		<!-- Host maintenance: Enter/Exit Maintenance Mode + plain cordon (shown
		     only when the caller's token may patch nodes). -->
		<NodeActions {node} {vms} />

		{#if nodeUplinks.length}
			<InfoCard title="Uplinks">
				{#snippet action()}
					<button
						onclick={() => (ui.modal = { kind: 'uplink' })}
						disabled={!inventory.canManage}
						title={inventory.canManage ? '' : 'Requires platform-network authoring permission'}
						class="text-xs text-accent hover:underline disabled:text-ink-faint">+ Add uplink</button
					>
				{/snippet}
				<ul class="divide-y divide-line-soft px-3 text-[13px]">
					{#each nodeUplinks as u (u.name)}
						<li class="flex items-baseline justify-between gap-3 py-1.5">
							<span class="text-ink">{u.name}{u.builtin ? ' · default' : ''}</span>
							<span class="text-ink-faint"
								>{u.bridge} · {u.nodeCount} node{u.nodeCount === 1 ? '' : 's'}</span
							>
						</li>
					{/each}
				</ul>
			</InfoCard>
		{/if}

		<InfoCard title="Physical adapters">
			{#if !inventory.nmstatePresent}
				<p class="px-3 py-3 text-xs text-ink-faint">
					Install the NMState operator instance to discover physical adapters.
				</p>
			{:else if nics.length}
				<ul class="divide-y divide-line-soft px-3 text-[13px]">
					{#each nics as a (a.name)}
						<li class="flex items-baseline justify-between gap-3 py-1.5">
							<span class="text-ink">{a.name}</span>
							<span class="flex items-center gap-3 text-right text-ink-faint">
								<span>{a.role}</span>
								<span>{a.state}{a.mtu ? ` · MTU ${a.mtu}` : ''}</span>
							</span>
						</li>
					{/each}
				</ul>
			{:else}
				<p class="px-3 py-3 text-xs text-ink-faint">No physical adapters reported.</p>
			{/if}
		</InfoCard>
	</div>
</div>
