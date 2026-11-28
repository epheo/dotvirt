<script lang="ts">
	import type { DesktopPool } from '#lib/api.js';
	import { poolState } from '#lib/desktops.js';
	import { hrefForScope } from '#lib/nav.js';
	import { desktops } from '#lib/state/desktops.svelte.js';

	// The pools that make no desktop because their class is missing, each
	// with the way to its page, which says what to do.
	let { pools }: { pools: DesktopPool[] } = $props();

	const stuck = $derived(pools.filter((p) => poolState(p).missingClass));
</script>

{#if stuck.length}
	<div
		class="rounded-lg border border-danger/30 bg-danger-soft px-4 py-3 text-[13px] text-danger-ink"
	>
		{#each stuck as p (`${p.namespace}/${p.name}`)}
			{@const project = desktops.owners.get(p.namespace)}
			<p>
				Pool
				{#if project}<a
						class="font-medium underline"
						href={hrefForScope({ kind: 'pool', project, namespace: p.namespace, pool: p.name })}
					>
						{p.name}
					</a>{:else}{p.name}{/if}
				in {p.namespace} names class {p.class}, which does not exist: it makes no desktop.
			</p>
		{/each}
	</div>
{/if}
