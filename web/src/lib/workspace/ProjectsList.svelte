<script lang="ts">
	import { hrefForScope } from '#lib/nav.js';
	import { DEFAULT_PAGE_SIZE, pageSlice } from '#lib/paging.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import Card from '#lib/components/Card.svelte';
	import Pager from '#lib/components/Pager.svelte';
	import ProjectSyncBadge from '#lib/components/ProjectSyncBadge.svelte';

	// Compute's root on a cluster without VMs: the summary there is about VMs,
	// and a project is what is left to open.
	const TH =
		'sticky top-0 z-[1] border-b border-line bg-panel/90 px-2 py-[9px] text-left text-xs font-bold whitespace-nowrap text-ink-muted backdrop-blur';
	const TD = 'h-10 border-b border-line-soft px-2';

	let pg = $state(1);
	let size = $state(DEFAULT_PAGE_SIZE);
	const slice = $derived(pageSlice(inventory.projects.length, pg, size));
	const rows = $derived(inventory.projects.slice(slice.start, slice.end));
</script>

<Card title="Projects" flush>
	{#if inventory.projects.length === 0}
		<p class="px-card py-3 text-sm text-ink-muted">No projects you can see yet.</p>
	{:else}
		<table class="w-full border-collapse text-[13px]">
			<thead>
				<tr>
					<th scope="col" class="{TH} pl-card">Project</th>
					<th scope="col" class="{TH} text-right">Namespaces</th>
					<th scope="col" class="{TH} pr-card">Sync</th>
				</tr>
			</thead>
			<tbody>
				{#each rows as p (p.name)}
					<tr class="hover:bg-select-soft">
						<td class="{TD} pl-card">
							<a
								class="text-accent-ink hover:underline"
								href={hrefForScope({ kind: 'project', project: p.name })}>{p.name}</a
							>
						</td>
						<td class="{TD} text-right tabular-nums">{p.namespaces.length}</td>
						<td class={TD}><ProjectSyncBadge gitOps={p.gitOps} compact /></td>
					</tr>
				{/each}
			</tbody>
		</table>
		{#if inventory.projects.length > Math.min(size, DEFAULT_PAGE_SIZE)}
			<Pager
				class="px-card py-3"
				total={inventory.projects.length}
				bind:page={pg}
				bind:size
				spread={false}
			/>
		{/if}
	{/if}
</Card>
