<script lang="ts">
	import { untrack } from 'svelte';
	import { clusterGate } from '#lib/gates.js';
	import { routesTile } from '#lib/landing.js';
	import { CHECKING } from '#lib/state/caps.svelte.js';
	import { gateways } from '#lib/state/gateways.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import CountTile from '#lib/components/CountTile.svelte';

	// The Gateway's state on the Networking landing tab, read as the tab reads
	// it: an operator every route, anyone else their own projects'.
	const gate = $derived(clusterGate.gateways());
	const deciding = $derived(!gate.ok && gate.why === CHECKING);
	$effect(() => {
		void inventory.routesVersion;
		if (deciding) return;
		const operator = gate.ok;
		untrack(() => gateways.pull(operator));
	});

	const tile = $derived.by(() => {
		if (deciding) return null;
		if (gate.ok) {
			const o = gateways.overview;
			return o?.synced ? routesTile(o.routes, true) : null;
		}
		return gateways.routes ? routesTile(gateways.routes, false) : null;
	});
</script>

{#if tile}
	<CountTile {...tile} />
{:else if gateways.error}
	<CountTile
		label="Routes unavailable"
		body={gateways.error}
		link={{ label: 'Try again', onclick: () => gateways.pull(gate.ok) }}
	/>
{:else}
	<CountTile label="Reading routes" body="Counting the routes the Gateway serves." />
{/if}
