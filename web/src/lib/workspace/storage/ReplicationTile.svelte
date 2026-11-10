<script lang="ts">
	import { untrack } from 'svelte';
	import { clusterGate } from '#lib/gates.js';
	import { protectionTile, replicationTile } from '#lib/landing.js';
	import { mapRows } from '#lib/replication.js';
	import { CHECKING } from '#lib/state/caps.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { replication } from '#lib/state/replication.svelte.js';
	import { storage } from '#lib/state/storage.svelte.js';
	import CountTile from '#lib/components/CountTile.svelte';

	// Replication's state on the Storage landing tab, read as the tab reads it:
	// an operator every copy, anyone else their own disks.
	let { project, namespaces }: { project: string; namespaces: Set<string> } = $props();

	const gate = $derived(clusterGate.replication());
	const deciding = $derived(!gate.ok && gate.why === CHECKING);
	$effect(() => {
		void inventory.replicationVersion;
		if (deciding) return;
		const operator = gate.ok;
		untrack(() => replication.pull(operator));
	});

	const inScope = (ns: string) => !project || namespaces.has(ns);
	const owner = $derived(
		new Map(
			storage.rows
				.filter((d) => d.disk.claim)
				.map((d) => [`${d.vm.namespace}/${d.disk.claim}`, d.vm.name]),
		),
	);
	const tile = $derived.by(() => {
		if (deciding) return null;
		if (gate.ok) {
			const o = replication.overview;
			return o?.synced
				? replicationTile(
						mapRows(
							o.volumes.filter((v) => inScope(v.namespace ?? '')),
							storage.rows,
						),
					)
				: null;
		}
		const list = replication.protection;
		return list
			? protectionTile(
					list.filter((p) => inScope(p.namespace)),
					(p) => owner.get(`${p.namespace}/${p.claim}`) ?? p.claim,
				)
			: null;
	});
</script>

{#if tile}
	<CountTile {...tile} />
{:else if replication.error}
	<CountTile
		label="Replication unavailable"
		body={replication.error}
		link={{ label: 'Try again', onclick: () => replication.pull(gate.ok) }}
	/>
{:else}
	<CountTile label="Reading replicated storage" body="Counting the disks that survive a host." />
{/if}
