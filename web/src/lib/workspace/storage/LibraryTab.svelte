<script lang="ts">
	import { ChevronRight, CircleDot, HardDrive, Layers, Upload } from '@lucide/svelte';
	import { storageHref } from '#lib/nav.js';
	import { catalog } from '#lib/state/catalog.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { storage } from '#lib/state/storage.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import {
		FRESH_DAYS,
		STALE_FLAG,
		imageUsage,
		libraryTiles,
		num,
		shortName,
		size,
		staleShare,
		topImages,
		type DiskRow,
		type ImageUse,
	} from '#lib/storage.js';
	import Button from '#lib/components/Button.svelte';
	import Card from '#lib/components/Card.svelte';
	import CountTile from '#lib/components/CountTile.svelte';
	import StageChip from '#lib/components/StageChip.svelte';
	import LocalSharedBar from './LocalSharedBar.svelte';

	// The Library: what needs doing about the disks (four tiles, tinted only
	// when the number is a problem) and where they came from, counted per boot
	// image so the page keeps its size as disks grow.
	let {
		rows,
		project,
		snapshotsLoaded,
	}: { rows: DiskRow[]; project: string; snapshotsLoaded: boolean } = $props();

	const p = $derived(project || undefined);
	const tiles = $derived(libraryTiles(rows, storage.classes ?? [], snapshotsLoaded));
	const uses = $derived(
		imageUsage(rows, inventory.options?.osImages ?? [], catalog.templates ?? []),
	);
	const summary = $derived(topImages(uses, 4));
	const noDataVMs = $derived(new Set(rows.filter((r) => r.ephemeral).map((r) => r.vmKey)).size);

	const full = $derived(tiles.free.filter((f) => f.nearFull));
	const freeLine = $derived(
		[...tiles.free]
			.sort((a, b) => Number(a.free === null) - Number(b.free === null))
			.map((f) =>
				f.free === null
					? `${f.name} does not report free space.`
					: `${f.name}: ${size(f.free)} free.`,
			)
			.join(' '),
	);

	const disksWord = (n: number) => `${num(n)} ${n === 1 ? 'disk' : 'disks'}`;

	function createVM(image: string) {
		ui.modal = { kind: 'newVM', namespaces: null, image };
	}

	// The drop zone hands the file to the upload flow, which asks where it goes.
	let dragging = $state(false);
	function upload(file?: File) {
		ui.modal = { kind: 'upload', file, bootImage: true };
	}
	function drop(e: DragEvent) {
		e.preventDefault();
		dragging = false;
		const f = e.dataTransfer?.files?.[0];
		if (f) upload(f);
	}
</script>

{#snippet snap(u: ImageUse, image: string)}
	{#if !snapshotsLoaded}
		<span class="text-ink-faint">-</span>
	{:else}
		{@const without = u.disks - u.fresh}
		<span>{num(u.fresh)} of {num(u.disks)}</span>
		{#if without === 0}
			<span class="mt-1.5 block text-xs leading-4 text-ink-faint">None without</span>
		{:else if staleShare(u) > STALE_FLAG}
			<span class="mt-1.5 flex items-center gap-1.5 text-xs leading-4">
				<span class="size-[7px] shrink-0 rounded-full bg-warn"></span>
				<a
					class="text-accent-ink hover:underline"
					href={storageHref('disks', { f: 'nosnap', image: image || undefined, project: p })}
					>{num(without)} without, {Math.round(staleShare(u) * 100)}% of them</a
				>
			</span>
		{:else}
			<a
				class="mt-1.5 block text-xs leading-4 text-accent-ink hover:underline"
				href={storageHref('disks', { f: 'nosnap', image: image || undefined, project: p })}
				>{num(without)} without</a
			>
		{/if}
	{/if}
{/snippet}

{#snippet where(u: ImageUse)}
	<LocalSharedBar local={u.local} shared={u.shared} total={u.disks} />
	<span class="mt-1.5 block text-xs leading-4 text-ink-muted"
		>{num(u.local)} local, {num(u.shared)} shared</span
	>
{/snippet}

<section
	aria-label="Needs attention"
	class="grid grid-cols-[repeat(auto-fit,minmax(min(220px,100%),1fr))] gap-3"
>
	{#if tiles.noLiveMigrate > 0}
		<CountTile
			value={tiles.noLiveMigrate}
			label="{tiles.noLiveMigrate === 1 ? 'disk' : 'disks'} cannot live-migrate"
			tone="warn"
			body="On local classes. Their VMs stop when their host goes into maintenance."
			link={{
				label: `Show the ${disksWord(tiles.noLiveMigrate)}`,
				href: storageHref('disks', { f: 'nolm', project: p }),
			}}
		/>
	{:else}
		<CountTile
			label="Every disk can live-migrate"
			tone="ok"
			body="All disks are on shared classes, so maintenance moves their VMs live."
		/>
	{/if}

	{#if tiles.stale === null}
		<CountTile
			label={storage.snapshotsError ? 'Snapshots unavailable' : 'Reading snapshots'}
			body={storage.snapshotsError || 'Counting the disks without a recent snapshot.'}
			link={storage.snapshotsError
				? { label: 'Try again', onclick: () => storage.refreshSnapshots() }
				: undefined}
		/>
	{:else if tiles.stale > 0}
		<CountTile
			value={tiles.stale}
			label="{tiles.stale === 1 ? 'disk' : 'disks'} without a snapshot in {FRESH_DAYS} days"
			tone="warn"
			body="Select them on the Disks tab and snapshot in one go. Runs now, no pull request."
			link={{
				label: `Show the ${disksWord(tiles.stale)}`,
				href: storageHref('disks', { f: 'nosnap', project: p }),
			}}
		/>
	{:else}
		<CountTile
			label="Every disk has a recent snapshot"
			tone="ok"
			body="Each disk was captured in the last {FRESH_DAYS} days."
		/>
	{/if}

	<CountTile
		value={noDataVMs}
		label="{noDataVMs === 1 ? 'VM keeps' : 'VMs keep'} no data"
		body="No persistent disk. What they write is lost on restart. Fine for stateless VMs."
		link={noDataVMs
			? {
					label: `Show the ${num(noDataVMs)} ${noDataVMs === 1 ? 'VM' : 'VMs'}`,
					href: storageHref('disks', { f: 'nodata', project: p }),
				}
			: undefined}
	/>

	{#if full.length}
		<CountTile
			label="{full.map((f) => f.name).join(', ')} {full.length === 1 ? 'is' : 'are'} close to full"
			tone="danger"
			body={freeLine}
			link={{ label: 'Open the Classes tab', href: storageHref('classes', { project: p }) }}
		/>
	{:else if tiles.free.some((f) => f.free !== null)}
		<CountTile
			label="Nothing close to full"
			tone="ok"
			body={freeLine}
			link={{ label: 'Open the Classes tab', href: storageHref('classes', { project: p }) }}
		/>
	{:else}
		<CountTile
			label="Free space not reported"
			body={tiles.free.length
				? 'No class here reports free space, so dotvirt cannot tell how full it is.'
				: 'No storage classes visible yet.'}
			link={{ label: 'Open the Classes tab', href: storageHref('classes', { project: p }) }}
		/>
	{/if}
</section>

<Card
	title="Disks by boot image"
	flush
	link={{
		label: `All ${num(summary.total)} images on the Images tab`,
		href: storageHref('images', { project: p }),
	}}
>
	<div class="flex flex-wrap gap-x-6 gap-y-1.5 px-card pt-3.5 pb-0.5 text-xs text-ink-muted">
		<span class="flex items-center gap-1.5">
			<i class="block size-2.5 rounded-[2px] bg-glyph"></i>Local class: the disk stays on one host,
			its VM cannot live-migrate
		</span>
		<span class="flex items-center gap-1.5">
			<i class="block size-2.5 rounded-[2px] bg-glyph-2"></i>Shared class: its VM can live-migrate
		</span>
	</div>

	<div class="overflow-x-auto px-1.5 pt-1">
		<table class="w-full border-collapse text-sm tabular-nums">
			<thead>
				<tr class="border-b border-line text-left text-xs font-bold text-ink-muted">
					<th scope="col" class="px-3 py-2 whitespace-nowrap">Image</th>
					<th scope="col" class="px-3 py-2 text-right whitespace-nowrap">
						<ChevronRight size={12} class="mr-1 inline align-[-1px] text-ink-faint" />Disks made
						from it
					</th>
					<th scope="col" class="px-3 py-2 whitespace-nowrap">Where they live</th>
					<th scope="col" class="px-3 py-2 whitespace-nowrap">
						<ChevronRight size={12} class="mr-1 inline align-[-1px] text-ink-faint" />Snapshot in {FRESH_DAYS}
						days
					</th>
					<th scope="col" class="px-3 py-2"><span class="sr-only">Actions</span></th>
				</tr>
			</thead>
			<tbody class="[&>tr:last-child>td]:border-b-0">
				{#each summary.top as u (u.image)}
					{@const name = shortName(u.image)}
					<tr>
						<td class="border-b border-line-soft p-3">
							<div class="flex items-center gap-2.5">
								<CircleDot size={16} class="shrink-0 text-ink-faint" />
								<div class="min-w-0">
									<span class="font-mono font-medium">{name}</span>
									<span class="block text-xs leading-4 text-ink-faint">
										{#if u.templates.length}
											{u.templates.length === 1 ? 'Template' : 'Templates'}
											{u.templates.join(', ')}
										{:else}
											In {u.image.split('/')[0]}
										{/if}
									</span>
								</div>
							</div>
						</td>
						<td class="border-b border-line-soft p-3 text-right">
							<span class="text-base font-bold">{num(u.disks)}</span>
						</td>
						<td class="border-b border-line-soft p-3">{@render where(u)}</td>
						<td class="border-b border-line-soft p-3">{@render snap(u, u.image)}</td>
						<td class="border-b border-line-soft p-3">
							<div class="flex justify-end gap-1.5">
								<Button
									variant="outline"
									size="sm"
									aria-label="Create VM from {name}"
									disabled={u.catalog ? !u.catalog.ready : true}
									title={!u.catalog
										? 'Not in the catalog: the image is gone or not readable here'
										: u.catalog.ready
											? ''
											: 'The image is still importing'}
									onclick={() => createVM(u.image)}>Create VM</Button
								>
								<Button
									variant="secondary"
									size="sm"
									aria-label="Open {name}"
									href={storageHref('images', { image: u.image, project: p })}>Open</Button
								>
							</div>
						</td>
					</tr>
				{/each}
				{#if summary.more}
					{@const more = summary.more}
					<tr>
						<td class="border-b border-line-soft p-3">
							<div class="flex items-center gap-2.5">
								<Layers size={16} class="shrink-0 text-ink-faint" />
								<div>
									<span>{num(more.count)} more {more.count === 1 ? 'image' : 'images'}</span>
									<span class="block text-xs leading-4 text-ink-faint"
										>Sorted by use on the Images tab</span
									>
								</div>
							</div>
						</td>
						<td class="border-b border-line-soft p-3 text-right">
							<span class="text-base font-bold">{num(more.disks)}</span>
						</td>
						<td class="border-b border-line-soft p-3">{@render where(more)}</td>
						<td class="border-b border-line-soft p-3">{@render snap(more, '')}</td>
						<td class="border-b border-line-soft p-3">
							<div class="flex justify-end">
								<Button variant="secondary" size="sm" href={storageHref('images', { project: p })}
									>Show all {num(more.count)}</Button
								>
							</div>
						</td>
					</tr>
				{/if}
				{#if summary.none}
					{@const none = summary.none}
					<tr>
						<td class="border-b border-line-soft p-3">
							<div class="flex items-center gap-2.5">
								<HardDrive size={16} class="shrink-0 text-ink-faint" />
								<div>
									<span>Not from an image</span>
									<span class="block text-xs leading-4 text-ink-faint"
										>Blank, imported or cloned disks</span
									>
								</div>
							</div>
						</td>
						<td class="border-b border-line-soft p-3 text-right">
							<span class="text-base font-bold">{num(none.disks)}</span>
						</td>
						<td class="border-b border-line-soft p-3">{@render where(none)}</td>
						<td class="border-b border-line-soft p-3">
							{#if snapshotsLoaded}
								<span>{num(none.fresh)} of {num(none.disks)}</span>
							{:else}<span class="text-ink-faint">-</span>{/if}
						</td>
						<td class="border-b border-line-soft p-3"></td>
					</tr>
				{/if}
				{#if !summary.top.length && !summary.none}
					<tr>
						<td colspan="5" class="p-3 text-[13px] text-ink-faint">
							{storage.rows.length ? 'No disks in this project.' : 'No VM disks yet.'}
						</td>
					</tr>
				{/if}
			</tbody>
		</table>
	</div>

	<div
		role="region"
		aria-label="Add a boot image"
		ondragover={(e) => {
			e.preventDefault();
			dragging = true;
		}}
		ondragleave={() => (dragging = false)}
		ondrop={drop}
		class="mx-[18px] mt-3 flex flex-wrap items-center gap-x-3.5 gap-y-2.5 rounded-lg border border-dashed px-3.5 py-3 {dragging
			? 'border-accent bg-select-soft'
			: 'border-line-strong bg-canvas/40'}"
	>
		<Upload size={20} class="shrink-0 text-accent-ink" />
		<div class="min-w-0 flex-[1_1_240px] text-sm">
			<strong>Add a boot image.</strong>
			<span class="text-ink-faint">Drop a qcow2, raw or iso file here.</span>
		</div>
		<Button
			variant="secondary"
			size="sm"
			disabled={!inventory.namespaces.length}
			onclick={() => upload()}>Choose file</Button
		>
		<StageChip kind="now" />
		<span class="text-xs text-ink-faint">Streams to the cluster. No pull request.</span>
	</div>

	<div class="flex flex-wrap items-center gap-2 px-card py-3.5 text-xs leading-4 text-ink-faint">
		<StageChip kind="pr" />
		<span
			>Create VM opens the create sheet with the image picked. Staging it opens a pull request in
			the chosen project.</span
		>
	</div>
</Card>
