<script lang="ts">
	import { page } from '$app/state';
	import { hrefForScope, scopeFromPath, storageHref, storageTab } from '#lib/nav.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { storage } from '#lib/state/storage.svelte.js';
	import { classTag, num, projectsBySize } from '#lib/storage.js';

	// The Storage rail: the project scope every tab reads (?project=), biggest
	// projects first with the rest folded, then the classes as drill-ins.
	const TOP = 5;

	storage.ensure();

	const scope = $derived(scopeFromPath(page.url.pathname));
	const onRoot = $derived(scope.kind === 'all');
	const current = $derived(onRoot ? page.url.searchParams.get('project') : null);
	const projects = $derived(projectsBySize(inventory.projects));

	let expanded = $state(false);
	let filter = $state('');
	const shown = $derived.by(() => {
		if (!expanded) {
			const top = projects.slice(0, TOP);
			// The scoped project stays in view even when it is not a big one.
			const cur = projects.find((p) => p.name === current);
			return cur && !top.includes(cur) ? [...top, cur] : top;
		}
		const f = filter.trim().toLowerCase();
		return f ? projects.filter((p) => p.name.toLowerCase().includes(f)) : projects;
	});
	const hidden = $derived(Math.max(0, projects.length - TOP));

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

	const ROW = 'flex items-center justify-between gap-2 rounded px-2 py-1.5';
</script>

{#snippet row(href: string, label: string, count: string, active: boolean)}
	<a
		{href}
		aria-current={active ? 'page' : undefined}
		class="{ROW} {active
			? 'bg-side-hover text-side-ink'
			: 'text-side-dim hover:bg-side-hover hover:text-side-ink'}"
	>
		<span class="truncate">{label}</span>
		{#if count}<span class="shrink-0 text-xs text-side-dim">{count}</span>{/if}
	</a>
{/snippet}

<nav aria-label="Storage scope" class="flex flex-col gap-0.5 px-2.5 py-3 text-[13px] select-none">
	<div class="px-2 py-1 text-[11px] font-bold tracking-[.06em] text-side-dim uppercase">
		Projects
	</div>
	{@render row(projectHref(null), 'All projects', num(projects.length), onRoot && !current)}
	{#if expanded && projects.length > 20}
		<input
			bind:value={filter}
			placeholder="Filter {projects.length} projects"
			aria-label="Filter projects"
			class="mx-1 my-1 h-7 rounded border border-side-line bg-side-hover px-2 text-xs text-side-ink placeholder:text-side-dim"
		/>
	{/if}
	{#each shown as p (p.name)}
		{@render row(
			projectHref(p.name),
			p.name,
			`${num(p.vms)} ${p.vms === 1 ? 'VM' : 'VMs'}`,
			p.name === current,
		)}
	{/each}
	{#if hidden > 0}
		<button
			class="{ROW} text-left text-side-dim hover:bg-side-hover hover:text-side-ink"
			aria-expanded={expanded}
			onclick={() => {
				expanded = !expanded;
				filter = '';
			}}
		>
			{expanded ? 'Fewer projects' : `${num(hidden)} more ${hidden === 1 ? 'project' : 'projects'}`}
		</button>
	{/if}

	<div class="px-2 pt-3 pb-1 text-[11px] font-bold tracking-[.06em] text-side-dim uppercase">
		Classes
	</div>
	{#each classes as c (c.name)}
		{@render row(
			hrefForScope({ kind: 'storage', storageClass: c.name }),
			c.name,
			c.tag,
			scope.kind === 'storage' && scope.storageClass === c.name,
		)}
	{:else}
		<p class="px-2 py-1 text-xs text-side-dim">No storage classes visible.</p>
	{/each}
</nav>
