<script lang="ts">
	import { untrack } from 'svelte';
	import { clusterGate } from '#lib/gates.js';
	import { CHECKING } from '#lib/state/caps.svelte.js';
	import { frontDoor } from '#lib/state/frontdoor.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import ErrorNote from '#lib/components/ErrorNote.svelte';
	import Note from '#lib/components/Note.svelte';
	import FrontDoorMap from './FrontDoorMap.svelte';
	import YourRoutes from './YourRoutes.svelte';

	// One tab, two audiences, on the capability the server asks: an operator
	// sees every front door, anyone else the routes of their own projects.
	const gate = $derived(clusterGate.frontDoor());
	const deciding = $derived(!gate.ok && gate.why === CHECKING);

	$effect(() => {
		void inventory.routesVersion;
		if (deciding) return;
		const operator = gate.ok;
		untrack(() => frontDoor.pull(operator));
	});
</script>

<div class="flex flex-col gap-4">
	<ErrorNote error={frontDoor.error} />
	{#if deciding}
		<p class="text-sm text-ink-muted">{CHECKING}</p>
	{:else if gate.ok}
		{#if frontDoor.overview?.stale}
			<Note tone="warn" class="px-3 py-2 text-xs">
				Showing the last known state: the watch on routes is down.
			</Note>
		{/if}
		{#if !frontDoor.overview?.synced}
			<p class="text-sm text-ink-muted">Reading routes...</p>
		{:else}
			<FrontDoorMap overview={frontDoor.overview} />
		{/if}
	{:else if frontDoor.routes}
		<YourRoutes routes={frontDoor.routes} />
	{:else if !frontDoor.error}
		<p class="text-sm text-ink-muted">Reading your routes...</p>
	{/if}
</div>
