<script lang="ts">
	import type { Network } from '#lib/api.js';
	import { kindLabel, attachRef } from '#lib/networks.js';

	let {
		attachPrimary = $bindable(),
		selectedNetworks = $bindable(),
		available,
		primaryLabel,
		namespace,
		hasNetwork,
	}: {
		// Optional for a VM, unlike a pod: it may run on secondary networks alone.
		attachPrimary: boolean;
		selectedNetworks: string[]; // attach refs
		available: Network[];
		primaryLabel: string;
		namespace: string;
		hasNetwork: boolean;
	} = $props();

	function toggleNet(n: Network, on: boolean) {
		const ref = attachRef(n);
		selectedNetworks = on ? [...selectedNetworks, ref] : selectedNetworks.filter((r) => r !== ref);
	}
</script>

<div class="space-y-2">
	<p class="text-xs text-ink-muted">
		Choose the networks this VM attaches to. The primary is attached by default, but a VM may run on
		secondary networks alone — at least one is required.
	</p>
	<div class="mt-1 space-y-1 rounded border border-line-strong p-2">
		<label class="flex items-center gap-2 text-xs">
			<input type="checkbox" bind:checked={attachPrimary} />
			<span class="text-ink-soft">{primaryLabel}</span>
			<span class="rounded bg-inset-strong px-1.5 py-0.5 text-[11px] text-ink-muted"
				>VM Network</span
			>
			<span class="text-[11px] text-ink-faint">primary</span>
		</label>
		{#each available as net (net.scope + '/' + (net.namespace ?? '') + '/' + net.name)}
			<label class="flex items-center gap-2 text-xs">
				<input
					type="checkbox"
					checked={selectedNetworks.includes(attachRef(net))}
					onchange={(e) => toggleNet(net, e.currentTarget.checked)}
				/>
				<span class="text-ink-soft">{net.name}</span>
				<span class="rounded bg-inset-strong px-1.5 py-0.5 text-[11px] text-ink-muted"
					>{kindLabel(net.kind)}{net.vlan ? ` ${net.vlan}` : ''}</span
				>
				{#if net.scope === 'shared'}<span class="text-[11px] text-ink-faint">shared</span>{/if}
			</label>
		{/each}
	</div>
	{#if !available.length}
		<p class="mt-1 text-xs text-ink-faint">No secondary networks available for {namespace}.</p>
	{/if}
	{#if !hasNetwork}
		<p class="text-xs text-warn-ink">Select at least one network — a VM needs a NIC.</p>
	{/if}
</div>
