<script lang="ts">
	import { untrack } from 'svelte';
	import { clusterGate } from '#lib/gates.js';
	import { CHECKING } from '#lib/state/caps.svelte.js';
	import { gateways } from '#lib/state/gateways.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import ErrorNote from '#lib/components/ErrorNote.svelte';
	import Note from '#lib/components/Note.svelte';
	import GatewayMap from './GatewayMap.svelte';
	import YourRoutes from './YourRoutes.svelte';

	// One tab, two audiences, on the capability the server asks: an operator
	// sees every Gateway, anyone else the routes of their own projects.
	const gate = $derived(clusterGate.gateways());
	const deciding = $derived(!gate.ok && gate.why === CHECKING);

	$effect(() => {
		void inventory.routesVersion;
		if (deciding) return;
		const operator = gate.ok;
		untrack(() => gateways.pull(operator));
	});
</script>

<div class="flex flex-col gap-4">
	<ErrorNote error={gateways.error} />
	{#if deciding}
		<p class="text-sm text-ink-muted">{CHECKING}</p>
	{:else if gate.ok}
		{#if gateways.overview?.stale}
			<Note tone="warn" class="px-3 py-2 text-xs">
				Showing the last known state: the watch on routes is down.
			</Note>
		{/if}
		{#if !gateways.overview?.synced}
			<p class="text-sm text-ink-muted">Reading routes...</p>
		{:else}
			<GatewayMap overview={gateways.overview} />
		{/if}
	{:else if gateways.routes}
		<YourRoutes routes={gateways.routes} />
	{:else if !gateways.error}
		<p class="text-sm text-ink-muted">Reading your routes...</p>
	{/if}
</div>
