<script lang="ts">
	import { page } from '$app/state';
	import TreeRow from '#lib/components/TreeRow.svelte';
	import { hrefForScope, scopeFromPath, storageHref, storageTab } from '#lib/nav.js';
	import { TOP_GROUPS, fold, matches } from '#lib/rail.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { storage } from '#lib/state/storage.svelte.js';
	import { classTag, num, projectsBySize } from '#lib/storage.js';
	import RailFilter from './RailFilter.svelte';

	// The Storage rail: the project scope every tab reads (?project=), biggest
	// projects first with the rest folded, then the classes as drill-ins.
	storage.ensure();

	const scope = $derived(scopeFromPath(page.url.pathname));
	const onRoot = $derived(scope.kind === 'all');
	const current = $derived(onRoot ? page.url.searchParams.get('project') : null);
	const projects = $derived(projectsBySize(inventory.projects));

	let expanded = $state(false);
	let query = $state('');
	const folded = $derived(fold(projects, TOP_GROUPS, (p) => p.name === current));
	const shown = $derived(expanded ? projects.filter((p) => matches(query, p.name)) : folded.shown);

	// A project keeps the tab and the disk filters in view; the page and the
	// open disk belong to the old scope.
	function projectHref(name: string | null): string {
		if (!onRoot) return storageHref('library', { project: name ?? undefined });
		const q = new URLSearchParams(page.url.search);
		q.delete('page');
		q.delete('disk');
		q.delete('image');
		if (name) q.set('project', name);
		else q.delete('project');
		q.set('tab', storageTab(q.get('tab')));
		return `/storage?${q}`;
	}

	// The default class first (where disks that name none land), then local
	// before shared.
	const classes = $derived(
		storage.classNames
			.map((name) => {
				const info = storage.classes?.find((c) => c.name === name);
				const rank = info?.default ? 0 : info?.profile?.shared ? 2 : 1;
				return { name, tag: info ? classTag(info) : 'not on the cluster', rank };
			})
			.sort((a, b) => a.rank - b.rank),
	);

	const HEAD = 'px-2 pb-1 text-[11px] font-bold tracking-[.06em] text-side-dim uppercase';
</script>

<nav
	aria-label="Storage scope"
	class="flex flex-col gap-0.5 px-2.5 pt-3 pb-3 text-[13px] select-none"
>
	<div class={HEAD}>Projects</div>
	<TreeRow href={projectHref(null)} active={onRoot && !current}>
		<span class="truncate">All projects</span>
		{#snippet trailing()}<span class="text-xs text-side-dim">{num(projects.length)}</span>{/snippet}
	</TreeRow>
	{#if expanded && projects.length > 20}
		<RailFilter
			bind:value={query}
			label="Filter projects"
			placeholder="Filter {projects.length} projects"
		/>
	{/if}
	{#each shown as p (p.name)}
		<TreeRow href={projectHref(p.name)} active={p.name === current}>
			<span class="truncate">{p.name}</span>
			{#snippet trailing()}
				<span class="text-xs text-side-dim">{num(p.vms)} {p.vms === 1 ? 'VM' : 'VMs'}</span>
			{/snippet}
		</TreeRow>
	{/each}
	{#if expanded && query.trim() && shown.length === 0}
		<div class="px-2 py-1.5 text-xs text-side-dim">No project matches.</div>
	{/if}
	{#if folded.folded.length > 0}
		<button
			class="flex w-full items-center rounded py-1.5 pr-2 pl-2 text-left text-side-dim hover:bg-side-hover/60 hover:text-side-ink"
			aria-expanded={expanded}
			onclick={() => {
				expanded = !expanded;
				query = '';
			}}
		>
			{expanded
				? 'Fewer projects'
				: `${num(folded.folded.length)} more ${folded.folded.length === 1 ? 'project' : 'projects'}`}
		</button>
	{/if}

	<div class="{HEAD} pt-3">Classes</div>
	{#each classes as c (c.name)}
		<TreeRow
			href={hrefForScope({ kind: 'storage', storageClass: c.name })}
			active={scope.kind === 'storage' && scope.storageClass === c.name}
		>
			<span class="truncate">{c.name}</span>
			{#snippet trailing()}<span class="text-xs text-side-dim">{c.tag}</span>{/snippet}
		</TreeRow>
	{:else}
		<p class="px-2 py-1 text-xs text-side-dim">No storage classes visible.</p>
	{/each}
</nav>
