<script lang="ts">
	import { page } from '$app/state';
	import { CircleDot, X } from '@lucide/svelte';
	import { storageHref } from '#lib/nav.js';
	import { catalog } from '#lib/state/catalog.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { storage } from '#lib/state/storage.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import {
		FRESH_DAYS,
		imageUsage,
		num,
		shortName,
		type DiskRow,
		type ImageUse,
	} from '#lib/storage.js';
	import Button from '#lib/components/Button.svelte';
	import LocalSharedBar from './LocalSharedBar.svelte';
	import Pager from '#lib/components/Pager.svelte';
	import { DEFAULT_PAGE_SIZE, pageSlice } from '#lib/paging.js';

	// Every boot image with what was made from it, biggest first; one opens
	// beside the list with the way to a new VM and to its disks.
	let {
		rows,
		project,
		go,
	}: {
		rows: DiskRow[];
		project: string;
		go: (params: Record<string, string | undefined>, opts?: { replace?: boolean }) => void;
	} = $props();

	const p = $derived(project || undefined);
	const images = $derived(
		imageUsage(rows, inventory.options?.osImages ?? [], catalog.templates ?? []).filter(
			(u) => u.image,
		),
	);
	const size = $derived(Number(page.url.searchParams.get('per')) || DEFAULT_PAGE_SIZE);
	const s = $derived(
		pageSlice(images.length, Number(page.url.searchParams.get('page')) || 1, size),
	);
	const pageRows = $derived(images.slice(s.start, s.end));
	const openKey = $derived(page.url.searchParams.get('image'));
	const open = $derived(openKey ? (images.find((u) => u.image === openKey) ?? null) : null);

	const imageState = (u: ImageUse) =>
		!u.catalog ? 'Not in the catalog' : u.catalog.ready ? 'Ready' : 'Importing';
	const createVM = (u: ImageUse) =>
		(ui.modal = { kind: 'newVM', namespaces: null, image: u.image });
	const TH =
		'border-b border-line px-3 py-2 text-left text-xs font-bold whitespace-nowrap text-ink-muted';
	const TD = 'border-b border-line-soft px-3 py-2.5';
</script>

<div class="flex flex-wrap items-start gap-4">
	<section
		aria-labelledby="images-h"
		class="flex min-w-0 flex-[2_1_640px] flex-col rounded-lg border border-line bg-panel [--card-x:18px]"
	>
		<div
			class="flex flex-wrap items-center gap-3 rounded-t-[7px] border-b border-line-soft bg-inset px-card py-3"
		>
			<h2 id="images-h" class="text-sm leading-5 font-bold">Boot images</h2>
			<span class="text-[13px] text-ink-faint">Sorted by the disks made from them</span>
			<span class="ml-auto text-[13px]">
				<button
					class="text-accent-ink hover:underline"
					onclick={() => (ui.modal = { kind: 'upload', bootImage: true })}>Add a boot image</button
				>
			</span>
		</div>
		<div class="overflow-x-auto">
			<table class="w-full border-collapse text-[13px] tabular-nums">
				<thead>
					<tr>
						<th scope="col" class="{TH} pl-card">Image</th>
						<th scope="col" class={TH}>State</th>
						<th scope="col" class="{TH} text-right">Disks made from it</th>
						<th scope="col" class={TH}>Where they live</th>
						<th scope="col" class={TH}>Snapshot in {FRESH_DAYS} days</th>
						<th scope="col" class="{TH} pr-card"><span class="sr-only">Actions</span></th>
					</tr>
				</thead>
				<tbody>
					{#each pageRows as u (u.image)}
						{@const current = u.image === openKey}
						<tr
							class="cursor-pointer {current
								? 'bg-select shadow-[inset_3px_0_0_var(--color-accent)]'
								: 'hover:bg-select-soft'}"
							onclick={() => go({ image: current ? undefined : u.image }, { replace: true })}
						>
							<td class="{TD} pl-card">
								<div class="flex items-center gap-2.5">
									<CircleDot size={16} class="shrink-0 text-ink-faint" />
									<div class="min-w-0">
										<button
											class="font-mono font-medium hover:text-accent-ink"
											aria-expanded={current}
											onclick={(e) => {
												e.stopPropagation();
												go({ image: current ? undefined : u.image }, { replace: true });
											}}>{shortName(u.image)}</button
										>
										<span class="block text-xs leading-4 text-ink-faint">
											{u.templates.length
												? `${u.templates.length === 1 ? 'Template' : 'Templates'} ${u.templates.join(', ')}`
												: `In ${u.image.split('/')[0]}`}
										</span>
									</div>
								</div>
							</td>
							<td class={TD}>
								<span class={u.catalog?.ready ? 'text-ink-soft' : 'text-warn-ink'}
									>{imageState(u)}</span
								>
							</td>
							<td class="{TD} text-right font-bold">{num(u.disks)}</td>
							<td class={TD}>
								{#if u.disks}
									<LocalSharedBar local={u.local} shared={u.shared} total={u.disks} />
									<span class="mt-1 block text-xs text-ink-muted"
										>{num(u.local)} local, {num(u.shared)} shared</span
									>
								{:else}<span class="text-ink-faint">No disks yet</span>{/if}
							</td>
							<td class={TD}>
								{#if !u.disks}<span class="text-ink-faint">-</span>
								{:else if storage.snapshots === null}<span class="text-ink-faint">-</span>
								{:else}{num(u.fresh)} of {num(u.disks)}{/if}
							</td>
							<td class="{TD} pr-card">
								<div class="flex justify-end gap-1.5">
									<Button
										variant="outline"
										size="sm"
										aria-label="Create VM from {shortName(u.image)}"
										disabled={!u.catalog?.ready}
										onclick={(e) => {
											e.stopPropagation();
											createVM(u);
										}}>Create VM</Button
									>
								</div>
							</td>
						</tr>
					{:else}
						<tr>
							<td colspan="6" class="px-card py-6 text-center text-ink-faint">
								No boot images visible. Add one to offer it to new VMs.
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
		<Pager
			class="border-t border-line px-card py-3"
			total={s.total}
			bind:page={
				() => s.page, (n) => go({ page: n > 1 ? String(n) : undefined }, { replace: true })
			}
			bind:size={
				() => size,
				(n) => go({ per: n === DEFAULT_PAGE_SIZE ? undefined : String(n) }, { replace: true })
			}
		/>
	</section>

	{#if open}
		<aside
			aria-labelledby="image-panel-h"
			class="flex min-w-0 flex-[1_1_320px] flex-col rounded-lg border border-line bg-panel [--card-x:18px]"
		>
			<div
				class="flex items-start gap-2 rounded-t-[7px] border-b border-line-soft bg-inset px-card py-3"
			>
				<div class="min-w-0 flex-1">
					<h2 id="image-panel-h" class="font-mono text-base leading-6 font-medium">
						{shortName(open.image)}
					</h2>
					<span class="block text-[13px] text-ink-muted"
						>Boot image in {open.image.split('/')[0]}. {imageState(open)}.</span
					>
				</div>
				<button
					class="flex size-[30px] shrink-0 items-center justify-center rounded-md text-ink-muted hover:bg-inset-strong"
					aria-label="Close {shortName(open.image)}"
					onclick={() => go({ image: undefined }, { replace: true })}><X size={16} /></button
				>
			</div>
			<dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 px-card py-3.5 text-[13px]">
				<dt class="text-ink-muted">Disks made from it</dt>
				<dd>{num(open.disks)}</dd>
				<dt class="text-ink-muted">Where they live</dt>
				<dd>{num(open.local)} local, {num(open.shared)} shared</dd>
				<dt class="text-ink-muted">Snapshot in {FRESH_DAYS} days</dt>
				<dd>{storage.snapshots === null ? '-' : `${num(open.fresh)} of ${num(open.disks)}`}</dd>
				<dt class="text-ink-muted">Templates</dt>
				<dd>{open.templates.join(', ') || 'None'}</dd>
			</dl>
			<div class="flex flex-wrap gap-2 border-t border-line-soft px-card py-3.5">
				<Button
					variant="outline"
					size="sm"
					disabled={!open.catalog?.ready}
					onclick={() => createVM(open)}>Create VM</Button
				>
				{#if open.disks}
					<Button
						variant="secondary"
						size="sm"
						href={storageHref('disks', { image: open.image, project: p })}
						>Show the {num(open.disks)} {open.disks === 1 ? 'disk' : 'disks'}</Button
					>
				{/if}
			</div>
		</aside>
	{/if}
</div>
