<script lang="ts">
	import { untrack } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { Search } from '@lucide/svelte';
	import { STORAGE_TABS, storageHref, storageTab } from '#lib/nav.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { storage } from '#lib/state/storage.svelte.js';
	import { storageContext } from '#lib/storage.js';
	import Breadcrumb from '#lib/components/Breadcrumb.svelte';
	import PageHeader from '#lib/components/PageHeader.svelte';
	import TabBar from '#lib/components/TabBar.svelte';
	import ClassesTab from './ClassesTab.svelte';
	import DisksTab from './DisksTab.svelte';
	import ImagesTab from './ImagesTab.svelte';
	import LibraryTab from './LibraryTab.svelte';
	import SnapshotsTab from './SnapshotsTab.svelte';

	// The Storage section root: one header over five tabs, each one job. The
	// view is the URL (tab, project scope, filters, the open disk), so every
	// count on the Library links into a view the back button and a shared link
	// both reach.
	storage.ensure();

	const tab = $derived(storageTab(page.url.searchParams.get('tab')));
	const project = $derived(page.url.searchParams.get('project') ?? '');
	const rows = $derived(project ? storage.rows.filter((r) => r.project === project) : storage.rows);
	const disks = $derived(storage.rows.filter((r) => !r.ephemeral).length);
	const namespaces = $derived(
		new Set(
			inventory.projects
				.filter((p) => !project || p.name === project)
				.flatMap((p) => p.namespaces.map((n) => n.namespace)),
		),
	);
	const snapshots = $derived(
		storage.snapshots?.filter((s) => !project || namespaces.has(s.namespace ?? '')) ?? null,
	);

	// The header and the tab counts speak for the whole platform; the project
	// narrows what the tabs list, like any other filter.
	const context = $derived(
		storageContext(storage.rows, storage.classNames.length, storage.hostCount),
	);
	const tabs = $derived(
		STORAGE_TABS.map((t) => ({
			id: t.id,
			label: t.label,
			count:
				t.id === 'disks'
					? disks
					: t.id === 'images'
						? storage.images.filter((u) => u.image).length
						: t.id === 'snapshots'
							? (storage.snapshots?.length ?? undefined)
							: t.id === 'classes'
								? storage.classNames.length
								: undefined,
		})),
	);
	const label = $derived(STORAGE_TABS.find((t) => t.id === tab)!.label);

	// Moves to another view of the section. replace keeps typing and paging out
	// of history; a tab or a drill-in is a step back can return to.
	function go(params: Record<string, string | undefined>, opts: { replace?: boolean } = {}) {
		const q = new URLSearchParams(page.url.search);
		for (const [k, v] of Object.entries(params)) {
			if (v) q.set(k, v);
			else q.delete(k);
		}
		goto(`/storage?${q}`, { replace: opts.replace ?? false, reset: false });
	}

	// The search finds disks: it lands on the Disks tab, filtered as you type.
	let query = $state('');
	$effect(() => {
		const q = page.url.searchParams.get('q') ?? '';
		// The URL holds the trimmed text; a trailing space being typed stays.
		if (q !== untrack(() => query).trim()) query = q;
	});
	function search(value: string) {
		query = value;
		go(
			{ q: value.trim() || undefined, tab: 'disks', page: undefined },
			{ replace: tab === 'disks' },
		);
	}
</script>

<div class="min-h-0 flex-1 overflow-y-auto bg-canvas">
	<div class="flex min-h-full flex-col gap-[18px] px-7 pt-5 pb-8">
		<div class="flex flex-col gap-2">
			<Breadcrumb
				trail={[{ label: 'Storage', href: storageHref('library', { project }) }, { label }]}
			/>
			<PageHeader title="Storage" {context}>
				{#snippet actions()}
					<label class="relative block w-[340px] max-w-full min-w-[200px]">
						<Search
							size={16}
							class="pointer-events-none absolute top-[9px] left-2.5 text-ink-faint"
						/>
						<span class="sr-only">Find a disk, VM or image</span>
						<input
							type="search"
							value={query}
							oninput={(e) => search(e.currentTarget.value)}
							placeholder="Find a disk, VM or image"
							class="h-[34px] w-full rounded-md border border-line-strong bg-panel pr-3 pl-[34px] text-sm text-ink placeholder:text-ink-faint"
						/>
					</label>
				{/snippet}
			</PageHeader>
			<TabBar
				class="mt-1"
				{tabs}
				active={tab}
				href={(id) => storageHref(storageTab(id), { project })}
			/>
		</div>

		{#if project && tab !== 'disks'}
			<p class="-mt-2 text-[13px] text-ink-muted">
				Showing <strong class="text-ink">{project}</strong> only.
				<a class="text-accent-ink hover:underline" href={storageHref(tab)}>Show all projects</a>
			</p>
		{/if}

		{#if tab === 'library'}
			<LibraryTab {rows} {project} snapshotsLoaded={snapshots !== null} />
		{:else if tab === 'disks'}
			<DisksTab rows={storage.rows} {go} />
		{:else if tab === 'images'}
			<ImagesTab {rows} {project} {go} />
		{:else if tab === 'snapshots'}
			<SnapshotsTab snapshots={snapshots ?? []} loaded={snapshots !== null} {go} />
		{:else}
			<ClassesTab {rows} />
		{/if}
	</div>
</div>
