<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { api, Unauthorized } from '$lib/api';
	import { CATALOG_KINDS, catalogHref, catalogKind, catalogRows } from '$lib/catalog';
	import { friendlyError } from '$lib/format';
	import { catalog } from '$lib/state/catalog.svelte';
	import { inventory } from '$lib/state/inventory.svelte';
	import { ui } from '$lib/state/ui.svelte';
	import Breadcrumb from '$lib/components/Breadcrumb.svelte';
	import ErrorNote from '$lib/components/ErrorNote.svelte';
	import TabBar from '$lib/components/TabBar.svelte';

	// The Content Library: VM templates (deployable, git-backed) first, then a
	// read-only browser over the cluster's catalog - boot images (DataSources),
	// instance types, preferences, networks (NADs), storage classes. Same
	// chrome as every container: the kinds are tabs (?kind=), the selected
	// item rides ?item= so the tree, the table and a shared link agree.
	const kind = $derived(catalogKind(page.url.searchParams.get('kind')));
	const picked = $derived(page.url.searchParams.get('item'));
	let error = $state('');

	// Templates re-pull when a merged PR lands (tasksVersion): a template
	// committed through the app appears without a reload. The options catalog
	// re-pulls on entry so a boot-time failure heals here.
	$effect(() => {
		inventory.tasksVersion;
		catalog.load();
	});
	$effect(() => {
		api
			.options()
			.then((o) => (inventory.options = o))
			.catch((e) => {
				if (e instanceof Unauthorized) return;
				error = friendlyError(e);
			});
	});

	const rows = $derived(catalogRows(kind, catalog.templates, inventory.options));
	const pickedRow = $derived(rows?.find((r) => r.key === picked) ?? null);

	// Selection is replaceState, like tabs: back never walks item picks.
	function setPicked(key: string | null) {
		goto(catalogHref(kind, key ?? undefined), {
			replaceState: true,
			keepFocus: true,
			noScroll: true,
		});
	}
</script>

<Breadcrumb trail={[{ label: 'Catalog' }]} />

<TabBar
	class="border-b border-line px-4"
	tabs={CATALOG_KINDS}
	active={kind}
	href={(k) => `?kind=${k}`}
/>

<div class="flex min-h-0 flex-1">
	<div class="min-h-0 flex-1 overflow-y-auto">
		{#if error || catalog.error}
			<ErrorNote error={error || catalog.error} class="m-4" />
		{:else if !rows}
			<p class="py-6 text-center text-sm text-ink-faint">Loading catalog…</p>
		{:else if rows.length === 0}
			<p class="py-6 text-center text-sm text-ink-faint">
				{kind === 'templates'
					? 'No templates yet — save one from a VM (Clone to Template) or commit VirtualMachineTemplate manifests under templates/.'
					: 'None available on this cluster.'}
			</p>
		{:else}
			<table class="w-full text-left text-[13px]">
				<thead class="border-b border-line text-xs text-ink-muted">
					<tr>
						<th class="px-4 py-2 font-medium">Name</th>
						<th class="px-4 py-2 font-medium">Details</th>
					</tr>
				</thead>
				<tbody>
					{#each rows as r (r.key)}
						<tr
							onclick={() => setPicked(picked === r.key ? null : r.key)}
							class="cursor-pointer border-b border-line-soft hover:bg-select-soft {picked === r.key
								? 'bg-select hover:bg-select'
								: ''}"
						>
							<td class="px-4 py-1.5 font-medium text-ink">{r.title}</td>
							<td class="px-4 py-1.5 text-ink-muted">{r.fact}</td>
						</tr>
					{/each}
				</tbody>
			</table>
		{/if}
	</div>

	{#if pickedRow}
		<!-- Detail pane for the selected catalog item. -->
		<aside class="w-80 shrink-0 overflow-y-auto border-l border-line">
			<h3
				class="border-b border-line bg-inset px-3 py-1.5 text-xs font-semibold tracking-wide text-ink-muted uppercase"
			>
				{pickedRow.title}
			</h3>
			<dl class="divide-y divide-line-soft text-[13px]">
				{#each pickedRow.detail as [label, value] (label)}
					<div class="flex justify-between gap-3 px-3 py-1.5">
						<dt class="shrink-0 text-ink-muted">{label}</dt>
						<dd class="min-w-0 truncate text-right text-ink" title={value}>{value}</dd>
					</div>
				{/each}
			</dl>
			{#if pickedRow.template}
				{@const t = pickedRow.template}
				{#if t.parameters?.length}
					<h4
						class="border-y border-line bg-inset px-3 py-1.5 text-xs font-semibold tracking-wide text-ink-muted uppercase"
					>
						Parameters
					</h4>
					<ul class="divide-y divide-line-soft text-[13px]">
						{#each t.parameters as p (p.name)}
							<li class="px-3 py-1.5">
								<div class="flex justify-between gap-3">
									<span class="font-mono text-xs text-ink">{p.name}</span>
									<span class="text-xs text-ink-muted">
										{p.generate
											? 'generated'
											: p.value
												? p.value
												: p.required
													? 'required'
													: 'optional'}
									</span>
								</div>
								{#if p.description}<p class="mt-0.5 text-xs text-ink-faint">{p.description}</p>{/if}
							</li>
						{/each}
					</ul>
				{/if}
				<div class="flex gap-2 border-t border-line p-3">
					{#if !t.error}
						<button
							onclick={() =>
								(ui.modal = { kind: 'deployTemplate', library: t.library, template: t.name })}
							class="flex-1 rounded-full bg-accent px-3 py-1.5 text-xs font-medium text-white hover:bg-accent-hover"
						>
							Deploy…
						</button>
					{/if}
					<button
						onclick={() => (ui.modal = { kind: 'editTemplate', template: t })}
						class="flex-1 rounded border border-line px-3 py-1.5 text-xs font-medium text-ink hover:bg-inset"
					>
						Edit…
					</button>
				</div>
			{/if}
		</aside>
	{/if}
</div>

<footer class="border-t border-line px-4 py-2 text-xs text-ink-faint">
	{kind === 'templates'
		? 'Templates live in git (templates/ in each library repo); deploying stages a VM into Changes — it applies when the PR merges.'
		: 'Read-only — these are platform objects; the New VM wizard consumes them.'}
</footer>
