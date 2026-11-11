<script lang="ts">
	import { branding } from '#lib/state/branding.svelte.js';
	import { untrack } from 'svelte';
	import { page } from '$app/state';
	import { CircleDot, X } from '@lucide/svelte';
	import { api, type UploadStatus } from '#lib/api.js';
	import { imageGate } from '#lib/gates.js';
	import { storageHref } from '#lib/nav.js';
	import { action, resource } from '#lib/resource.svelte.js';
	import { templates } from '#lib/state/templates.svelte.js';
	import { inventory } from '#lib/state/inventory.svelte.js';
	import { storage } from '#lib/state/storage.svelte.js';
	import { ui } from '#lib/state/ui.svelte.js';
	import {
		FRESH_DAYS,
		imageDeleteConsequence,
		imageProgress,
		imageUsage,
		num,
		shortName,
		type DiskRow,
		type ImageUse,
	} from '#lib/storage.js';
	import Button from '#lib/components/Button.svelte';
	import Consequence from '#lib/components/Consequence.svelte';
	import ErrorNote from '#lib/components/ErrorNote.svelte';
	import StageChip from '#lib/components/StageChip.svelte';
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
		imageUsage(rows, inventory.options?.osImages ?? [], templates.list ?? []).filter(
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
		!u.catalog ? 'Not in the catalog' : u.catalog.ready ? 'Ready' : 'Not ready';
	const nsOf = (image: string) => image.slice(0, image.indexOf('/'));

	// A boot image dotvirt added that is not ready says where it stands once
	// opened: one status read per open (or per Check again), never one per
	// row. Its DataVolume shares its name; one added outside dotvirt may not.
	const pending = $derived(
		open?.catalog && !open.catalog.ready && open.catalog.removable ? open.image : '',
	);
	const status = resource<UploadStatus | null>(
		() => pending,
		(k) => (k ? api.uploadStatus(nsOf(k), shortName(k)) : Promise.resolve(null)),
		{ reset: true },
	);
	const sentence = (t: string) => (/[.!?]$/.test(t) ? t : `${t}.`);
	// VMs git declares from it: never cloned, they wait for it to be ready.
	const waiting = $derived(
		pending ? new Set(rows.filter((r) => r.image === pending).map((r) => r.vmKey)).size : 0,
	);
	const progress = $derived(
		pending && status.data ? imageProgress(status.data, !!open?.catalog?.import) : null,
	);

	// Deleting one that never finished: the DataSource, its disk and claim.
	const removal = action();
	let armed = $state('');
	$effect(() => {
		void openKey;
		untrack(() => {
			armed = '';
			removal.clear();
		});
	});
	async function remove(u: ImageUse) {
		if (armed !== u.image) {
			armed = u.image;
			return;
		}
		const name = shortName(u.image);
		let note = '';
		const del = async () => {
			note = (await api.deleteImage(nsOf(u.image), name)).note ?? '';
		};
		if (!(await removal.run(del))) return;
		armed = '';
		ui.showToast(`Boot image ${name} deleted.${note ? ` ${note}` : ''}`, { kind: 'success' });
		go({ image: undefined }, { replace: true });
		await inventory.loadOptions(true);
	}
	const createVM = (u: ImageUse) =>
		(ui.modal = { kind: 'newVM', namespaces: null, image: u.image });
	const TH =
		'border-b border-line px-2 py-2 text-left align-bottom text-xs font-bold text-ink-muted';
	const TD = 'border-b border-line-soft px-2 py-2.5 whitespace-nowrap';
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
			<!-- Fixed shares: beside the open panel a long name is cut instead of
			     pushing the columns past the card. -->
			<table class="w-full min-w-[620px] table-fixed border-collapse text-[13px] tabular-nums">
				<thead>
					<tr>
						<th scope="col" class="{TH} pl-card">Image</th>
						<th scope="col" class="{TH} w-[12%]">State</th>
						<th scope="col" class="{TH} w-[12%] text-right">Disks made from it</th>
						<th scope="col" class="{TH} w-[21%]">Where they live</th>
						<th scope="col" class="{TH} w-[14%]">Snapshot in {FRESH_DAYS} days</th>
						<th scope="col" class="{TH} w-[16%] pr-card"><span class="sr-only">Actions</span></th>
					</tr>
				</thead>
				<tbody>
					{#each pageRows as u (u.image)}
						{@const current = u.image === openKey}
						{@const origin = u.templates.length
							? `${u.templates.length === 1 ? 'Template' : 'Templates'} ${u.templates.join(', ')}`
							: `In ${u.image.split('/')[0]}`}
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
											class="block max-w-full truncate text-left font-mono font-medium hover:text-accent-ink"
											title={shortName(u.image)}
											aria-expanded={current}
											onclick={(e) => {
												e.stopPropagation();
												go({ image: current ? undefined : u.image }, { replace: true });
											}}>{shortName(u.image)}</button
										>
										<span class="block truncate text-xs leading-4 text-ink-faint" title={origin}
											>{origin}</span
										>
									</div>
								</div>
							</td>
							<td class={TD}>
								<span
									class="block truncate {u.catalog?.ready ? 'text-ink-soft' : 'text-warn-ink'}"
									title={imageState(u)}>{imageState(u)}</span
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
			{#if pending && open.catalog}
				{@const gate = imageGate.delete(nsOf(open.image))}
				{@const moving = progress?.tone === 'info'}
				<div class="flex flex-col gap-2.5 border-b border-line-soft px-card py-3.5">
					{#if progress}
						<Consequence tone={progress.tone}
							>{sentence(progress.text)}
							<button
								class="text-accent-ink underline underline-offset-2"
								onclick={() => status.refresh()}>Check again</button
							></Consequence
						>
					{:else if status.error}
						<Consequence tone="warn"
							>{sentence(`Cannot read where it stands: ${status.error}`)}
							<button
								class="text-accent-ink underline underline-offset-2"
								onclick={() => status.refresh()}>Try again</button
							></Consequence
						>
					{:else}
						<p class="text-[13px] text-ink-faint">Reading where it stands.</p>
					{/if}
					<div class="flex flex-col gap-1.5">
						<div class="flex flex-wrap items-center gap-2">
							<Button
								variant={armed === open.image ? 'danger' : 'danger-secondary'}
								size="sm"
								disabled={removal.busy || !gate.ok}
								title={gate.why || undefined}
								onclick={() => open && remove(open)}
								>{armed === open.image ? 'Confirm delete' : 'Delete'}</Button
							>
							<StageChip kind="now" />
						</div>
						{#if !gate.ok}<p class="text-xs leading-4 text-ink-faint">{gate.why}</p>{/if}
						<Consequence tone="danger" size="sm"
							>{imageDeleteConsequence(
								shortName(open.image),
								waiting,
								moving ? (open.catalog.import ? 'download' : 'import') : '',
							)}</Consequence
						>
						<ErrorNote error={removal.error} />
					</div>
				</div>
			{:else if open.catalog && !open.catalog.ready}
				<p class="border-b border-line-soft px-card py-3.5 text-xs leading-4 text-ink-faint">
					Added outside {branding.name}, so it is not deleted here.
				</p>
			{/if}
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
